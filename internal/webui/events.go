package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents streams workspace change notifications over Server-Sent Events
// (SSE) so the browser UI reacts immediately when files are created, modified,
// renamed, or deleted by MCP tool calls or other sessions.
func (u *UI) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsubscribe := u.ws.Subscribe()
	defer unsubscribe()

	// Send initial hello with current revision so the client can detect missed
	// updates on reconnect.
	initPayload, _ := json.Marshal(map[string]any{
		"seq": u.ws.Revision(),
		"op":  "hello",
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", initPayload)
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
