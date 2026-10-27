package loadpoint

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestSmallEnergyBudgetStopsOnceAndStaysStopped(t *testing.T) {
	for _, replan := range []bool{false, true} {
		t.Run(map[bool]string{false: "one plan", true: "replan while charging"}[replan], func(t *testing.T) {
			start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
			sender := &fakeSender{}
			cfgs := []Config{{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000}}
			dir := &Directive{SlotStart: start, SlotEnd: start.Add(15 * time.Minute), LoadpointEnergyWh: map[string]float64{"garage": 100}}
			samples := map[string]EVSample{"easee": {Connected: true, RequestActive: true, SessionWh: 1000}}
			c := newTestController(t, cfgs, dir, samples, sender)
			var delivered, previousPower float64
			stopped := false
			for seconds := 0; seconds < 900; seconds += 5 {
				delivered += previousPower * 5 / 3600
				sample := samples["easee"]
				sample.PowerW = previousPower
				sample.SessionWh = 1000 + delivered
				samples["easee"] = sample
				now := start.Add(time.Duration(seconds) * time.Second)
				if replan && seconds == 45 {
					dir.SlotStart = now
					dir.LoadpointEnergyWh["garage"] = 100 - delivered
				}
				c.Tick(context.Background(), now)
				command, ok := lastSetCurrent(sender.calls)
				if !ok {
					t.Fatal("missing current command")
				}
				if stopped && command.power > 0 {
					t.Fatalf("spent energy disappeared at %ds: %.2f Wh, command %.0f W", seconds, delivered, command.power)
				}
				if command.power == 0 && delivered >= 100 {
					stopped = true
				}
				previousPower = command.power
			}
			if !stopped || delivered < 100 || delivered > 100+4140*5.0/3600+1e-6 {
				t.Fatalf("budget not met within one control tick: %.4f Wh", delivered)
			}
		})
	}
}

func TestEnergyBudgetUsesActualCounterAcrossPowerChanges(t *testing.T) {
	start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	cfg := Config{ID: "garage", DriverName: "easee"}
	c := NewController(NewManager(), nil, nil, nil)
	for _, s := range []struct {
		seconds        int
		power, counter float64
	}{{0, 11000, 1000}, {5, 4140, 1015}, {10, 0, 1022}, {15, 0, 1022}} {
		c.observeEnergy(cfg, EVSample{Connected: true, PowerW: s.power, SessionWh: s.counter}, start.Add(time.Duration(s.seconds)*time.Second))
	}
	wh, unknown := c.energySince("garage", start, start.Add(15*time.Second))
	if math.Abs(wh-22) > 1e-9 || unknown != 0 {
		t.Fatalf("got %.2f Wh, %.2f unknown seconds", wh, unknown)
	}
}

func TestEnergyBudgetDoesNotWaitForQuantizedSessionCounter(t *testing.T) {
	start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	cfg := Config{ID: "garage", DriverName: "easee"}
	c := NewController(NewManager(), nil, nil, nil)
	for seconds := 0; seconds <= 90; seconds += 5 {
		c.observeEnergy(cfg, EVSample{Connected: true, PowerW: 4140, SessionWh: 1000}, start.Add(time.Duration(seconds)*time.Second))
	}
	wh, _ := c.energySince("garage", start, start.Add(90*time.Second))
	if math.Abs(wh-103.5) > 1e-9 {
		t.Fatalf("counter hid measured charging: %.4f Wh", wh)
	}
}

func TestDutyPlanCommandsOnPowerThenStopsAtEnergyBudget(t *testing.T) {
	start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000}
	dir := &Directive{SlotStart: start, SlotEnd: start.Add(15 * time.Minute), LoadpointEnergyWh: map[string]float64{"garage": 100}, LoadpointMaxPowerW: map[string]float64{"garage": 11000}}
	c := newTestController(t, []Config{cfg}, dir, map[string]EVSample{}, &fakeSender{})
	c.observeEnergy(cfg, EVSample{Connected: true, SessionWh: 1000}, start)
	got, _, _ := c.computeCommand(start, cfg, 0)
	if got != 11000 {
		t.Fatalf("command %.0f, want legal on-power 11000", got)
	}
	later := start.Add(35 * time.Second)
	c.observeEnergy(cfg, EVSample{Connected: true, SessionWh: 1100}, later)
	got, _, _ = c.computeCommand(later, cfg, 0)
	if got != 0 {
		t.Fatalf("spent budget still commands %.0f", got)
	}
}

// A duty slot runs its on-power for the plan's on-time, its energy at that
// power. The home box on 25 Oct 2026: the plan gave the car 10350 W for the
// whole quarter, the mains ran at 230.8 V and the car drew 10386 W, so the
// energy ran out 3 s before the quarter ended. The stop reached the charger
// in the next quarter, which wanted the car too.
func TestDutySlotRunsItsOnTime(t *testing.T) {
	for _, tc := range []struct {
		name           string
		from           int     // seconds into the quarter the plan's interval starts, as after a replan
		onS            float64 // the plan's on-time at 10350 W from then
		draw           float64 // what the car draws per watt ordered
		capFrom, capTo int     // the fuse cap is capW in [capFrom, capTo)
		capW           float64
		stopAt         int     // the tick the order goes to 0 W; -1: never
		finalW         float64 // the order at the quarter's last tick, when it never stops
	}{
		{name: "a full quarter at 230.8 V (the home box)", onS: 900, draw: 230.8 / 230, stopAt: -1, finalW: 10350},
		{name: "a full quarter at 239 V", onS: 900, draw: 239.0 / 230, stopAt: -1, finalW: 10350},
		{name: "replanned at 5 minutes, 239 V", from: 300, onS: 600, draw: 239.0 / 230, stopAt: -1, finalW: 10350},
		{name: "nine tenths, the car draws 4 % more", onS: 810, draw: 1.04, stopAt: 810},
		{name: "nine tenths, the car draws 5 % less", onS: 810, draw: .95, stopAt: 855},
		{name: "an off-time of 15 s", onS: 885, draw: 1, stopAt: -1, finalW: 10350},
		{name: "an off-time of 20 s", onS: 880, draw: 1, stopAt: 882},
		{name: "an off-time of 25 s, the car draws 2 % more", onS: 875, draw: 1.02, stopAt: 876},
		{name: "no energy", onS: 0, draw: 1, stopAt: 0},
		{name: "a fuse cap of 5000 W in the last minute", onS: 900, draw: 230.8 / 230, capFrom: 840, capTo: 900, capW: 5000, stopAt: -1, finalW: 4140},
		{name: "a fuse stop once the energy is spent", onS: 810, draw: 1.04, capFrom: 783, capTo: 795, stopAt: 783},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quarter := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)
			cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11040, AllowedStepsW: []float64{0, 4140, 8970, 10350}}
			budgetWh := 10350 * tc.onS / 3600
			dir := &Directive{SlotStart: quarter.Add(time.Duration(tc.from) * time.Second), SlotEnd: quarter.Add(15 * time.Minute),
				LoadpointEnergyWh: map[string]float64{"garage": budgetWh}, LoadpointMaxPowerW: map[string]float64{"garage": 10350}}
			samples := map[string]EVSample{"easee": {Connected: true, RequestActive: true, SessionWh: 1000}}
			sender := &fakeSender{}
			c := newTestController(t, []Config{cfg}, dir, samples, sender)
			var s int
			c.SetFuseEVMax(func() (float64, bool) {
				if s >= tc.capFrom && s < tc.capTo {
					return tc.capW, true
				}
				return 1e9, true
			})
			var delivered, drawW, cmdW float64
			stoppedAt := -1
			for s = tc.from; s < 900; s += 3 {
				now := quarter.Add(time.Duration(s) * time.Second)
				delivered += drawW * 3 / 3600
				sample := samples["easee"]
				sample.PowerW, sample.PowerAt, sample.SessionWh, sample.EnergyAt = drawW, now, 1000+delivered, now
				samples["easee"] = sample
				c.Tick(context.Background(), now)
				cmd, ok := lastSetCurrent(sender.calls)
				if !ok {
					t.Fatal("missing current command")
				}
				if stoppedAt >= 0 && cmd.power > 0 {
					t.Fatalf("started again at %ds after stopping at %ds", s, stoppedAt)
				}
				if stoppedAt < 0 && cmd.power == 0 {
					stoppedAt = s
				}
				cmdW, drawW = cmd.power, cmd.power*tc.draw
			}
			if stoppedAt != tc.stopAt || tc.stopAt < 0 && cmdW != tc.finalW {
				t.Fatalf("stopped at %ds (want %d), last order %.0f W (want %.0f); %.1f of %.1f Wh delivered", stoppedAt, tc.stopAt, cmdW, tc.finalW, delivered, budgetWh)
			}
		})
	}
}

func TestDutyDispatchStopsForUnavailablePowerAndKeepsSpentBudget(t *testing.T) {
	start := time.Now().Truncate(time.Minute)
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000}
	dir := &Directive{SlotStart: start, SlotEnd: start.Add(15 * time.Minute), LoadpointEnergyWh: map[string]float64{"garage": 100}, LoadpointMaxPowerW: map[string]float64{"garage": 11000}}
	samples := map[string]EVSample{"easee": {Connected: true, RequestActive: true, SessionWh: 1000, EnergyAt: start, PowerAt: start}}
	sender := &fakeSender{}
	c := newTestController(t, []Config{cfg}, dir, samples, sender)
	c.Tick(context.Background(), start)
	// A fresh counter proves that the whole pulse has been delivered.
	s := samples["easee"]
	s.SessionWh, s.EnergyAt, s.PowerAt = 1100, start.Add(35*time.Second), start.Add(35*time.Second)
	samples["easee"] = s
	c.Tick(context.Background(), s.PowerAt)
	for sec := 40; sec <= 600; sec += 5 {
		s.PowerUnavailable = true
		samples["easee"] = s
		c.Tick(context.Background(), start.Add(time.Duration(sec)*time.Second))
		cmd, ok := lastSetCurrent(sender.calls)
		if !ok || cmd.power != 0 {
			t.Fatalf("missing power resumed a spent pulse at %ds: %+v", sec, cmd)
		}
	}
	s.PowerUnavailable, s.PowerAt = false, start.Add(605*time.Second)
	samples["easee"] = s
	c.Tick(context.Background(), s.PowerAt)
	if cmd, _ := lastSetCurrent(sender.calls); cmd.power != 0 {
		t.Fatalf("recovery forgot the spent budget: %+v", cmd)
	}
}

// An old power reading from a live driver no longer pauses charging while the
// site meter is fresh; the fuse clamps work from the site meter. The manual
// request stays in force throughout.
func TestUnavailablePowerKeepsManualCharge(t *testing.T) {
	start := time.Now().Truncate(time.Minute)
	cfg := Config{ID: "garage", DriverName: "easee", MinChargeW: 4140, MaxChargeW: 11000}
	samples := map[string]EVSample{"easee": {Connected: true, RequestActive: true, DeviceID: "easee:A", SessionID: "session-1", SessionWh: 1000}}
	sender := &fakeSender{}
	c := newTestController(t, []Config{cfg}, nil, samples, sender)
	c.Tick(context.Background(), start)
	c.SetManualHold(cfg.ID, ManualHold{PowerW: 11000, Persistent: true})
	s := samples["easee"]
	s.PowerUnavailable = true
	samples["easee"] = s
	c.Tick(context.Background(), start.Add(5*time.Minute))
	if cmd, _ := lastSetCurrent(sender.calls); cmd.power <= 0 {
		t.Fatalf("an old power reading paused the manual charge: %+v", cmd)
	}
	if _, held := c.GetManualHold(cfg.ID, start.Add(5*time.Minute)); !held {
		t.Fatal("stale measurements erased the manual request")
	}
	s.PowerUnavailable = false
	samples["easee"] = s
	c.Tick(context.Background(), start.Add(5*time.Minute+5*time.Second))
	if cmd, _ := lastSetCurrent(sender.calls); cmd.power <= 0 {
		t.Fatalf("recovered measurements did not resume the request: %+v", cmd)
	}
}
