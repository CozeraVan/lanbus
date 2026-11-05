package hub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (h *Hub) worker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method"})
		return
	}
	a, err := readJSON(r)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad json"})
		return
	}
	a["_addr"] = r.RemoteAddr
	if !h.auth(r, true) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized", "role": "worker"})
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/worker/")
	out := h.workerOp(op, a)
	writeJSON(w, 200, out)
}

func (h *Hub) workerOp(op string, a map[string]any) map[string]any {
	name := asString(a["name"])
	if name == "" {
		name = "anonymous"
	}
	switch op {
	case "register":
		env := strings.TrimSpace(asString(a["env"]))
		caps := asList(a["caps"])
		prev, _ := h.store.one("SELECT current_task FROM workers WHERE name=?", name)
		addr := asString(a["_addr"])
		remote := workerIsRemote(asString(a["hostname"]), addr)
		cwd := asString(a["cwd"])
		_, err := h.store.exec(`INSERT INTO workers(name,env,agent,caps,started_at,last_seen,current_task,version,pid,hostname,cwd,addr,remote)
			VALUES(?,?,?,?,?,?,NULL,?,?,?,?,?,?)
			ON CONFLICT(name) DO UPDATE SET env=?,agent=?,caps=?,last_seen=?,current_task=NULL,version=?,pid=?,hostname=?,cwd=?,addr=?,remote=?`,
			name, env, asString(a["agent"]), jsonText(caps), nowText(), nowText(),
			asString(a["version"]), asInt(a["pid"]), asString(a["hostname"]), cwd, addr, boolInt(remote),
			env, asString(a["agent"]), jsonText(caps), nowText(),
			asString(a["version"]), asInt(a["pid"]), asString(a["hostname"]), cwd, addr, boolInt(remote))
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		h.emit("worker.online", map[string]any{"name": name, "env": env, "caps": caps})
		prevTask := ""
		if prev != nil {
			prevTask = asString(prev["current_task"])
		}
		return map[string]any{"ok": true, "now": nowText(), "previous_task": prevTask,
			"config": map[string]any{"lease_s": h.cfg.LeaseS, "progress_s": 3,
				"max_artifact_bytes": h.cfg.MaxArtifactBytes}}
	case "heartbeat":
		_, _ = h.store.exec("UPDATE workers SET last_seen=? WHERE name=?", nowText(), name)
		return map[string]any{"ok": true, "now": nowText(), "cancel": h.cancelIDs(name)}
	case "claim":
		env := asString(a["env"])
		task, err := h.claim(name, env)
		if err != nil {
			return map[string]any{"task": nil, "error": err.Error()}
		}
		return map[string]any{"task": task}
	case "progress":
		return h.progress(name, a)
	case "log":
		return h.logLines(name, a)
	case "submit":
		return h.submit(name, a)
	case "recover":
		return h.recoverWorker(name, a)
	case "leave":
		_, _ = h.store.exec("UPDATE workers SET last_seen=?, current_task=NULL WHERE name=?", nowText(), name)
		return map[string]any{"ok": true}
	}
	return map[string]any{"error": "unknown op"}
}

func (h *Hub) claim(name, env string) (map[string]any, error) {
	// Remote targets must have an active human approval. Check before opening
	// the SQLite transaction: querying on a second connection would deadlock
	// with MaxOpenConns(1).
	if env != "" && h.targetRequiresConfirmation(env) && h.approval(env) == nil {
		return nil, nil
	}
	tx, err := h.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	wRow, _ := tx.Query("SELECT caps FROM workers WHERE name=?", name)
	caps := []string{}
	if wRow != nil {
		cols, _ := wRow.Columns()
		if wRow.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = wRow.Scan(ptrs...)
			if len(vals) > 0 {
				caps = asList(vals[0])
			}
		}
		wRow.Close()
	}
	rows, err := tx.Query(`SELECT * FROM tasks WHERE status='queued' AND (env='' OR ?='' OR env=?)
		ORDER BY priority DESC, retry ASC, created_at ASC LIMIT 200`, env, env)
	if err != nil {
		return nil, err
	}
	tasks, err := scanRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	var picked map[string]any
	for _, t := range tasks {
		req := asList(t["required_caps"])
		if !capsMatch(caps, req) {
			continue
		}
		token := newID() + newID()
		lease := asInt(t["lease_s"])
		if lease <= 0 {
			lease = h.cfg.LeaseS
		}
		res, err := tx.Exec(`UPDATE tasks SET status='running', worker=?, claim_token=?,
			last_claim_token=NULL, started_at=?, heartbeat_at=?, lease_expires_at=?,
			cancel_requested=0, cancel_reason=NULL, updated_at=? WHERE id=? AND status='queued'`,
			name, token, nowText(), nowText(), laterText(lease), nowText(), asString(t["id"]))
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		picked = t
		picked["status"] = StatusRunning
		picked["worker"] = name
		picked["claim_token"] = token
		picked["lease_expires_at"] = laterText(lease)
		picked["log_seq"] = asInt(t["log_seq"])
		break
	}
	if picked == nil {
		_, _ = tx.Exec("UPDATE workers SET last_seen=? WHERE name=?", nowText(), name)
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	_, _ = tx.Exec("UPDATE workers SET last_seen=?, current_task=? WHERE name=?", nowText(), asString(picked["id"]), name)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	h.emit("task.claimed", map[string]any{"id": picked["id"], "worker": name})
	h.emit("task.running", map[string]any{"id": picked["id"], "worker": name})
	return picked, nil
}

func (h *Hub) taskForWorker(tid, name, claim string) (map[string]any, map[string]any) {
	r, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if r == nil {
		return nil, map[string]any{"ok": false, "error": "task not found"}
	}
	if !terminal[asString(r["status"])] {
		if asString(r["worker"]) != name {
			return nil, map[string]any{"ok": false, "invalid": true, "error": "worker mismatch"}
		}
		if asString(r["claim_token"]) != "" && claim != asString(r["claim_token"]) {
			return nil, map[string]any{"ok": false, "invalid": true, "error": "stale claim token"}
		}
	}
	return r, nil
}

func (h *Hub) progress(name string, a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	r, er := h.taskForWorker(tid, name, asString(a["claim_token"]))
	if er != nil {
		return er
	}
	if !active[asString(r["status"])] {
		return map[string]any{"ok": false, "invalid": true, "status": r["status"],
			"cancel": []string{tid}}
	}
	lease := asInt(r["lease_s"])
	if lease <= 0 {
		lease = h.cfg.LeaseS
	}
	_, _ = h.store.exec("UPDATE tasks SET heartbeat_at=?, lease_expires_at=?, updated_at=? WHERE id=?",
		nowText(), laterText(lease), nowText(), tid)
	_, _ = h.store.exec("UPDATE workers SET last_seen=? WHERE name=?", nowText(), name)
	cancel := asBool(r["cancel_requested"]) || asString(r["status"]) == StatusCancelled
	out := map[string]any{"ok": true, "now": nowText(), "status": r["status"], "lease_expires_at": laterText(lease)}
	if cancel {
		out["cancel"] = []string{tid}
	} else {
		out["cancel"] = []string{}
	}
	return out
}

func (h *Hub) logLines(name string, a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	r, er := h.taskForWorker(tid, name, asString(a["claim_token"]))
	if er != nil {
		return er
	}
	if !active[asString(r["status"])] {
		return map[string]any{"ok": false, "stale": true, "status": r["status"]}
	}
	raw, _ := a["lines"].([]any)
	if raw == nil {
		raw = []any{}
	}
	last := asInt(r["log_seq"])
	n := 0
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			m = map[string]any{"text": asString(it)}
		}
		seq := asInt(m["seq"])
		if seq <= 0 {
			seq = last + 1
		}
		text := asString(m["text"])
		if len(text) > 20000 {
			text = text[:20000]
		}
		stream := asString(m["stream"])
		if stream == "" {
			stream = "stdout"
		}
		if len(stream) > 32 {
			stream = stream[:32]
		}
		res, err := h.store.exec("INSERT OR IGNORE INTO task_logs(task_id,seq,stream,text,created_at) VALUES(?,?,?,?,?)",
			tid, seq, stream, text, nowText())
		if err == nil {
			if nf, _ := res.RowsAffected(); nf > 0 {
				n++
			}
		}
		if seq > last {
			last = seq
		}
	}
	lease := asInt(r["lease_s"])
	if lease <= 0 {
		lease = h.cfg.LeaseS
	}
	_, _ = h.store.exec(`UPDATE tasks SET log_seq=?, log_lines=COALESCE(log_lines,0)+?, last_log_at=?,
		heartbeat_at=?, lease_expires_at=?, updated_at=? WHERE id=?`,
		last, n, nowText(), nowText(), laterText(lease), nowText(), tid)
	if n > 0 {
		lastItem := raw[len(raw)-1]
		text, stream := "", "stdout"
		if m, ok := lastItem.(map[string]any); ok {
			text = asString(m["text"])
			if s := asString(m["stream"]); s != "" {
				stream = s
			}
		}
		if len(text) > 1000 {
			text = text[:1000]
		}
		h.emit("task.log", map[string]any{"id": tid, "seq": last, "stream": stream, "text": text})
	}
	return map[string]any{"ok": true, "next_seq": last}
}

func (h *Hub) submit(name string, a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	status := asString(a["status"])
	if status == "" {
		status = StatusSubmitted
	}
	if status != StatusSubmitted && status != StatusFailed && status != StatusCancelled {
		return map[string]any{"error": "bad status"}
	}
	r, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if r == nil {
		return map[string]any{"error": "not found", "task_id": tid}
	}
	claim := asString(a["claim_token"])
	if asString(r["status"]) == StatusApproved || asString(r["status"]) == StatusFailed {
		return map[string]any{"ok": true, "ignored": "task already " + asString(r["status"]), "task_id": tid}
	}
	if asString(r["status"]) == StatusCancelled || asBool(r["cancel_requested"]) {
		if asString(r["status"]) == StatusCancelled {
			return map[string]any{"ok": true, "ignored": "cancelled", "task_id": tid}
		}
	}
	if asString(r["status"]) == StatusSubmitted {
		return map[string]any{"ok": true, "ignored": "already submitted", "task_id": tid}
	}
	recovered := false
	if asString(r["status"]) == StatusQueued {
		old := asString(r["last_claim_token"])
		recovered = claim != "" && old != "" && asString(r["worker"]) == "" && claim == old
		if !recovered {
			return map[string]any{"ok": true, "ignored": "task is queued (rework/recovered)", "task_id": tid}
		}
		if status == StatusCancelled {
			return map[string]any{"ok": true, "ignored": "recovered task awaits re-claim", "task_id": tid}
		}
	}
	if !recovered {
		if asString(r["worker"]) != name {
			return map[string]any{"ok": true, "ignored": "worker mismatch", "task_id": tid}
		}
		if asString(r["claim_token"]) != "" && claim != "" && asString(r["claim_token"]) != claim {
			return map[string]any{"ok": true, "ignored": "stale claim token", "task_id": tid}
		}
		if asString(r["claim_token"]) != "" && claim == "" {
			return map[string]any{"ok": true, "ignored": "claim token required", "task_id": tid}
		}
		if !active[asString(r["status"])] {
			return map[string]any{"ok": true, "ignored": "task is " + asString(r["status"]), "task_id": tid}
		}
	}
	files := []map[string]any{}
	if status != StatusCancelled {
		files = h.saveArtifacts(tid, a["artifacts"])
	}
	result := trunc(asString(a["result"]), 20000)
	verifyOut := trunc(asString(a["verify_out"]), 20000)
	diff := trunc(asString(a["diff"]), 200000)
	changed := trunc(asString(a["changed"]), 8000)
	composed := trunc(asString(a["composed_prompt"]), 40000)
	verifyOK := 0
	if asBool(a["verify_ok"]) {
		verifyOK = 1
	}
	var res interface {
		RowsAffected() (int64, error)
	}
	var err error
	if recovered {
		res, err = h.store.exec(`UPDATE tasks SET status=?, worker=?, result=?, exit_code=?, verify_ok=?,
			verify_out=?, diff=?, changed=?, composed_prompt=?, claim_token=NULL,
			last_claim_token=NULL, updated_at=?, finished_at=?, lease_expires_at=NULL, heartbeat_at=?
			WHERE id=? AND status='queued' AND worker IS NULL AND COALESCE(last_claim_token,'')=?`,
			status, name, result, asInt(a["exit_code"]), verifyOK, verifyOut, diff, changed,
			composed, nowText(), nowText(), nowText(), tid, claim)
	} else {
		res, err = h.store.exec(`UPDATE tasks SET status=?, worker=?, result=?, exit_code=?, verify_ok=?,
			verify_out=?, diff=?, changed=?, composed_prompt=?, claim_token=NULL,
			last_claim_token=NULL, updated_at=?, finished_at=?, lease_expires_at=NULL, heartbeat_at=?
			WHERE id=? AND status IN ('claimed','running') AND worker=? AND COALESCE(claim_token,'')=?`,
			status, name, result, asInt(a["exit_code"]), verifyOK, verifyOut, diff, changed,
			composed, nowText(), nowText(), nowText(), tid, name, claim)
	}
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return map[string]any{"ok": true, "ignored": "task state changed during submit", "task_id": tid}
	}
	h.emit("task."+status, map[string]any{"id": tid, "worker": name,
		"exit_code": asInt(a["exit_code"]), "verify_ok": verifyOK})
	_, _ = h.store.exec("UPDATE workers SET current_task=NULL,last_seen=? WHERE name=?", nowText(), name)
	return map[string]any{"ok": true, "artifacts": files, "task_id": tid, "status": status}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (h *Hub) recoverWorker(name string, a map[string]any) map[string]any {
	rows, _ := h.store.query("SELECT * FROM tasks WHERE worker=? AND status IN ('claimed','running')", name)
	done := []string{}
	failed := []string{}
	force := asBool(a["force"])
	maxAge := asFloat(a["max_age_s"])
	if maxAge <= 0 {
		maxAge = 15
	}
	for _, r := range rows {
		age := time.Since(parseText(asString(r["heartbeat_at"]))).Seconds()
		if force || age > maxAge || parseText(asString(r["lease_expires_at"])).Before(time.Now()) {
			res := h.recoverTask(r, "worker "+name+" restarted / lost")
			if res == "failed" {
				failed = append(failed, asString(r["id"]))
			} else {
				done = append(done, asString(r["id"]))
			}
		}
	}
	return map[string]any{"ok": true, "recovered": done, "failed": failed}
}

func (h *Hub) cancelIDs(name string) []string {
	rows, _ := h.store.query("SELECT id FROM tasks WHERE worker=? AND cancel_requested=1", name)
	out := []string{}
	for _, r := range rows {
		out = append(out, asString(r["id"]))
	}
	return out
}

func (h *Hub) saveArtifacts(tid string, raw any) []map[string]any {
	items, _ := raw.([]any)
	out := []map[string]any{}
	total := int64(0)
	max := h.cfg.MaxArtifactBytes
	for _, it := range items {
		if len(out) >= 50 {
			break
		}
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := safeName(asString(m["name"]))
		if name == "" {
			continue
		}
		var data []byte
		if b64 := asString(m["data"]); b64 != "" {
			var err error
			data, err = base64.StdEncoding.DecodeString(b64)
			if err != nil {
				continue
			}
		} else if p := asString(m["path"]); p != "" {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			data = b
		}
		if int64(len(data)) > max || total+int64(len(data)) > max*2 {
			continue
		}
		mt := asString(m["mime"])
		if mt == "" {
			mt = mime.TypeByExtension(filepath.Ext(name))
		}
		if mt == "" {
			mt = "application/octet-stream"
		}
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		dir := filepath.Join(h.cfg.DataDir, "artifacts", tid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			continue
		}
		_, _ = h.store.exec("INSERT INTO artifacts(task_id,name,relpath,path,mime,sha256,size,created_at) VALUES(?,?,?,?,?,?,?,?)",
			tid, name, name, path, mt, sha, len(data), nowText())
		out = append(out, map[string]any{"name": name, "mime": mt, "size": len(data), "sha256": sha})
		total += int64(len(data))
	}
	return out
}

func safeName(raw string) string {
	raw = strings.ReplaceAll(raw, "\\", "/")
	parts := []string{}
	for _, p := range strings.Split(raw, "/") {
		if p == "" || p == "." || p == ".." {
			continue
		}
		if len(p) == 2 && p[1] == ':' {
			continue
		}
		parts = append(parts, strings.ReplaceAll(p, ":", "_"))
	}
	if len(parts) == 0 {
		return "artifact.bin"
	}
	return strings.Join(parts, "/")
}
