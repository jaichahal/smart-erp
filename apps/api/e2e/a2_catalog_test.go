package e2e

import "testing"

func init() {
	// A11–A16 are scenario tests, not error-code probes. The catalog skips an
	// id once a dedicated test asserts the acceptance sentence.
	for _, id := range []string{"A11", "A12", "A13", "A14", "A15", "A16"} {
		coveredByDedicated[id] = true
	}
}

func TestAcceptanceCatalogP1_3(t *testing.T) {
	s := newLiveStack(t)
	t.Run("P1.3/A11", func(t *testing.T) { acceptA11(t, s) })
	t.Run("P1.3/A12", func(t *testing.T) { acceptA12(t, s) })
	t.Run("P1.3/A13", func(t *testing.T) { acceptA13(t, s) })
	t.Run("P1.3/A14", func(t *testing.T) { acceptA14(t, s) })
	t.Run("P1.3/A15", func(t *testing.T) { acceptA15(t, s) })
	t.Run("P1.3/A16", func(t *testing.T) { acceptA16(t, s) })
}
