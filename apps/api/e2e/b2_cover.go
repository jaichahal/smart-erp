package e2e

func init() {
	// Dedicated tests below assert these sentences against the API and Postgres.
	// The gap probe cannot pass them: the sentences carry no error code.
	for _, id := range []string{
		"B9", "B10", "B11",
		"I2", "I3", "I4", "I5", "I6", "I7", "I8", "I15", "I16",
	} {
		coveredByDedicated[id] = true
	}
}
