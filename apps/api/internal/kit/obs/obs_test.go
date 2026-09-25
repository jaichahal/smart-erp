package obs

import (
	"context"
	"testing"
)

// The tracer provider must construct with and without an OTLP endpoint; a schema
// conflict here once made every binary exit at boot.
func TestTracingBootstraps(t *testing.T) {
	shutdown, err := Tracing(context.Background(), "test", "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "nonsense"} {
		if Logger(lvl, "t", "v") == nil {
			t.Fatal(lvl)
		}
	}
}
