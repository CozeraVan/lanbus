package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (h *Hub) sse(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "stream unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprintf(w, ": connected %s\n\n", nowText())
	fl.Flush()
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.stop:
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}
