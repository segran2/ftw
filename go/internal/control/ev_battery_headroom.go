package control

import (
	"math"
	"time"

	"github.com/srcfl/ftw/go/internal/telemetry"
)

// clampEVBatteryHeadroom keeps optional battery charge out of room an EV
// still needs. A stable offer is not proof of delivery. Fresh, aligned EV
// power may reduce the reservation, but never beyond the lowest recent offer.
// This caps optional charge and the removal of permitted fuse relief; it
// grants no EV rise or restart.
func clampEVBatteryHeadroom(targets []DispatchTarget, store *telemetry.Store, state *State, fuseMaxW float64) []DispatchTarget {
	if store == nil || state == nil || len(targets) == 0 || fuseMaxW <= 0 {
		return targets
	}
	now := state.now()
	// The lowest recent offer caps measured delivery credit; an offer alone
	// does not establish load already in the site meter.
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
	if len(state.evFuseDemands) == 0 && len(state.evCommanded) == 0 {
		return targets
	}
	meter := store.Get(state.SiteMeterDriver, telemetry.DerMeter)
	if meter == nil {
		return floorPositiveEVBatteryTargets(targets)
	}
	gridW := meter.RawW
	if !finiteFuseValue(gridW) || !finiteFuseValue(fuseMaxW) {
		return floorPositiveEVBatteryTargets(targets)
	}
	creditedW := realizedEVForBatteryW(store, state, meter, now)
	needW := math.Max(0, demandW-math.Min(offeredW, creditedW))
	latest := make(map[string]float64, len(targets))
	lowest := make(map[string]float64, len(targets))
	targeted := make(map[string]bool, len(targets))
	for _, target := range targets {
		if targeted[target.Driver] {
			return floorPositiveEVBatteryTargets(targets)
		}
		targeted[target.Driver] = true
		if _, _, last, sent := state.batteryCommands[target.Driver].span(now); sent {
			latest[target.Driver] = last
			// Only a discharge we ordered can be kept; a battery we have
			// not ordered keeps nothing.
			if r := store.Get(target.Driver, telemetry.DerBattery); eligibleEVBatteryRelief(store, state, target.Driver, r, now) {
				lowest[target.Driver], _ = fuseTargetBounds(r, state.DriverLimits[target.Driver])
			}
		} else {
			// Without a recent order it may be discharging and stop.
			latest[target.Driver] = -state.DriverLimits[target.Driver].dischargeCap()
		}
	}
	// Battery charge stays under the level the EV may rise to. Ending a
	// discharge needs room past our own raises the meter may not show, the
	// EV's included, not past what the car still wants.
	riseW := math.Max(0, state.EVCommandedW-math.Min(offeredW, creditedW))
	ceilingW := state.importRiseCeilingW(fuseMaxW) - gridW
	spare, releaseSpare := ceilingW-needW, ceilingW-riseW
	// During measured export overflow, the existing fuse pipeline may
	// require charge to reduce export; leave that direction to it. The
	// phases carry their direction (phaseCurrentsA): the worst phase is the
	// one importing most, and an exporting phase leaves the charge more room.
	if gridW >= -state.effectiveExportCeilingW(fuseMaxW) && state.SiteFuseVoltage > 0 {
		if phaseSpare, known := evBatteryPhaseSpareW(meter, state, gridW, needW/(state.SiteFuseVoltage*phases)); known {
			spare = math.Min(spare, phaseSpare)
		}
		if phaseSpare, known := evBatteryPhaseSpareW(meter, state, gridW, riseW/(state.SiteFuseVoltage*phases)); known {
			releaseSpare = math.Min(releaseSpare, phaseSpare)
		}
	}
	// New charge still uses the full pending-command window. For ending
	// discharge, aligned fresh battery delivery may prove that rise arrived.
	pendingW := state.pendingBatteryCommandsW(now)
	releaseSpare += pendingW - pendingEVBatteryImportW(store, state, meter, latest)
	out := capBatteryCharge(targets, latest, lowest, pendingW, spare, releaseSpare)
	// Charge needed to bring measured export inside its hard ceiling is
	// not optional. Preserve that part of the already validated offer;
	// this stage never invents or increases an original battery command.
	var offeredCharge, finalCharge float64
	for i := range targets {
		offeredCharge += math.Max(0, targets[i].TargetW)
		finalCharge += math.Max(0, out[i].TargetW)
	}
	exportFloor := math.Min(offeredCharge, math.Max(0, -gridW-state.effectiveExportCeilingW(fuseMaxW)))
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
// reduction this tick funds nothing, as the meter does not show it. Ending a
// discharge is our increase too: the part releaseSpare, less our unshown
// increases, cannot take stays as discharge, never below lowest, the lowest
// order each battery's bounds allow (0 W without a reading).
func capBatteryCharge(targets []DispatchTarget, latest, lowest map[string]float64, pendingW, spare, releaseSpare float64) []DispatchTarget {
	if !finiteFuseValue(spare) || !finiteFuseValue(pendingW) {
		return floorPositiveEVBatteryTargets(targets)
	}
	out := append([]DispatchTarget(nil), targets...)
	holds := make([]float64, len(out))
	var holdW, increaseW, lostW float64
	for i, target := range out {
		last := latest[target.Driver]
		lost := math.Max(0, math.Min(0, target.TargetW)-math.Min(0, last)) // Lost discharge.
		spare, lostW = spare-lost, lostW+lost
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
	return keepEndingDischarge(out, latest, lowest, lostW+pendingW-releaseSpare-holdW*(1-holdScale))
}

// keepEndingDischarge keeps shortW of the discharge this tick's targets end,
// shared by how much each battery can keep: down to its latest order, never
// below its lowest.
func keepEndingDischarge(out []DispatchTarget, latest, lowest map[string]float64, shortW float64) []DispatchTarget {
	keep := make([]float64, len(out))
	var keepW float64
	for i, target := range out {
		if last := latest[target.Driver]; last < 0 {
			keep[i] = math.Max(0, math.Min(0, target.TargetW)-math.Max(last, lowest[target.Driver]))
			keepW += keep[i]
		}
	}
	if !(shortW > 0) || keepW <= 0 {
		return out
	}
	share := math.Min(1, shortW/keepW)
	for i := range out {
		if keep[i] > 0 {
			out[i].TargetW, out[i].Clamped = math.Min(0, out[i].TargetW)-keep[i]*share, true
		}
	}
	return out
}

func evBatteryPhaseSpareW(meter *telemetry.DerReading, state *State, gridW, evIncreaseA float64) (float64, bool) {
	worst, present, valid := worstPhaseA(meter, state, gridW)
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
		observation, known := store.OnlineEVControlPower(demand.driverName, now, maxAge)
		skew := meter.UpdatedAt.Sub(observation.Seen())
		if !known || skew < -maxSkew || skew > maxSkew {
			continue
		}
		total += math.Min(demand.powerW, math.Max(0, observation.PowerW))
	}
	return total
}

// Only a fresh, healthy battery above its floor may retain an ordered
// discharge; fuseTargetBounds applies its configured and device limits.
func eligibleEVBatteryRelief(store *telemetry.Store, state *State, driver string, r *telemetry.DerReading, now time.Time) bool {
	h := store.DriverHealth(driver)
	return r != nil && h != nil && h.TelemetryLive() && !h.DeviceFault && !r.UpdatedAt.After(now) && now.Sub(r.UpdatedAt) <= 15*time.Second && r.SoC != nil && finiteFuseValue(*r.SoC) && *r.SoC >= 0.05 && *r.SoC <= 1
}

// A qualified battery reading aligned with the site meter may prove a prior
// import increase has arrived. Otherwise retain the existing command window.
func pendingEVBatteryImportW(store *telemetry.Store, state *State, meter *telemetry.DerReading, latest map[string]float64) float64 {
	now := state.now()
	pending := state.pendingBatteryCommandsW(now)
	// Proof from current targets cannot clear a pending rise on a battery
	// that has no target in this dispatch.
	for driver, log := range state.batteryCommands {
		low, _, last, sent := log.span(now)
		if _, present := latest[driver]; sent && last > low && !present {
			return pending
		}
	}
	observedPending := 0.0
	for driver, last := range latest {
		observation, known := store.OnlineBatteryControlPower(driver, now, 15*time.Second)
		skew := meter.UpdatedAt.Sub(observation.Seen())
		if !known || skew < -10*time.Second || skew > 10*time.Second {
			return pending
		}
		observedPending += math.Max(0, last-observation.PowerW)
	}
	return math.Min(pending, observedPending)
}
