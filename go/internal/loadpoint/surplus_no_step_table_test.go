package loadpoint

import (
	"context"
	"testing"
	"time"
)

// A charger with no step table, as the settings page writes a new one,
// takes any power from its minimum to its maximum. Surplus used to snap
// every order to its only step, 0 W, so the car never charged from
// surplus. It orders the surplus and still pauses below the minimum.
func TestSurplusWithoutAStepTableOrdersTheSurplus(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000, PhaseMode: "3p", SurplusOnly: true}
	for _, tc := range []struct {
		surplusW, want float64
	}{{6000, 6000}, {2000, 0}} {
		c := NewController(NewManager(), nil, nil, nil)
		c.SetSiteSurplusForEV(func() (float64, bool) { return tc.surplusW, true })
		if got := c.computeSurplusCmd(now, cfg, cfg.MaxChargeW, nil); got != tc.want {
			t.Errorf("%.0f W of surplus: order %.0f W, want %.0f W", tc.surplusW, got, tc.want)
		}
	}
}

// With phase_mode unset or auto and no step table, as on the home box,
// surplus has no one-phase ladder to lock to. A day forecast under the
// three-phase minimum must not lock the charger to one phase, where an
// order is capped at 3,680 W, under its 4,140 W minimum.
func TestSurplusWithoutAStepTableNeverLocksToOnePhase(t *testing.T) {
	for _, mode := range []string{"", "auto"} {
		t.Run("phase_mode "+mode, func(t *testing.T) {
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
			cfg := Config{ID: "garage", DriverName: "evse", MinChargeW: 4140, MaxChargeW: 11000, PhaseMode: mode, SurplusOnly: true}
			samples := map[string]EVSample{cfg.DriverName: {Connected: true, RequestActive: true}}
			sender := &fakeSender{}
			c := newTestController(t, []Config{cfg}, nil, samples, sender)
			c.SetSiteFuse(SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
			c.SetSiteSurplusForEV(func() (float64, bool) { return 9310, true })
			c.SetNearTermPeakSurplusW(func(time.Duration) (float64, bool) { return 0, true })
			c.SetPeakRemainingSurplusW(func() (float64, bool) { return 0, true })
			for tick := 0; tick < 10; tick++ {
				c.Tick(context.Background(), now)
				cmd, _ := lastSetCurrent(sender.calls)
				samples[cfg.DriverName] = EVSample{Connected: true, RequestActive: true, PowerW: cmd.power}
				now = now.Add(5 * time.Second)
			}
			for _, cmd := range sender.calls {
				if cmd.action == "ev_set_current" && (cmd.phaseMode == "1p" || cmd.power > 0 && cmd.power < cfg.MinChargeW) {
					t.Fatalf("offer %+v: want no one-phase order and none under 4140 W", cmd)
				}
			}
			if last, _ := lastSetCurrent(sender.calls); last.power < 9000 {
				t.Fatalf("last offer %.0f W of 9310 W surplus", last.power)
			}
		})
	}
}

// An owner who removes the step table on a day the charger was locked to
// one phase, or held on one, leaves no lock behind: the charger holds
// three phases from the next tick, without a restart.
func TestRemovingTheStepTableClearsTheOnePhaseLock(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	cfg := Config{ID: "garage", DriverName: "evse", MinChargeW: 4140, MaxChargeW: 11000, PhaseMode: "auto", SurplusOnly: true}
	c := NewController(NewManager(), nil, nil, nil)
	c.phaseLocked1P = map[string]bool{cfg.ID: true}
	c.phaseLockedAt = map[string]time.Time{cfg.ID: now.Add(-time.Hour)}
	c.phaseSelected3P = map[string]bool{cfg.ID: false}
	c.phaseSelectedAt = map[string]time.Time{cfg.ID: now.Add(-time.Hour)}
	if steps := c.pickSurplusSteps(now, cfg); steps != nil {
		t.Fatalf("steps %v, want none without a table", steps)
	}
	if c.surplusLockedTo1P(cfg.ID) || c.dwellSelectedPhaseMode(cfg.ID) != "3p" {
		t.Fatalf("lock %v, phase held %q after the table went; want no lock and 3p", c.surplusLockedTo1P(cfg.ID), c.dwellSelectedPhaseMode(cfg.ID))
	}
}

// Through a pause and back, a no-table charger on auto keeps three phases:
// an "auto" order at 0 W lets the charger flip to one phase and cycle its
// contactor at every cloud.
func TestSurplusWithoutAStepTableHoldsThreePhasesThroughAPause(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	cfg := Config{ID: "garage", DriverName: "evse", MinChargeW: 4140, MaxChargeW: 11000, PhaseMode: "auto", SurplusOnly: true}
	samples := map[string]EVSample{cfg.DriverName: {Connected: true, RequestActive: true}}
	sender := &fakeSender{}
	c := newTestController(t, []Config{cfg}, nil, samples, sender)
	c.SetSiteFuse(SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
	surplusW := 9310.0
	c.SetSiteSurplusForEV(func() (float64, bool) { return surplusW, true })
	c.SetNearTermPeakSurplusW(func(time.Duration) (float64, bool) { return 0, true })
	c.SetPeakRemainingSurplusW(func() (float64, bool) { return 0, true })
	for tick := 0; tick < 60; tick++ {
		switch tick {
		case 10:
			surplusW = 1000
		case 30:
			surplusW = 9310
		}
		c.Tick(context.Background(), now)
		cmd, _ := lastSetCurrent(sender.calls)
		samples[cfg.DriverName] = EVSample{Connected: true, RequestActive: true, PowerW: cmd.power}
		now = now.Add(5 * time.Second)
	}
	paused := false
	for _, cmd := range sender.calls {
		if cmd.action != "ev_set_current" {
			continue
		}
		paused = paused || cmd.power == 0
		if cmd.phaseMode != "3p" {
			t.Fatalf("offer %+v: want three phases through the pause", cmd)
		}
	}
	if !paused {
		t.Fatal("surplus never paused at 1000 W")
	}
}
