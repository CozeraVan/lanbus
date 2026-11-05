package hub

import (
	_ "embed"
	"net/http"
)

//go:embed webui.html
var uiHTML string

// UIHTML returns the embedded dashboard HTML for desktop shells.
func UIHTML() string { return uiHTML }

func (h *Hub) ui(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(uiHTML))
}

func (h *Hub) apiEnvs(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	writeJSON(w, 200, h.toolEnvs())
}

func (h *Hub) apiTasks(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	limit := asInt(r.URL.Query().Get("limit"))
	writeJSON(w, 200, h.toolList(map[string]any{"limit": limit}))
}

func (h *Hub) apiChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method"})
		return
	}
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	a, err := readJSON(r)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad json"})
		return
	}
	writeJSON(w, 200, h.toolChat(a))
}

func (h *Hub) conversations(w http.ResponseWriter, r *http.Request, id string) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	if id != "" {
		writeJSON(w, 200, h.toolConversations(map[string]any{"conversation_id": id}))
		return
	}
	limit := asInt(r.URL.Query().Get("limit"))
	writeJSON(w, 200, h.toolConversations(map[string]any{"limit": limit}))
}
