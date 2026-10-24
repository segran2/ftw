package control

import (
	"encoding/json"
	"github.com/srcfl/ftw/go/internal/telemetry"
	"math"
	"testing"
	"time"
)

func TestBatteryReliefRedistributesOnlyAvailableSupply(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		lastA, keepA, keepB, pending, wantA, wantB float64
	}{
		{"blocked", -1000, 0, 5000, 0, 0, -1827},
		{"partial", -1000, 300, 5000, 0, 0, -1827},
		{"insufficient", -1000, 100, 700, 0, -100, -700},
		{"no_prior_supply", 0, 5000, 5000, 0, 0, -827},
		{"pending_exceeds_supply", -1000, 0, 5000, 1000, 0, -2000},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/forward", true: "/reverse"}[reverse], func(t *testing.T) {
				targets := []DispatchTarget{{Driver: "a", TargetW: 0}, {Driver: "b", TargetW: 0}}
				if reverse {
					targets[0], targets[1] = targets[1], targets[0]
				}
				out := capBatteryCharge(targets, map[string]float64{"a": tc.lastA, "b": -2000}, tc.pending, 1173, map[string]float64{"a": tc.keepA, "b": tc.keepB})
				if tc.name == "partial" {
					total := 0.0
					for _, v := range out {
						total += v.TargetW
						if v.TargetW > 0 || v.Driver == "a" && v.TargetW < -300 || v.Driver == "b" && v.TargetW < -2000 {
							t.Fatalf("relief crossed available supply: %+v", out)
						}
					}
					if math.Abs(total+1827) > 1e-6 {
						t.Fatalf("retained %.6f W, want -1827 W", total)
					}
					return
				}
				wants := map[string]float64{"a": tc.wantA, "b": tc.wantB}
				for _, v := range out {
					if math.Abs(v.TargetW-wants[v.Driver]) > 1e-6 {
						t.Fatalf("%s target %.6f, want %.6f", v.Driver, v.TargetW, wants[v.Driver])
					}
				}
			})
		}
	}
}

func TestBatteryReliefDistributionAcrossDirectionsAndDispatch(t *testing.T) {
	for _, direction := range []float64{1, -1} {
		for _, bound := range []string{"blocked", "partial"} {
			for _, layer := range []string{"allocation", "manual", "idle"} {
				for _, reverse := range []bool{false, true} {
					name := map[float64]string{1: "import", -1: "export"}[direction] + "/" + bound + "/" + layer + map[bool]string{false: "/forward", true: "/reverse"}[reverse]
					t.Run(name, func(t *testing.T) {
						now := time.Now().UTC()
						store := telemetry.NewStore()
						data, _ := json.Marshal(map[string]float64{"l1_a": 22.3 * direction, "l2_a": 12.3 * direction, "l3_a": 12.3 * direction, "l1_w": 22.3 * 230 * direction, "l2_w": 12.3 * 230 * direction, "l3_w": 12.3 * 230 * direction})
						store.Update("meter", telemetry.DerMeter, 10787*direction, nil, data)
						store.RecordDriverSuccess("meter")
						soc := .6
						low := .04
						var blocked json.RawMessage
						aSoc := &soc
						if bound == "blocked" {
							if direction > 0 {
								aSoc = &low
							} else {
								blocked = json.RawMessage(`{"charge_capable":false}`)
							}
						}
						store.Update("a", telemetry.DerBattery, -1000*direction, aSoc, blocked)
						store.RecordDriverSuccess("a")
						store.Update("b", telemetry.DerBattery, -2000*direction, &soc, nil)
						store.RecordDriverSuccess("b")
						for _, d := range []string{"meter", "a", "b"} {
							kind := telemetry.DerBattery
							if d == "meter" {
								kind = telemetry.DerMeter
							}
							store.Get(d, kind).UpdatedAt = now
						}
						st := phaseState(25, 1)
						st.clock = func() time.Time { return now }
						st.Mode = ModeIdle
						st.SlewEnabled = false
						st.MinDispatchIntervalS = 0
						limits := PowerLimits{MaxChargeW: 5000, MaxDischargeW: 5000}
						if bound == "partial" {
							if direction > 0 {
								limits.MaxDischargeW = 300
							} else {
								limits.MaxChargeW = 300
							}
						}
						st.DriverLimits = map[string]PowerLimits{"a": limits, "b": {MaxChargeW: 5000, MaxDischargeW: 5000}}
						st.batteryCommands = map[string]commandLog{"a": commandLog{}.add(now.Add(-time.Hour), -1000*direction), "b": commandLog{}.add(now.Add(-time.Hour), -2000*direction)}
						targets := []DispatchTarget{{Driver: "a", TargetW: 0}, {Driver: "b", TargetW: 0}}
						if reverse {
							targets[0], targets[1] = targets[1], targets[0]
						}
						var out []DispatchTarget
						if layer == "allocation" {
							out = fitBatteryRisesToFuse(targets, store, st)
						} else {
							if layer == "manual" {
								st.SetBatteryManualHold(BatteryManualHold{PowerW: 0, ExpiresAt: now.Add(time.Minute)})
							}
							out = ComputeDispatch(store, st, map[string]float64{"a": 10000, "b": 10000}, 17250)
						}
						if len(out) != 2 {
							t.Fatalf("missing targets %+v", out)
						}
						total := 0.0
						for _, v := range out {
							total += v.TargetW
							if v.Driver == "a" {
								cap := 0.0
								if bound == "partial" {
									cap = 300
								}
								if math.Abs(v.TargetW) > cap+1e-6 {
									t.Fatalf("blocked/bounded supply used: %+v", out)
								}
							}
							if v.TargetW*direction > 1e-6 || math.Abs(v.TargetW) > map[string]float64{"a": 1000, "b": 2000}[v.Driver]+1e-6 {
								t.Fatalf("new or reversed supply %+v", out)
							}
						}
						projected := 22.3 + (3000+direction*total)/690
						if math.Abs(projected-24) > 1e-6 {
							t.Fatalf("target %.6f projects %.6f A, want 24 A", total, projected)
						}
					})
				}
			}
		}
	}
}
