package state

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// The app's tiles ask for fixed five-minute buckets laid from the tile's
// start. A row on a boundary belongs to the bucket it starts, and a bucket
// with no row is absent rather than zero.
func TestLoadPowerBucketsLaysFixedBuckets(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-time.Hour).Truncate(5 * time.Minute).UnixMilli()
	const step = int64(300_000)
	for _, r := range []struct {
		at       int64
		pv, grid float64
	}{
		{base + 10_000, -400, 300},
		{base + 200_000, -600, 100},
		{base + step, -2000, -500}, // on the boundary: the second bucket's
		// nothing in the third bucket
		{base + 3*step + 1_000, -100, 700},
		{base + 4*step, -9000, 9000}, // at `to`: outside the range
	} {
		if err := s.RecordHistory(HistoryPoint{TsMs: r.at, PVW: r.pv, GridW: r.grid}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadPowerBuckets(context.Background(), base, base+4*step, step)
	if err != nil {
		t.Fatal(err)
	}
	want := []PowerBucket{
		{StartMS: base, PVW: -500, GridW: 200},
		{StartMS: base + step, PVW: -2000, GridW: -500},
		{StartMS: base + 3*step, PVW: -100, GridW: 700},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].StartMS != want[i].StartMS || got[i].PVW != want[i].PVW || got[i].GridW != want[i].GridW || got[i].Missing != 0 {
			t.Fatalf("bucket %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := s.LoadPowerBuckets(context.Background(), base, base, step); err == nil {
		t.Fatal("an empty range must be refused")
	}
}

// One stale reading keeps its five minutes, and keeps them the same after
// the day-old ticks roll up into minutes that carry its flag.
func TestPowerBucketSurvivesTheMinuteRollup(t *testing.T) {
	s := freshStore(t)
	if err := s.EnableHistoryAggregation(); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(24 * time.Hour)
	ms := base.UnixMilli()
	const step = int64(300_000)
	for i := int64(0); i < 150; i++ {
		// As Core writes them: Load comes from the source accounting, so
		// every tick is a partial row, and the roll-up merges the stale
		// reading's flag into its whole minute.
		p := HistoryPoint{TsMs: ms + i*2_000, PVW: -300, GridW: 100, Missing: MissingLoad}
		if i == 40 {
			p.PVW = 0
			p.Missing |= MissingPV
		}
		dashboardTick(t, s, fmt.Sprint(i), &p)
	}
	check := func(when string) {
		t.Helper()
		got, err := s.LoadPowerBuckets(context.Background(), ms, ms+step, step)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Missing&MissingPV != 0 || math.Abs(got[0].PVW+300) > 1e-6 {
			t.Fatalf("%s: %+v, want -300 W measured", when, got)
		}
	}
	check("ten-second rows")
	if err := s.maintainDashboard(context.Background(), base.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check("minute rows")
}
