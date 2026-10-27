// Package chargersocket is the WebSocket client every simulated or test
// charger in this module dials Core with. Core itself is the OCPP server and
// never dials.
package chargersocket

import (
	"errors"
	"sync"

	ocpp16 "github.com/lorenzodonini/ocpp-go/ocpp1.6"
	ocpp201 "github.com/lorenzodonini/ocpp-go/ocpp2.0.1"
	"github.com/lorenzodonini/ocpp-go/ws"
)

// Socket closes three races that ocpp-go v0.19.0's ws.Client has on close,
// each of which has failed CI:
//
//   - Write checks the connection, then queues without a lock, while the write
//     pump closes that queue on Stop. A server request answered as the charger
//     stops sends on a closed channel and panics (#1712).
//   - ocppj.Client.Stop installs its own disconnect handler while the write
//     pump may be reading the old one after the server closed: a data race.
//   - If the pump has already called the old handler, ocppj.Client.Stop waits
//     for a call that never comes, and the caller hangs.
//
// It serializes Write with Stop, keeps the disconnect handler itself, and
// passes a disconnect the pump has already reported to the handler Stop
// installed. A Write while the server closes the socket can still meet the
// first race; tests therefore stop their chargers before the server.
type Socket struct {
	*ws.Client

	mu       sync.Mutex
	stopping bool
	onDown   func(error)
	onUp     func()
	// installed counts disconnect handlers; reportedTo is the one that was
	// told of the current disconnect, 0 while connected.
	installed, reportedTo int
}

// New wraps client, which may carry credentials or TLS.
func New(client *ws.Client) *Socket {
	s := &Socket{Client: client}
	// Set once, before the pumps start, so the library never sees a change.
	client.SetDisconnectedHandler(s.disconnected)
	client.SetReconnectedHandler(s.reconnected)
	return s
}

// ChargePoint16 is an OCPP 1.6 charge point on a Socket. A nil client is a
// plain ws.Client.
func ChargePoint16(id string, client *ws.Client) ocpp16.ChargePoint {
	if client == nil {
		client = ws.NewClient()
	}
	return ocpp16.NewChargePoint(id, nil, New(client))
}

// ChargingStation201 is ChargePoint16 for OCPP 2.0.1.
func ChargingStation201(id string, client *ws.Client) ocpp201.ChargingStation {
	if client == nil {
		client = ws.NewClient()
	}
	return ocpp201.NewChargingStation(id, nil, New(client))
}

func (s *Socket) SetDisconnectedHandler(fn func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDown = fn
	s.installed++
}

func (s *Socket) SetReconnectedHandler(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onUp = fn
}

func (s *Socket) disconnected(err error) {
	s.mu.Lock()
	fn := s.onDown
	s.reportedTo = s.installed
	s.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

func (s *Socket) reconnected() {
	s.mu.Lock()
	fn := s.onUp
	s.reportedTo = 0
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (s *Socket) Start(url string) error {
	s.mu.Lock()
	s.stopping, s.reportedTo = false, 0
	s.mu.Unlock()
	return s.Client.Start(url)
}

func (s *Socket) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return errors.New("charger is stopping")
	}
	return s.Client.Write(data)
}

func (s *Socket) Stop() {
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
