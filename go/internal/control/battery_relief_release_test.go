package control

import (
	"math"
	"testing"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// batteryReading gives a battery the SoC its command bounds read.
func batteryReading(store *telemetry.Store, driver string, w, soc float64) {
	store.Update(driver, telemetry.DerBattery, w, &soc, nil)
	store.RecordDriverSuccess(driver)
}

// Field matrix, relief rows (srcfl/ftw#1763's site, 25 A): a kettle on L1,
// the battery relieving at -3,105 W and L1 reading 19.53 A. Ending that
// relief is our own raise of 4.5 A on every phase; with the car's unseen
// 1 A rise it took L1 to 24.03 A, over the 24 A working limit, for more
// than 3 s. The battery keeps the part of its relief the phase cannot take.
func TestEVBatteryEndingReliefKeepsWhatThePhaseCannotTake(t *testing.T) {
	for _, tc := range []struct {
		name          string
		l1A, carRiseA float64
		soc           float64
		want          float64
	}{
		{"the phase takes 3.47 A of the 4.5 A", 19.53, 1, .5, -(3105 - (24-19.53-1)*690)},
		{"the phase takes it all and some charge", 15, 1, .5, (24-15-1)*690 - 3105},
		{"a battery at its floor cannot keep it", 19.53, 1, 0.01, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phases := []float64{tc.l1A, tc.l1A - 10, tc.l1A - 10}
			store, state := evSplitFixture(t, (phases[0]+phases[1]+phases[2])*230, phases, 9660+tc.carRiseA*690, "3p")
			sent(state, "ev", 9660, 9660+tc.carRiseA*690)
			sent(state, "first", -3105)
			batteryReading(store, "first", -3105, tc.soc)
			out := clampEVBatteryHeadroom([]DispatchTarget{{Driver: "first", TargetW: 3430}}, store, state, 17250)
			if got := targetW(out, "first"); math.Abs(got-tc.want) > .01 {
				t.Fatalf("battery %.1f W, want %.1f W", got, tc.want)
			}
		})
	}
}

// Before our first battery order, after every Core start, the charge cap
// counts the battery as possibly discharging at its cap. That is no order
// of ours: it must not become a discharge to keep. The battery keeps its
// target or less, never a discharge the plan did not ask for.
func TestEVBatteryWithNoOrderOfOursKeepsNoDischarge(t *testing.T) {
	for _, planW := range []float64{0, 6000} {
		phases := []float64{20.7, 10.7, 10.7}
		store, state := evSplitFixture(t, (phases[0]+phases[1]+phases[2])*230, phases, 4140, "3p")
		batteryReading(store, "first", 0, .5)
		out := clampEVBatteryHeadroom([]DispatchTarget{{Driver: "first", TargetW: planW}}, store, state, 17250)
		if got := targetW(out, "first"); got < 0 {
			t.Fatalf("plan %.0f W with no order logged: battery %.0f W, want no discharge", planW, got)
		}
	}
}
