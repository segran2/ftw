package control

import (
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/loadpoint"
	"github.com/srcfl/ftw/go/internal/sitesim"
)

// A duty quarter keeps the car on for the plan's on-time after its energy is
// spent. Core's import ceiling still cuts it: the on-time only shapes what
// the loadpoint asks for, and the EV hard limit's cap applies after it. Here
// the mains runs at 237 V, so the car draws 10665 W of the plan's 10350 W
// and spends the 02:45 quarter's energy at 02:59:33. At 02:59:40 the hour's
// ceiling leaves 5 kW for the rest of the hour, under the house and the car.
func TestSiteSimDutyOnTimeYieldsToTheImportCeiling(t *testing.T) {
	start := time.Date(2026, 10, 26, 2, 30, 0, 0, time.Local)
	p := simPlant(start)
	p.Voltage, p.HouseW, p.EVSE.ReadbackLag = 237, houseA(1.5, 1.5, 1.5), 0
	quarter := start.Add(15 * time.Minute)
	s := &simSite{p: p, lp: simLoadpoint(), plan: func(now time.Time) (loadpoint.Directive, bool) {
		slot := now.Truncate(15 * time.Minute)
		if now.Before(start) || !now.Before(quarter.Add(15*time.Minute)) {
			return loadpoint.Directive{}, false
		}
		return loadpoint.Directive{SlotStart: slot, SlotEnd: slot.Add(15 * time.Minute),
			LoadpointEnergyWh: map[string]float64{"garage": 10350.0 / 4}, LoadpointMaxPowerW: map[string]float64{"garage": 10350}}, true
	}}
	s.setup = func(s *simSite) {
		s.state.SiteFuseVoltage = 230
		s.ctrl.SetSiteFuse(loadpoint.SiteFuse{MaxAmps: p.Fuse.RatingA, Voltage: 230, PhaseCnt: 3})
		s.mgr.SetSchedule(s.lp.ID, loadpoint.Schedule{SoC: .8})
	}
	s.boot()
	s.run(quarter.Sub(p.Now))
	quarterWh := p.EVSE.SessionWh
	s.run(quarter.Add(14*time.Minute + 39*time.Second).Sub(p.Now))
	if s.failure != "" {
		t.Fatal(s.failure)
	}
	if spentWh := p.EVSE.SessionWh - quarterWh; s.offerW != 10350 || spentWh < 10350.0/4 {
		t.Fatalf("setup at 02:59:39: offer %.0f W, %.0f of %.0f Wh in the quarter; want the on-time running past the spent energy", s.offerW, spentWh, 10350.0/4)
	}
	s.state.PeakImportCeilingW = 5000
	cutAt := p.Now
	for s.offerW != 0 && p.Now.Sub(cutAt) < 5*time.Second {
		s.step()
	}
	// The cut is the ceiling's; after it the spent energy keeps the car off.
	if st, _ := s.mgr.State(s.lp.ID); s.offerW != 0 || st.CommandedReason != "import_limit" {
		t.Fatalf("%v after the ceiling: offer %.0f W (%s), want 0 W on import_limit", p.Now.Sub(cutAt), s.offerW, st.CommandedReason)
	}
	s.run(quarter.Add(15*time.Minute - sitesim.Step).Sub(p.Now))
	for _, c := range s.commands {
		if c.device == "evse" && c.at.After(cutAt) && c.w > 0 {
			t.Fatalf("ordered the car %.0f W at %s, after the ceiling cut it", c.w, c.at.Format("15:04:05"))
		}
	}
	if p.EVSE.DrawW != 0 {
		t.Fatalf("the car still draws %.0f W at the quarter's end", p.EVSE.DrawW)
	}
}
