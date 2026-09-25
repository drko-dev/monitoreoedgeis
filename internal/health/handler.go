package health

import (
	"encoding/json"
	"net/http"
)

// Handler returns the local health HTTP surface: no framework, stdlib
// net/http only.
//
//	GET /healthz  -> 200 if the process is alive.
//	GET /readyz   -> 200 only when state == READY, 503 otherwise.
//	GET /operationalz -> 200 only when configured camera/video targets are
//	                    processing; camera/SaaS degradation never affects liveness.
//	GET /status   -> JSON Snapshot. No secrets.
func Handler(r *Reporter) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if r.State() == StateReady && r.EdgeVisionReady() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	mux.HandleFunc("/operationalz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snapshot := r.Snapshot()
		if snapshot.Operational.State != "READY" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(snapshot.Operational)
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Snapshot())
	})

	return mux
}
