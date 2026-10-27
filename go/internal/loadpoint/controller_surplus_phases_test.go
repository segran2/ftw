package loadpoint

import (
	"context"
	"testing"
	"time"
)

// surplusRun ticks a surplus-only loadpoint every 5 s against a fixed live
// surplus and a plan that shows no PV, as the site rig's planner does, and
// returns every current order sent.
func surplusRun(t *testing.T, cfg Config, surplusW float64, d time.Duration) []sentCommand {
	t.Helper()
	return surplusRunAt(t, cfg, func(time.Duration) float64 { return surplusW }, d)
}

// surplusRunAt is surplusRun with the surplus a function of the time since
// the plug-in.
func surplusRunAt(t *testing.T, cfg Config, surplusAt func(time.Duration) float64, d time.Duration) []sentCommand {
	t.Helper()
	cfg.ID, cfg.DriverName, cfg.SurplusOnly = "garage", "evse", true
	samples := map[string]EVSample{cfg.DriverName: {Connected: true, RequestActive: true}}
	sender := &fakeSender{}
	c := newTestController(t, []Config{cfg}, nil, samples, sender)
	c.SetSiteFuse(SiteFuse{MaxAmps: 25, Voltage: 230, PhaseCnt: 3})
	start := time.Date(2026, 10, 25, 11, 0, 0, 0, time.Local)
	now := start
	c.SetSiteSurplusForEV(func() (float64, bool) { return surplusAt(now.Sub(start)), true })
	c.SetNearTermPeakSurplusW(func(time.Duration) (float64, bool) { return 0, true })
	c.SetPeakRemainingSurplusW(func() (float64, bool) { return 0, true })
	var orders []sentCommand
	for ; now.Sub(start) < d; now = now.Add(5 * time.Second) {
		before := len(sender.calls)
		c.Tick(context.Background(), now)
		for _, cmd := range sender.calls[before:] {
			if cmd.action == "ev_set_current" {
				orders = append(orders, cmd)
			}
		}
		last, _ := lastSetCurrent(sender.calls)
		samples[cfg.DriverName] = EVSample{Connected: true, RequestActive: true, PowerW: last.power}
	}
	if len(orders) == 0 {
		t.Fatal("no current order sent")
	}
	return orders
}

// Site rig s5s: auto with Easee's steps stayed on one phase at 9.3 kW of
// surplus. The live surplus picked three phases at the first tick; the
// next tick the plan, which showed no PV, set the day's one-phase lock.
// A forecast window starts now, so it cannot be below the surplus measured
// now: the charge stays on three phases, past the 30 min hold too.
func TestSurplusAboveTheThreePhaseMinimumRunsThreePhases(t *testing.T) {
	cfg := Config{MinChargeW: 4140, MaxChargeW: 11040, PhaseMode: "auto", AllowedStepsW: threePhaseSteps}
	orders := surplusRun(t, cfg, 9310, phaseSwitchMinHold+15*time.Minute)
	for i, cmd := range orders {
		if a := commandAmps(cmd); cmd.phaseMode != "3p" || a < 6 || a > 16 || cmd.power > 9310 {
			t.Fatalf("order %d of %d: %+v (%.1f A); want three phases at 6-16 A within the 9,310 W surplus", i, len(orders), cmd, a)
		}
	}
}

// Site rig s5t: 3.8 kW of surplus is under the three-phase minimum of
// 4,140 W, so the charge runs on one phase. Easee's table has 3,680 W, 16 A
// on one phase; counting it as a three-phase step sent 4,140 W on three
// phases at the first tick, which imports.
func TestSurplusBelowTheThreePhaseMinimumRunsOnePhase(t *testing.T) {
	cfg := Config{MinChargeW: 4140, MaxChargeW: 11040, PhaseMode: "auto", AllowedStepsW: threePhaseSteps}
	for i, cmd := range surplusRun(t, cfg, 3810, 2*time.Minute) {
		if cmd.power > 0 && (cmd.phaseMode != "1p" || cmd.power > 3810) {
			t.Fatalf("order %d: %+v; want one phase within the surplus, under the three-phase minimum", i, cmd)
		}
	}
}

// When the hold ends, one high reading must not switch to three phases:
// the reading counts only up to the average the pause gate keeps. Three
// phases at 1.5 kW of surplus pause the car for the next 30 min.
func TestSurplusOneHighReadingKeepsOnePhase(t *testing.T) {
	cfg := Config{MinChargeW: 1380, MaxChargeW: 11040, PhaseMode: "auto", AllowedStepsW: threePhaseSteps}
	surplus := func(at time.Duration) float64 {
		if at == phaseSwitchMinHold {
			return 4300
		}
		return 1500
	}
	for i, cmd := range surplusRunAt(t, cfg, surplus, phaseSwitchMinHold+5*time.Minute) {
		if cmd.phaseMode != "1p" || cmd.power == 0 {
			t.Fatalf("order %d: %+v; want one phase charging through one high reading", i, cmd)
		}
	}
}

// One phase yields to measured surplus above the three-phase minimum once
// the 30 min hold has passed, and three phases then hold for 30 min too.
// With a near-term forecast the dwell holds the choice; without one the
// day's one-phase lock does.
func TestSurplusOnePhaseYieldsAfterTheHold(t *testing.T) {
	for _, nearTerm := range []bool{true, false} {
		name := "day_lock"
		if nearTerm {
			name = "near_term_dwell"
		}
		t.Run(name, func(t *testing.T) {
			c := NewController(NewManager(), nil, nil, nil)
			cfg := Config{ID: "garage", SurplusOnly: true, MinChargeW: 1380, MaxChargeW: 11040,
				PhaseMode: "auto", AllowedStepsW: threePhaseSteps}
			live := 2000.0
			c.SetSiteSurplusForEV(func() (float64, bool) { return live, true })
			if nearTerm {
				c.SetNearTermPeakSurplusW(func(time.Duration) (float64, bool) { return 0, true })
			}
			c.SetPeakRemainingSurplusW(func() (float64, bool) { return 0, true })
			start := time.Date(2026, 10, 25, 11, 0, 0, 0, time.Local)
			onePhase := func(at time.Duration) bool {
				return smallestNonZero(c.pickSurplusSteps(start.Add(at), cfg)) < 4140
			}

			if !onePhase(0) || c.surplusLockedTo1P(cfg.ID) == nearTerm {
				t.Fatal("2 kW of surplus and no PV in the plan: want one phase")
			}
			live = 9000
			if !onePhase(10 * time.Minute) {
				t.Fatal("one phase must hold for 30 min")
			}
			if onePhase(phaseSwitchMinHold+time.Minute) || c.surplusLockedTo1P(cfg.ID) || c.dwellSelectedPhaseMode(cfg.ID) != "3p" {
				t.Fatal("9 kW of surplus after the hold: want three phases")
			}
			live = 2000
			if onePhase(phaseSwitchMinHold + 2*time.Minute) {
				t.Fatal("three phases must hold for 30 min")
			}
			if !onePhase(2*phaseSwitchMinHold + 2*time.Minute) {
				t.Fatal("2 kW of surplus after the second hold: want one phase again")
			}
		})
	}
}
