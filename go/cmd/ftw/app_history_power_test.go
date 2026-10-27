package main

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/state"
	"github.com/srcfl/ftw/go/internal/telemetry"
)

// The app's tiles draw what was measured. A Sungrow's energy counters move in
// 0.1 kWh steps, so a ledger bucket read 0 or 1.2 kW while the sun shone at
// 0.4 kW (Fredrik's box, 25 October 2026). Grid, PV and battery now come from
// the power rows /api/history draws; a flow that fell silent is a hole there,
// never a mean of the rows that were left.
func TestAppHistoryDrawsMeasuredPower(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnableHistoryAggregation(); err != nil {
		t.Fatal(err)
	}
	tel := telemetry.NewStore()
	tel.Update("grid", telemetry.DerMeter, 300, nil, nil)
	tel.RecordDriverSuccess("grid")
	tel.Update("sungrow", telemetry.DerPV, -400, nil, nil)
	tel.Update("sungrow", telemetry.DerBattery, 500, nil, nil)
	tel.RecordDriverSuccess("sungrow")
	const step = int64(300_000)
	base := time.Now().Add(-20 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	for i := int64(0); i < 60; i++ {
		// The inverter falls silent six minutes in.
		if i == 36 {
			tel.Get("sungrow", telemetry.DerPV).UpdatedAt = time.Now().Add(-10 * time.Minute)
		}
		p, _ := buildHistoryPoint(tel, tickPersistControl{SiteMeterDriver: "grid"}, base+i*10_000, 90*time.Second)
		p.Sources.AtMS = base + i*10_000
		if err := st.RecordTickWithEnergy(p, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FlushHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := (&appHistory{st: st}).Window(context.Background(), []string{"pv_w", "grid_w", "battery_w"}, step, base, base+2*step)
	if err != nil {
		t.Fatal(err)
	}
	pv := map[int64]float64{}
	for _, s := range got["pv_w"] {
		pv[s.StartMs] = s.W
	}
	grid := map[int64]float64{}
	for _, s := range got["grid_w"] {
		grid[s.StartMs] = s.W
	}
	if w, ok := pv[base]; !ok || math.Abs(w+400) > 0.5 {
		t.Fatalf("first five minutes: pv %v (present %v), want -400 W measured", w, ok)
	}
	if w, ok := pv[base+step]; ok {
		t.Fatalf("a silent inverter's five minutes read %v W, want a hole", w)
	}
	for _, at := range []int64{base, base + step} {
		if w, ok := grid[at]; !ok || math.Abs(w-300) > 0.5 {
			t.Fatalf("grid at %d: %v (present %v), want 300 W", at, w, ok)
		}
	}
	// Charging is power into the site: positive, as the ledger had it.
	battery := map[int64]float64{}
	for _, s := range got["battery_w"] {
		battery[s.StartMs] = s.W
	}
	if w, ok := battery[base]; !ok || math.Abs(w-500) > 0.5 {
		t.Fatalf("battery: %v (present %v), want +500 W charging in the first five minutes", w, ok)
	}
}

// One stale reading in five minutes is not a hole: the mean is over the
// readings that measured the sun, as long as they cover 90 % of them.
func TestAppHistoryKeepsAFiveMinutesWithOneMissedReading(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnableHistoryAggregation(); err != nil {
		t.Fatal(err)
	}
	tel := telemetry.NewStore()
	tel.Update("grid", telemetry.DerMeter, 300, nil, nil)
	tel.RecordDriverSuccess("grid")
	tel.Update("sungrow", telemetry.DerPV, -400, nil, nil)
	tel.RecordDriverSuccess("sungrow")
	const step = int64(300_000)
	base := time.Now().Add(-20 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	for i := int64(0); i < 30; i++ {
		pv := tel.Get("sungrow", telemetry.DerPV)
		pv.UpdatedAt = time.Now()
		if i == 12 {
			pv.UpdatedAt = time.Now().Add(-10 * time.Minute)
		}
		p, _ := buildHistoryPoint(tel, tickPersistControl{SiteMeterDriver: "grid"}, base+i*10_000, 90*time.Second)
		p.Sources.AtMS = base + i*10_000
		if err := st.RecordTickWithEnergy(p, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FlushHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := (&appHistory{st: st}).Window(context.Background(), []string{"pv_w"}, step, base, base+step)
	if err != nil {
		t.Fatal(err)
	}
	if pv := got["pv_w"]; len(pv) != 1 || math.Abs(pv[0].W+400) > 0.5 {
		t.Fatalf("pv: %+v, want -400 W over the readings that measured it", pv)
	}
}
