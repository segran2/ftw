package state

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// PowerBucket is the measured mean power over one fixed bucket, site-signed,
// from the same stored rows /api/history draws. Each flow's mean is over the
// rows that measured it. Missing marks a flow left unmeasured in more than
// maxPowerBucketUnknownShare of the bucket's rows, or in all of them: its
// mean is not a value then.
type PowerBucket struct {
	StartMS int64
	GridW   float64
	PVW     float64
	BatW    float64
	Missing HistoryMissing
}

// LoadPowerBuckets returns the mean measured power in each bucketMs bucket of
// [fromMs, toMs), the buckets laid from fromMs. A bucket with no stored row is
// absent. Unlike an energy counter, which may move in 0.1 kWh steps, the
// power rows give each bucket its own shape. Load is not here: it comes from
// the source accounting, which the energy ledger keeps.
func (s *Store) LoadPowerBuckets(ctx context.Context, fromMs, toMs, bucketMs int64) ([]PowerBucket, error) {
	if bucketMs <= 0 || toMs <= fromMs {
		return nil, errors.New("power buckets need a positive width and range")
	}
	if (toMs-fromMs+bucketMs-1)/bucketMs > maxSeriesBuckets {
		return nil, ErrHistoryQueryLimit
	}
	if s.history == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Per flow: the mean over the rows that measured it, and how many rows
	// did not.
	flow := func(column string, bit HistoryMissing) string {
		b := strconv.Itoa(int(bit))
		return `SUM(CASE WHEN missing&` + b + `=0 THEN COALESCE(` + column + `,0)*n END)/SUM(CASE WHEN missing&` + b + `=0 THEN n END),
		       SUM(CASE WHEN missing&` + b + `<>0 THEN n ELSE 0 END)`
	}
	rows, err := s.history.QueryContext(ctx, halfOpenHistoryRange(historyTierUnion)+`
		SELECT (ts_ms - ?) / ? AS bucket, SUM(n),
		       `+flow("grid_w", MissingGrid)+`,
		       `+flow("pv_w", MissingPV)+`,
		       `+flow("bat_w", MissingBattery)+`
		FROM deduped GROUP BY bucket ORDER BY bucket`,
		fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, toMs, fromMs, bucketMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PowerBucket
	for rows.Next() {
		var b PowerBucket
		var index, total int64
		var grid, pv, bat sql.NullFloat64
		var gridOff, pvOff, batOff int64
		if err := rows.Scan(&index, &total, &grid, &gridOff, &pv, &pvOff, &bat, &batOff); err != nil {
			return nil, err
		}
		b.StartMS = fromMs + index*bucketMs
		take := func(mean sql.NullFloat64, off int64, bit HistoryMissing) float64 {
			if !mean.Valid || off*100 > total*maxPowerBucketUnknownShare {
				b.Missing |= bit
				return 0
			}
			return mean.Float64
		}
		b.GridW = take(grid, gridOff, MissingGrid)
		b.PVW = take(pv, pvOff, MissingPV)
		b.BatW = take(bat, batOff, MissingBattery)
		out = append(out, b)
	}
	return out, rows.Err()
}

// maxPowerBucketUnknownShare is the share of a bucket's rows, by weight, a
// flow may miss and still have a mean. Rows roll up after a day into minutes
// that carry any tick's flag, so one stale reading in five minutes grows
// from 3 % of the rows to 20 %. A majority rule keeps a closed bucket the
// same before and after; a tenth would turn it into a hole a day later.
const maxPowerBucketUnknownShare = 50 // percent

// halfOpenHistoryRange turns historyTierUnion's closed ranges into [from, to),
// so a row on a bucket boundary belongs to one bucket only.
func halfOpenHistoryRange(union string) string {
	union = strings.ReplaceAll(union, "ts_ms BETWEEN ? AND ?", "ts_ms >= ? AND ts_ms < ?")
	return strings.ReplaceAll(union, "last_ms BETWEEN ? AND ?", "last_ms >= ? AND last_ms < ?")
}
