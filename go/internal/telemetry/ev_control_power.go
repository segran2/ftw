package telemetry

import "time"

// OnlineEVControlPower qualifies raw EV delivery for optional battery-load
// reservation. A faulted charger earns no credit here (unlike total site-load
// accounting), since retaining the full reservation is the conservative choice.
func (s *Store) OnlineEVControlPower(driver string, now time.Time, maxAge time.Duration) (ControlObservation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.readings[key(driver, DerEV)]
	h := s.health[driver]
	if r == nil || h == nil || !h.TelemetryLive() || h.DeviceFault || maxAge <= 0 || r.UpdatedAt.After(now) || now.Sub(r.UpdatedAt) > maxAge {
		return ControlObservation{}, false
	}
	observation, known := ControlPowerObservation(r.RawW, r.Data, r.UpdatedAt)
	age := now.Sub(observation.Seen())
	if !known || age < 0 || age > maxAge || age > ControlPowerMaxAge(r.Data) {
		return ControlObservation{}, false
	}
	return observation, true
}
