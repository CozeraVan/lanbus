package hub

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func hubHostname() string {
	h, _ := os.Hostname()
	return h
}

func workerIsRemote(hostname, addr string) bool {
	h := strings.TrimSpace(hostname)
	if h != "" && h == hubHostname() {
		return false
	}
	ip := strings.TrimSpace(addr)
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if ip == "" {
		return h != "" && h != hubHostname()
	}
	parsed := net.ParseIP(ip)
	if parsed != nil && (parsed.IsLoopback() || parsed.IsPrivate()) {
		// Private LAN is still remote from the hub's point of view, unless the
		// worker reports the hub's hostname (WSL/同一台机器).
		if h == "" || h == hubHostname() {
			return false
		}
	}
	if h != "" && h != hubHostname() {
		return true
	}
	return parsed != nil && !parsed.IsLoopback()
}

func (h *Hub) envIsLocal(env string) bool {
	rows, _ := h.store.query("SELECT hostname,addr,remote FROM workers WHERE env=?", env)
	for _, r := range rows {
		remote := asBool(r["remote"]) ||
			workerIsRemote(asString(r["hostname"]), asString(r["addr"]))
		if !remote {
			return true
		}
	}
	return false
}

func (h *Hub) targetRequiresConfirmation(env string) bool {
	if strings.TrimSpace(env) == "" {
		return false
	}
	return !h.envIsLocal(env)
}

func (h *Hub) approval(env string) map[string]any {
	row, _ := h.store.one("SELECT * FROM approvals WHERE env=?", env)
	if row == nil {
		return nil
	}
	exp := parseText(asString(row["expires_at"]))
	if !exp.IsZero() && exp.Before(time.Now()) {
		return nil
	}
	return row
}

func (h *Hub) targetInfo(env string) map[string]any {
	rows, _ := h.store.query("SELECT * FROM workers WHERE env=? ORDER BY last_seen DESC", env)
	workers := []map[string]any{}
	for _, r := range rows {
		workers = append(workers, map[string]any{
			"name": asString(r["name"]), "env": asString(r["env"]),
			"agent": asString(r["agent"]), "hostname": asString(r["hostname"]),
			"cwd": asString(r["cwd"]), "version": asString(r["version"]),
			"online": parseText(asString(r["last_seen"])).After(time.Now().Add(-45 * time.Second)),
			"remote": asBool(r["remote"]) || workerIsRemote(asString(r["hostname"]), asString(r["addr"])),
		})
	}
	ap := h.approval(env)
	out := map[string]any{"env": env, "workers": workers,
		"local": h.envIsLocal(env), "approved": ap != nil, "approval": ap}
	if ap != nil {
		out["approval_expires_at"] = ap["expires_at"]
		out["approved_by"] = ap["approved_by"]
	}
	return out
}

func (h *Hub) toolConfirmations(a map[string]any) map[string]any {
	rows, _ := h.store.query("SELECT * FROM workers ORDER BY env,name")
	seen := map[string]bool{}
	targets := []map[string]any{}
	for _, r := range rows {
		env := asString(r["env"])
		if env == "" || seen[env] {
			continue
		}
		seen[env] = true
		if !h.targetRequiresConfirmation(env) {
			continue
		}
		targets = append(targets, h.targetInfo(env))
	}
	waiting, _ := h.store.query("SELECT * FROM tasks WHERE status='waiting_confirmation' ORDER BY created_at")
	ws := []map[string]any{}
	for _, r := range waiting {
		ws = append(ws, slim(r))
	}
	return map[string]any{"targets": targets, "waiting": ws,
		"operator_token_file": h.cfg.DataDir + string(os.PathSeparator) + "operator-token.txt"}
}

func (h *Hub) approvals(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Method == http.MethodPost {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
	}
	if !h.authOperator(r) {
		writeJSON(w, 401, map[string]any{"error": "operator authorization required"})
		return
	}
	switch {
	case r.URL.Path == "/confirmations" || r.URL.Path == "/api/confirmations":
		writeJSON(w, 200, h.toolConfirmations(map[string]any{}))
	case r.URL.Path == "/confirm/approve" || r.URL.Path == "/api/confirm/approve":
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"error": "method"})
			return
		}
		a := map[string]any{}
		if err := json.Unmarshal(body, &a); err != nil {
			writeJSON(w, 400, map[string]any{"error": "bad json"})
			return
		}
		env := strings.TrimSpace(asString(a["env"]))
		if env == "" {
			writeJSON(w, 400, map[string]any{"error": "env required"})
			return
		}
		ttl := asInt(a["ttl_s"])
		if ttl <= 0 {
			ttl = 1800
		}
		if ttl > 86400 {
			ttl = 86400
		}
		by := asString(a["approved_by"])
		if by == "" {
			by = "operator"
		}
		_, err := h.store.exec(`INSERT INTO approvals(env,name,host,cwd,approved_by,approved_at,expires_at,note)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(env) DO UPDATE SET name=?,host=?,cwd=?,approved_by=?,approved_at=?,expires_at=?,note=?`,
			env, "", "", "", by, nowText(), laterText(ttl), asString(a["note"]),
			"", "", "", by, nowText(), laterText(ttl), asString(a["note"]))
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		res, _ := h.store.exec(`UPDATE tasks SET status='queued', confirm_required=0,
			confirmed_by=?, confirmed_at=?, updated_at=? WHERE env=? AND status='waiting_confirmation'`,
			by, nowText(), nowText(), env)
		n, _ := res.RowsAffected()
		h.emit("approval.granted", map[string]any{"env": env, "by": by, "ttl_s": ttl, "queued": n})
		writeJSON(w, 200, map[string]any{"ok": true, "env": env, "ttl_s": ttl, "queued": n,
			"expires_at": laterText(ttl)})
	case r.URL.Path == "/confirm/revoke" || r.URL.Path == "/api/confirm/revoke":
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"error": "method"})
			return
		}
		a := map[string]any{}
		if err := json.Unmarshal(body, &a); err != nil {
			writeJSON(w, 400, map[string]any{"error": "bad json"})
			return
		}
		env := strings.TrimSpace(asString(a["env"]))
		_, _ = h.store.exec("DELETE FROM approvals WHERE env=?", env)
		h.emit("approval.revoked", map[string]any{"env": env})
		writeJSON(w, 200, map[string]any{"ok": true, "env": env})
	default:
		http.NotFound(w, r)
	}
}
