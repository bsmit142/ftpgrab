package status

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

//go:embed index.html
var indexHTML []byte

// Handler serves the status page at / and its data at /api/status
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(t.Snapshot()); err != nil {
			log.Debug().Err(err).Msg("Cannot write status")
		}
	})
	return mux
}

// Serve runs the status page on addr until the process exits. A failure is
// logged, not fatal: grabbing carries on without the page.
func (t *Tracker) Serve(addr string) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           t.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info().Msgf("Status page listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error().Err(err).Msg("Status page stopped")
	}
}
