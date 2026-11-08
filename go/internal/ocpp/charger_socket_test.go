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

	"github.com/srcfl/ftw/go/internal/ocpp/chargersocket"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

func wsURL(port int) string { return fmt.Sprintf("ws://127.0.0.1:%d", port) }

// dialChargePoint16 starts a 1.6 charge point on a chargersocket.Socket.
// setup runs before Start, to register handlers; client may carry credentials
// or TLS. The stop it returns runs once and is also registered as cleanup, so
// a test may disconnect on purpose.
func dialChargePoint16(t *testing.T, url, id string, client *ws.Client, setup func(ocpp16.ChargePoint)) (ocpp16.ChargePoint, func(), error) {
	t.Helper()
	cp := chargersocket.ChargePoint16(id, client)
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
	cs := chargersocket.ChargingStation201(id, nil)
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
// take it before Start checks it, or between that check and the library's
// bind. In the first case Start fails, or for 2.0.1 starts without that
// listener; in the second the library gives up on the listener without an
// error, so a listener goroutine that exits early means the port was lost.
// Either way, try fresh ones.
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
		var taken *net.OpError
		if errors.As(err, &taken) && attempt < attempts {
			continue // another test took the port after freePort gave it back
		}
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if (!v201 || srv.csms != nil) && listening(t, srv) {
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
