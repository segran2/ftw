package loadpoint

import "math"

// SoCConfirmedForPlan excludes default plug-in guesses and completion estimates.
// A user correction or a matched car anchor supplies the starting level.
func SoCConfirmedForPlan(st State) bool {
	return !math.IsNaN(st.CurrentSoC) && !math.IsInf(st.CurrentSoC, 0) &&
		st.CurrentSoC >= 0 && st.CurrentSoC <= 1 &&
		st.SoCSource != "assumed" && st.SoCSource != "completed"
}
