package loadpoint

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestPlanningRequiresConfirmedStartingLevel(t *testing.T) {
	for _, source := range []string{"assumed", "completed"} {
		if SoCConfirmedForPlan(State{CurrentSoC: .2, SoCSource: source}) {
			t.Fatal(source)
		}
	}
	for _, soc := range []float64{math.NaN(), math.Inf(1), -.01, 1.01} {
		if SoCConfirmedForPlan(State{CurrentSoC: soc}) {
			t.Fatal(soc)
		}
	}
	for _, source := range []string{"", "inferred", "vehicle"} {
		if !SoCConfirmedForPlan(State{CurrentSoC: .86, SoCSource: source}) {
			t.Fatal(source)
		}
	}
}
func TestUnconfirmedSoCRejectsCachedScheduleUntilCorrection(t *testing.T) {
	now := time.Now()
	cfg := Config{ID: "easee", DriverName: "easee-cloud", VehicleCapacityWh: 86500, MinChargeW: 4140, MaxChargeW: 11000}
	sender := &fakeSender{}
	dir := &Directive{SlotStart: now, SlotEnd: now.Add(time.Hour), LoadpointEnergyWh: map[string]float64{"easee": 11000}}
	c := newTestController(t, []Config{cfg}, dir, map[string]EVSample{cfg.DriverName: {Connected: true, RequestActive: true}}, sender)
	c.manager.SetSchedule(cfg.ID, Schedule{SoC: .9, TimeOfDayMinUTC: 420, Recurring: true})
	c.Tick(context.Background(), now)
	last, ok := lastSetCurrent(sender.calls)
	if !ok || last.power != 0 {
		t.Fatalf("unconfirmed schedule dispatched: %+v", last)
	}
	st, _ := c.manager.State(cfg.ID)
	if st.CommandedReason != "soc_confirmation_required" {
		t.Fatal(st.CommandedReason)
	}
	if !c.manager.SetCurrentSoC(cfg.ID, .86) {
		t.Fatal("correction rejected")
	}
	c.Tick(context.Background(), now.Add(time.Second))
	last, _ = lastSetCurrent(sender.calls)
	if last.power <= 0 {
		t.Fatalf("confirmed schedule did not resume: %+v", last)
	}
	c.manager.Observe(cfg.ID, false, 0, 0, true)
	c.Tick(context.Background(), now.Add(2*time.Second))
	last, _ = lastSetCurrent(sender.calls)
	if last.power != 0 {
		t.Fatal("new unconfirmed session reused confirmed plan")
	}
}
