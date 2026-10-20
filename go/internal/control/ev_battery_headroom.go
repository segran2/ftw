package control

import (
	"math"
	"time"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// clampEVBatteryHeadroom keeps optional battery charge out of room an EV
// still needs. A stable offer is not proof of delivery. Fresh, aligned EV
// power may reduce the reservation, but never beyond the lowest recent offer.
// This only caps optional battery charge; it grants no EV rise or restart.
func clampEVBatteryHeadroom(targets []DispatchTarget, store *telemetry.Store, state *State, fuseMaxW float64) []DispatchTarget {
	if store == nil || state == nil || len(targets) == 0 || fuseMaxW <= 0 {
		return targets
	}
	now := state.now()
	// A recent offer rise cannot establish load already in the meter. The
	// lowest logged offer is an upper bound on credited measured EV power.
	offeredW, _, _, ok := state.evOffers.span(now)
	if !ok {
		offeredW = 0
	}
	var demandW float64
	phases := 3.0 // No phase placement promise for 1p or auto chargers.
	for _, demand := range state.evFuseDemands {
		demandW += demand.powerW
		if demand.phaseMode != "3p" {
			phases = 1
		}
	}
	if len(state.evFuseDemands) == 0 {
		return targets
	}
	meter := store.Get(state.SiteMeterDriver, telemetry.DerMeter)
	if meter == nil || !finiteFuseValue(meter.RawW) || !finiteFuseValue(fuseMaxW) {
		return floorPositiveEVBatteryTargets(targets)
	}
	creditedW := realizedEVForBatteryW(store, state, meter, now)
	needW := math.Max(0, demandW-math.Min(offeredW, creditedW))
	latest := make(map[string]float64, len(targets))
	targeted := make(map[string]bool, len(targets))
	for _, target := range targets {
		if targeted[target.Driver] {
			return floorPositiveEVBatteryTargets(targets)
		}
		targeted[target.Driver] = true
		if _, _, last, sent := state.batteryCommands[target.Driver].span(now); sent {
			latest[target.Driver] = last
		} else {
			// Without a recent order it may be discharging and stop.
			latest[target.Driver] = -state.DriverLimits[target.Driver].dischargeCap()
		}
	}
	// Battery charge stays under the level the EV may rise to.
	spare := state.importRiseCeilingW(fuseMaxW) - meter.RawW - needW
	// During measured export overflow, the existing fuse pipeline may
	// require charge to reduce export. Magnitudes alone cannot label that
	// worst phase as import; leave this direction to the existing guard.
	if meter.RawW >= -state.effectiveExportCeilingW(fuseMaxW) && state.SiteFuseVoltage > 0 {
		if phaseSpare, known := evBatteryPhaseSpareW(meter, state, needW/(state.SiteFuseVoltage*phases)); known {
			spare = math.Min(spare, phaseSpare)
		}
	}
	out := capBatteryCharge(targets, latest, state.pendingBatteryCommandsW(now), spare)
	// Charge needed to bring measured export inside its hard ceiling is
	// not optional. Preserve that part of the already validated offer;
	// this stage never invents or increases an original battery command.
	var offeredCharge, finalCharge float64
	for i := range targets {
		offeredCharge += math.Max(0, targets[i].TargetW)
		finalCharge += math.Max(0, out[i].TargetW)
	}
	exportFloor := math.Min(offeredCharge, math.Max(0, -meter.RawW-state.effectiveExportCeilingW(fuseMaxW)))
	if exportFloor > finalCharge && offeredCharge > finalCharge {
		scale := (exportFloor - finalCharge) / (offeredCharge - finalCharge)
		for i := range out {
			if targets[i].TargetW > out[i].TargetW && targets[i].TargetW > 0 {
				out[i].TargetW += (targets[i].TargetW - out[i].TargetW) * scale
			}
		}
	}
	return out
}

// capBatteryCharge keeps each battery's latest order: holding it is never an
// increase. A shortfall of measured room takes positive charge back; new
// charge needs room left after our increases the meter may not show yet. A
// reduction this tick funds nothing, as the meter does not show it, and only
// positive targets change.
func capBatteryCharge(targets []DispatchTarget, latest map[string]float64, pendingW, spare float64) []DispatchTarget {
	if !finiteFuseValue(spare) || !finiteFuseValue(pendingW) {
		return floorPositiveEVBatteryTargets(targets)
	}
	out := append([]DispatchTarget(nil), targets...)
	holds := make([]float64, len(out))
	var holdW, increaseW float64
	for i, target := range out {
		last := latest[target.Driver]
		spare -= math.Max(0, math.Min(0, target.TargetW)-math.Min(0, last)) // Lost discharge.
		if target.TargetW > 0 {
			holds[i] = math.Min(target.TargetW, math.Max(0, last))
			holdW += holds[i]
			increaseW += target.TargetW - holds[i]
		}
	}
	holdScale, increaseScale := 1.0, 0.0
	if spare < 0 && holdW > 0 {
		holdScale = math.Max(0, 1+spare/holdW)
	}
	if room := spare - pendingW; room > 0 && increaseW > 0 {
		increaseScale = math.Min(1, room/increaseW)
	}
	for i := range out {
		if out[i].TargetW <= 0 {
			continue
		}
		capped := holds[i]*holdScale + (out[i].TargetW-holds[i])*increaseScale
		if capped < out[i].TargetW {
			out[i].TargetW, out[i].Clamped = capped, true
		}
	}
	return out
}

func evBatteryPhaseSpareW(meter *telemetry.DerReading, state *State, evIncreaseA float64) (float64, bool) {
	worst, present, valid := worstPhaseA(meter, state)
	if !present {
		return 0, false
	}
	if !valid {
		return math.Inf(-1), true
	}
	margin := math.Max(1, state.SiteFuseSafetyA) // Same working margin as the EV phase guard.
	return (state.SiteFuseAmps - margin - worst - evIncreaseA) * state.SiteFuseVoltage * float64(state.SiteFusePhases), true
}

func floorPositiveEVBatteryTargets(targets []DispatchTarget) []DispatchTarget {
	out := append([]DispatchTarget(nil), targets...)
	for i := range out {
		if out[i].TargetW > 0 {
			out[i].TargetW, out[i].Clamped = 0, true
		}
	}
	return out
}

// Missing, unhealthy or unaligned EV observations reserve the full demand.
// The meter-only EV fuse gate still decides every rise/start; battery relief
// from this clamp cannot fund a rise before it appears in the site meter.
func realizedEVForBatteryW(store *telemetry.Store, state *State, meter *telemetry.DerReading, now time.Time) float64 {
	const maxAge = 15 * time.Second
	const maxSkew = 10 * time.Second
	if meter == nil || meter.UpdatedAt.After(now) || now.Sub(meter.UpdatedAt) > maxAge {
		return 0
	}
	var total float64
	for _, demand := range state.evFuseDemands {
		rd := store.Get(demand.driverName, telemetry.DerEV)
		health := store.DriverHealth(demand.driverName)
		if rd == nil || health == nil || !health.TelemetryLive() || health.DeviceFault || rd.UpdatedAt.After(now) || now.Sub(rd.UpdatedAt) > maxAge {
			continue
		}
		observation, known := telemetry.ControlPowerObservation(rd.RawW, rd.Data, rd.UpdatedAt)
		at := observation.Seen()
		age, skew := now.Sub(at), meter.UpdatedAt.Sub(at)
		if !known || age < 0 || age > maxAge || age > telemetry.ControlPowerMaxAge(rd.Data) || skew < -maxSkew || skew > maxSkew || !finiteFuseValue(observation.PowerW) {
			continue
		}
		total += math.Min(demand.powerW, math.Max(0, observation.PowerW))
	}
	return total
}
