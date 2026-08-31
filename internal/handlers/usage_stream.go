package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/usagetracker"
)

const usagePingInterval = 25 * time.Second

// HandleUsageStream serves real-time active requests and usage stats over SSE for the dashboard.
func HandleUsageStream(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		tracker := usagetracker.GetTracker()

		send := func(b []byte) bool {
			if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		// Send initial state immediately
		initialState := tracker.GetActiveState(repo)
		initBytes, err := json.Marshal(initialState)
		if err == nil {
			if !send(initBytes) {
				return
			}
		}

		ch, unsubscribe := tracker.Subscribe()
		defer unsubscribe()

		ping := time.NewTicker(usagePingInterval)
		defer ping.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-ch:
				if !ok {
					return
				}
				if !send(b) {
					return
				}
			case <-ping.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// HandleUsageStats returns current active and pending stats as JSON.
func HandleUsageStats(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker := usagetracker.GetTracker()
		state := tracker.GetActiveState(repo)
		handlerutil.WriteJSON(w, http.StatusOK, state)
	}
}
