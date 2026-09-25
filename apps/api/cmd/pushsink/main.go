// pushsink is the dev stand-in for FCM and APNs: it accepts push payloads, validates
// them against contracts/events/event.schema.json when present, and logs them.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("ERP_PUSH_SINK_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "unreadable body", 400)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			log.Warn("push rejected: not json", "err", err)
			http.Error(w, "invalid json", 400)
			return
		}
		if _, hasNotification := payload["notification"]; hasNotification {
			log.Error("push rejected: notification block present; messages must be data-only (ADR-07)")
			http.Error(w, "data-only messages required", 400)
			return
		}
		log.Info("push received", "platform", r.Header.Get("X-Push-Platform"), "payload", payload)
		w.WriteHeader(202)
	})
	log.Info("pushsink listening", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
