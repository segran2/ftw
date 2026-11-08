package loadpoint

import (
	"testing"
	"time"
)

// A charger reports its draw late: Easee Cloud about 10 s after the draw.
// Surplus counts each charger at what the meter may show of our orders, not
// at its late reading. A raise on its way already claims its room; a
// charger ordered off is leaving; a smaller cut keeps the reading until it
// lands.
func TestSurplusCountsOurOrdersNotALateReading(t *testing.T) {
	start := time.Date(2026, 10, 21, 22, 15, 0, 0, time.UTC)
	for _, tc := range []struct {
		name               string
		orders             []float64 // each sent 2 s apart; the last is the latest
		readings           []float64 // each read 1 s after the latest order
		wantAdj, wantRaise float64
	}{
		{"ordered off, the reading still shows the draw", []float64{6210, 0}, []float64{6210}, -6210, 0},
		{"a raise on its way", []float64{4830, 6900}, []float64{4830}, 2070, 6900},
		{"a raise the reading has shown", []float64{4830, 6900}, []float64{4830, 6900}, 0, 0},
		{"one step up from 9 A, on its way", []float64{6210, 6900}, []float64{6210}, 690, 6900},
		{"one step up from 9 A, shown", []float64{6210, 6900}, []float64{6210, 6900}, 0, 0},
		{"a raise after a cut that has not landed", []float64{8970, 4830, 6210}, []float64{8970}, -2760, 6210},
		{"a restart under the draw read before the stop", []float64{6210, 0, 4830}, []float64{6210}, -1380, 4830},
		{"a cut on its way keeps the reading", []float64{8970, 3450}, []float64{8970}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestController(t, []Config{{ID: "garage", DriverName: "easee", SurplusOnly: true, MinChargeW: 4140, MaxChargeW: 11040}}, nil, nil, &fakeSender{})
			now := start
			for _, w := range tc.orders {
				now = now.Add(2 * time.Second)
				c.manager.SetCommanded("garage", w, "pv_surplus")
				c.noteSurplusOrder("garage", now, w)
			}
			var adj, raise float64
			for _, r := range tc.readings {
				now = now.Add(time.Second)
				c.manager.Observe("garage", true, r, 0, true)
				adj, raise = c.surplusOrderAdjustW(now, "garage")
			}
			if adj != tc.wantAdj || raise != tc.wantRaise {
				t.Fatalf("adjust %.0f W, raise on its way %.0f W; want %.0f and %.0f", adj, raise, tc.wantAdj, tc.wantRaise)
			}
		})
	}
}
