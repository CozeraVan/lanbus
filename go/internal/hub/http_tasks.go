package hub

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Hub) taskGet(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	tid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/")
	row, err := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if err != nil || row == nil {
		writeJSON(w, 404, map[string]any{"error": "not found", "task_id": tid})
		return
	}
	writeJSON(w, 200, h.resultMap(row, false))
}

func (h *Hub) taskLogs(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	tid := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/logs"), "/")
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	var rows []map[string]any
	var err error
	if since > 0 {
		rows, err = h.store.query("SELECT seq,stream,text,created_at FROM task_logs WHERE task_id=? AND seq>? ORDER BY seq LIMIT 2000", tid, since)
	} else {
		rows, err = h.store.query("SELECT * FROM (SELECT seq,stream,text,created_at FROM task_logs WHERE task_id=? ORDER BY seq DESC LIMIT ?) ORDER BY seq", tid, tail)
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	task, _ := h.store.one("SELECT id,status FROM tasks WHERE id=?", tid)
	status := ""
	if task != nil {
		status = asString(task["status"])
	}
	writeJSON(w, 200, map[string]any{"task_id": tid, "status": status, "logs": rows, "next_since": maxSeq(rows, since)})
}

func maxSeq(rows []map[string]any, def int) int {
	m := def
	for _, r := range rows {
		if n := asInt(r["seq"]); n > m {
			m = n
		}
	}
	return m
}

func (h *Hub) dispatchREST(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, 200, h.toolDispatch(a))
}
