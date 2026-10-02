package e2e

// C1, C2, C4 and C5 have no error code in the acceptance sentence, so the
// shared catalog probe cannot pass them (an empty wanted code always fails).
// Their assertions live in the dedicated tests in this package. C3 stays on
// the catalog: the probe requires PERIOD_CLOSED and must not be skipped.
func init() {
	coveredByDedicated["C1"] = true
	coveredByDedicated["C2"] = true
	coveredByDedicated["C4"] = true
	coveredByDedicated["C5"] = true
}
