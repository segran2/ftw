package ocpp

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lorenzodonini/ocpp-go/ocpp1.6/core"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/types"
	"github.com/lorenzodonini/ocpp-go/ws"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// freePort returns a port the kernel just allocated and immediately gave back.
// Cheaper than racing a hardcoded port across parallel test runs.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startServer brings up an OCPP CS on a free port and returns the port and
// the running server, which stops at cleanup. approved lists the charger ids
// treated as loadpoint-named; chargers outside it connect as pending and push
// no telemetry.
func startServer(t *testing.T, tel *telemetry.Store, approved ...string) (int, *Server) {
	t.Helper()
	srv := startOCPP(t, Config{Enabled: true, Bind: "127.0.0.1", HeartbeatIntervalS: 60, ApprovedIDs: approved}, tel, false)
	return srv.cfg.Port, srv
}

func TestStopIsConcurrentAndIdempotent(t *testing.T) {
	_, srv := startServer(t, telemetry.NewStore())

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.Stop()
		}()
	}
	wg.Wait()
	srv.Stop()
}

func TestBootAndMeterValuesPushDerEV(t *testing.T) {
	tel := telemetry.NewStore()
	port, srv := startServer(t, tel, "EH123456")

	cp, _, err := dialChargePoint16(t, wsURL(port), "EH123456", nil, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	if _, err := cp.BootNotification("EaseeHome", "Easee"); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Charging status — handler should mark connected+charging.
	if _, err := cp.StatusNotification(1, core.NoError, core.ChargePointStatusCharging); err != nil {
		t.Fatalf("status: %v", err)
	}

	// MeterValues with a 7200 W power sample.
	mv := []types.MeterValue{{
		Timestamp: types.NewDateTime(time.Now()),
		SampledValue: []types.SampledValue{
			{
				Value:     "7200",
				Measurand: types.MeasurandPowerActiveImport,
				Unit:      types.UnitOfMeasureW,
			},
		},
	}}
	if _, err := cp.MeterValues(1, mv); err != nil {
		t.Fatalf("meter values: %v", err)
	}

	// Allow the handler goroutine to flush.
	deadline := time.Now().Add(2 * time.Second)
	var r *telemetry.DerReading
	for time.Now().Before(deadline) {
		r = tel.Get("EH123456", telemetry.DerEV)
		if r != nil && r.RawW > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r == nil {
		t.Fatal("expected DerEV reading for EH123456, got nil")
	}
	if r.RawW != 7200 {
		t.Errorf("expected 7200 W, got %f", r.RawW)
	}

	view := srv.Handler().Snapshot()["EH123456"]
	if !view.Connected || !view.Charging {
		t.Errorf("expected connected+charging, got %+v", view)
	}
}

func TestStartStopTransactionTracksSession(t *testing.T) {
	tel := telemetry.NewStore()
	port, srv := startServer(t, tel, "EH-SESSION")

	// The idTag that starts a transaction is the session's vehicle identity
	// — main.go turns it into a vehicle-profile lookup, so the callback and
	// the snapshot must both carry it.
	var idMu sync.Mutex
	var identified [][3]string
	srv.Handler().SetVehicleIdentified(func(chargerID, vehicleID, source string) {
		idMu.Lock()
		identified = append(identified, [3]string{chargerID, vehicleID, source})
		idMu.Unlock()
	})

	cp, _, err := dialChargePoint16(t, wsURL(port), "EH-SESSION", nil, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if _, err := cp.BootNotification("Home", "Easee"); err != nil {
		t.Fatalf("boot: %v", err)
	}
	startConf, err := cp.StartTransaction(1, "RFID-ABCD", 1000, types.NewDateTime(time.Now()))
	if err != nil {
		t.Fatalf("start tx: %v", err)
	}
	if startConf.IdTagInfo.Status != types.AuthorizationStatusAccepted {
		t.Errorf("expected accepted, got %s", startConf.IdTagInfo.Status)
	}
	idView := srv.Handler().Snapshot()["EH-SESSION"]
	if idView.VehicleID != "RFID-ABCD" || idView.VehicleIDSource != "rfid" {
		t.Errorf("vehicle identity not captured: %+v", idView)
	}
	idMu.Lock()
	if len(identified) != 1 || identified[0] != [3]string{"EH-SESSION", "RFID-ABCD", "rfid"} {
		t.Errorf("vehicleIdentified callback: got %v", identified)
	}
	idMu.Unlock()

	if _, err := cp.StopTransaction(8500, types.NewDateTime(time.Now()), startConf.TransactionId); err != nil {
		t.Fatalf("stop tx: %v", err)
	}

	// Session energy = 8500 - 1000 = 7500 Wh
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v := srv.Handler().Snapshot()["EH-SESSION"]
		if v.SessionWh == 7500 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	view := srv.Handler().Snapshot()["EH-SESSION"]
	t.Errorf("expected session_wh=7500, got %+v", view)
}

// A charger no loadpoint names may connect and is visible in Snapshot, but
// nothing it reports may reach telemetry — otherwise any device holding the
// shared basic-auth secret could fabricate EV load and steer dispatch.
func TestPendingChargerIsQuarantinedFromTelemetry(t *testing.T) {
	tel := telemetry.NewStore()
	port, srv := startServer(t, tel, "adopted-charger")

	cp, _, err := dialChargePoint16(t, wsURL(port), "intruder", nil, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if _, err := cp.BootNotification("EvilCo", "FakeCharger"); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if _, err := cp.StatusNotification(1, core.NoError, core.ChargePointStatusCharging); err != nil {
		t.Fatalf("status: %v", err)
	}
	mv := []types.MeterValue{{
		Timestamp: types.NewDateTime(time.Now()),
		SampledValue: []types.SampledValue{{
			Value:     "7200",
			Measurand: types.MeasurandPowerActiveImport,
			Unit:      types.UnitOfMeasureW,
		}},
	}}
	if _, err := cp.MeterValues(1, mv); err != nil {
		t.Fatalf("meter values: %v", err)
	}

	// Wait until the handler has demonstrably processed the meter values —
	// the snapshot shows the power — so the nil telemetry check below is a
	// real assertion, not a race won by asserting too early.
	deadline := time.Now().Add(2 * time.Second)
	var view ChargerView
	for time.Now().Before(deadline) {
		view = srv.Handler().Snapshot()["intruder"]
		if view.PowerW == 7200 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view.PowerW != 7200 {
		t.Fatalf("snapshot never showed the meter value: %+v", view)
	}
	if !view.Pending {
		t.Errorf("expected pending=true for an unadopted charger, got %+v", view)
	}
	if r := tel.Get("intruder", telemetry.DerEV); r != nil {
		t.Errorf("pending charger leaked into telemetry: %+v", r)
	}

	if srv.Handler().isApproved("intruder") {
		t.Error("intruder must not be approved")
	}
	if !srv.Handler().isApproved("adopted-charger") {
		t.Error("adopted-charger should be approved")
	}

	// Hot adoption: loadpoints hot-reload and the config applier re-calls
	// SetApprovedIDs, so naming the id admits the very next reading — no
	// reconnect, no restart.
	srv.Handler().SetApprovedIDs([]string{"intruder"})
	if _, err := cp.MeterValues(1, mv); err != nil {
		t.Fatalf("meter values after adopt: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	var r *telemetry.DerReading
	for time.Now().Before(deadline) {
		r = tel.Get("intruder", telemetry.DerEV)
		if r != nil && r.RawW == 7200 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r == nil || r.RawW != 7200 {
		t.Fatalf("adopted charger should reach telemetry, got %+v", r)
	}

	// Revocation pushes a zero synchronously so the last power figure
	// cannot linger in the DerEV sum, and the charger is pending again.
	srv.Handler().SetApprovedIDs(nil)
	if r := tel.Get("intruder", telemetry.DerEV); r == nil || r.RawW != 0 {
		t.Fatalf("revocation should zero the reading, got %+v", r)
	}
	if v := srv.Handler().Snapshot()["intruder"]; !v.Pending {
		t.Errorf("revoked charger should be pending, got %+v", v)
	}
}

func TestBasicAuthRejectsWrongCredentials(t *testing.T) {
	srv := startOCPP(t, Config{Enabled: true, Bind: "127.0.0.1", Username: "easee", Password: "secret"}, telemetry.NewStore(), false)

	wsClient := ws.NewClient()
	wsClient.SetBasicAuth("easee", "wrong-password")
	if _, _, err := dialChargePoint16(t, wsURL(srv.cfg.Port), "EH-AUTH", wsClient, nil); err == nil {
		t.Error("expected auth failure with wrong password, got nil")
	}
}
