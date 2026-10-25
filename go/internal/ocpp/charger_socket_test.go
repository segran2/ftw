package ocpp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	ocpp16 "github.com/lorenzodonini/ocpp-go/ocpp1.6"
	ocpp201 "github.com/lorenzodonini/ocpp-go/ocpp2.0.1"
	"github.com/lorenzodonini/ocpp-go/ws"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// chargerSocket is the WebSocket client every test charger dials with. It
// closes three races that ocpp-go v0.19.0's ws.Client has on close, each of
// which has failed CI:
//
//   - Write checks the connection, then queues without a lock, while the write
//     pump closes that queue on Stop. A server request answered as the charger
//     stops sends on a closed channel and panics (#1712).
//   - ocppj.Client.Stop installs its own disconnect handler while the write
//     pump may be reading the old one after the server closed: a data race.
//   - If the pump has already called the old handler, ocppj.Client.Stop waits
//     for a call that never comes, and the package hits its timeout.
//
// It serializes Write with Stop, keeps the disconnect handler itself, and
// passes a disconnect the pump has already reported to the handler Stop
// installed. A Write while the server closes the socket can still meet the
// first race; chargers therefore stop before the server, at cleanup.
type chargerSocket struct {
	*ws.Client

	mu       sync.Mutex
	stopping bool
	onDown   func(error)
	onUp     func()
	// installed counts disconnect handlers; reportedTo is the one that was
	// told of the current disconnect, 0 while connected.
	installed, reportedTo int
}

func newChargerSocket(client *ws.Client) *chargerSocket {
	s := &chargerSocket{Client: client}
	// Set once, before the pumps start, so the library never sees a change.
	client.SetDisconnectedHandler(s.disconnected)
	client.SetReconnectedHandler(s.reconnected)
	return s
}

func (s *chargerSocket) SetDisconnectedHandler(fn func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDown = fn
	s.installed++
}

func (s *chargerSocket) SetReconnectedHandler(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onUp = fn
}

func (s *chargerSocket) disconnected(err error) {
	s.mu.Lock()
	fn := s.onDown
	s.reportedTo = s.installed
	s.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

func (s *chargerSocket) reconnected() {
	s.mu.Lock()
	fn := s.onUp
	s.reportedTo = 0
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (s *chargerSocket) Start(url string) error {
	s.mu.Lock()
	s.stopping, s.reportedTo = false, 0
	s.mu.Unlock()
	return s.Client.Start(url)
}

func (s *chargerSocket) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return errors.New("test charger is stopping")
	}
	return s.Client.Write(data)
}

func (s *chargerSocket) Stop() {
	s.mu.Lock()
	s.stopping = true
	var missed func(error)
	if s.reportedTo != 0 && s.reportedTo != s.installed {
		missed, s.reportedTo = s.onDown, s.installed
	}
	s.mu.Unlock()
	s.Client.Stop()
	if missed != nil {
		missed(nil)
	}
}

func wsURL(port int) string { return fmt.Sprintf("ws://127.0.0.1:%d", port) }

// dialChargePoint16 starts a 1.6 charge point on a chargerSocket. setup runs
// before Start, to register handlers; client may carry credentials or TLS.
// The stop it returns runs once and is also registered as cleanup, so a test
// may disconnect on purpose.
func dialChargePoint16(t *testing.T, url, id string, client *ws.Client, setup func(ocpp16.ChargePoint)) (ocpp16.ChargePoint, func(), error) {
	t.Helper()
	if client == nil {
		client = ws.NewClient()
	}
	cp := ocpp16.NewChargePoint(id, nil, newChargerSocket(client))
	if setup != nil {
		setup(cp)
	}
	err := cp.Start(url)
	var once sync.Once
	stop := func() { once.Do(cp.Stop) }
	t.Cleanup(stop)
	return cp, stop, err
}

// dialChargingStation201 is dialChargePoint16 for OCPP 2.0.1.
func dialChargingStation201(t *testing.T, url, id string, setup func(ocpp201.ChargingStation)) (ocpp201.ChargingStation, func(), error) {
	t.Helper()
	cs := ocpp201.NewChargingStation(id, nil, newChargerSocket(ws.NewClient()))
	if setup != nil {
		setup(cs)
	}
	err := cs.Start(url)
	var once sync.Once
	stop := func() { once.Do(cs.Stop) }
	t.Cleanup(stop)
	return cs, stop, err
}

// startOCPP starts cfg on free ports, with a 2.0.1 listener too when v201 is
// set, and returns once every listener accepts. The server stops at cleanup,
// after the chargers a test dials later.
//
// freePort hands back a port the kernel just released, and another socket can
// take it before the library binds it. The library then gives up on that
// listener without an error, so a listener goroutine that exits early means
// the port was lost: try fresh ones.
func startOCPP(t *testing.T, cfg Config, tel *telemetry.Store, v201 bool) *Server {
	t.Helper()
	const attempts = 5
	for attempt := 1; ; attempt++ {
		c := cfg
		c.Port = freePort(t)
		if v201 {
			c.PortV201 = freePort(t)
		}
		srv, err := Start(context.Background(), &c, tel)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if listening(t, srv) {
			t.Cleanup(srv.Stop)
			return srv
		}
		srv.Stop()
		if attempt == attempts {
			t.Fatalf("no listener bound after %d attempts", attempts)
		}
	}
}

// listening waits until each listener of srv accepts a connection, and
// reports false as soon as one has given up on its port.
func listening(t *testing.T, srv *Server) bool {
	t.Helper()
	type listener struct {
		port int
		done <-chan struct{}
	}
	listeners := []listener{{srv.cfg.Port, srv.done}}
	if srv.doneV201 != nil {
		listeners = append(listeners, listener{srv.cfg.PortV201, srv.doneV201})
	}
	// Only a hung listener reaches this deadline.
	deadline := time.Now().Add(10 * time.Second)
	for _, l := range listeners {
		for {
			select {
			case <-l.done:
				return false
			default:
			}
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", l.port), 100*time.Millisecond)
			if err == nil {
				c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("listener on port %d neither accepted nor exited", l.port)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return true
}

// The write pump reports a server-side close to the handler of the moment.
// When ocppj.Client.Stop installs its own handler after that, Stop must
// still hear of the disconnect, once, or it waits forever.
func TestChargerSocketPassesAMissedDisconnectToStop(t *testing.T) {
	s := newChargerSocket(ws.NewClient())
	var first, second int
	s.SetDisconnectedHandler(func(error) { first++ })
	s.disconnected(errors.New("server closed")) // as the write pump does
	s.SetDisconnectedHandler(func(error) { second++ })
	s.Stop()
	s.Stop()
	if first != 1 || second != 1 {
		t.Fatalf("disconnect reached the first handler %d times and Stop's %d times, want once each", first, second)
	}
	if err := s.Write([]byte("[]")); err == nil {
		t.Fatal("a stopped charger still wrote")
	}
}
