package hub

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *Hub) toolDispatch(a map[string]any) map[string]any {
	kind := strings.ToLower(strings.TrimSpace(asString(a["kind"])))
	if kind == "" {
		kind = "agent"
	}
	switch kind {
	case "agent", "shell", "browser", "discover", "ask":
	default:
		return map[string]any{"error": "kind must be agent|shell|browser|discover|ask"}
	}
	prompt := strings.TrimSpace(asString(a["prompt"]))
	browser := a["browser"]
	discoverSpec := a["discover"]
	askSpec := a["ask"]
	if kind == "browser" {
		b, _ := browser.(map[string]any)
		if b == nil {
			b = map[string]any{}
		}
		if asString(b["url"]) == "" {
			b["url"] = asString(a["url"])
		}
		if asString(b["out"]) == "" {
			b["out"] = ".lanbus-shot.png"
		}
		browser = b
		if prompt == "" {
			prompt = "浏览器任务：打开 " + asString(b["url"]) + " 并执行指定动作/截图。"
		}
	}
	if kind == "discover" {
		if _, ok := discoverSpec.(map[string]any); !ok {
			discoverSpec = map[string]any{}
		}
		if prompt == "" {
			prompt = "侦查当前局域网内的 AI agent 环境，并交回 discovery.json"
		}
	}
	if kind == "ask" {
		if _, ok := askSpec.(map[string]any); !ok {
			askSpec = map[string]any{}
		}
		if prompt == "" {
			prompt = asString(askSpec.(map[string]any)["prompt"])
			if prompt == "" {
				prompt = "向发现的 AI 端点提问"
			}
		}
	}
	if prompt == "" {
		return map[string]any{"error": "prompt required"}
	}
	env := strings.TrimSpace(asString(a["env"]))
	tid := newID()
	maxRetry := asInt(a["max_retry"])
	if maxRetry == 0 {
		maxRetry = 2
	}
	maxRec := asInt(a["max_recoveries"])
	if maxRec == 0 {
		maxRec = 3
	}
	lease := asInt(a["lease_s"])
	if lease <= 0 {
		lease = h.cfg.LeaseS
	}
	timeout := asInt(a["timeout_s"])
	if timeout <= 0 {
		timeout = 900
	}
	priority := asInt(a["priority"])
	ctx := asString(a["context"])
	globs := asList(a["artifact_globs"])
	required := asList(a["required_caps"])
	if globs == nil {
		globs = []string{}
	}
	if required == nil {
		required = []string{}
	}
	if kind == "browser" {
		b, _ := browser.(map[string]any)
		out := asString(b["out"])
		if out != "" && !contains(globs, out) {
			globs = append(globs, out)
		}
		if len(required) == 0 {
			required = []string{"browser"}
		}
	}
	if kind == "discover" && len(required) == 0 {
		required = []string{"discover"}
	}
	if kind == "ask" && len(required) == 0 {
		required = []string{"ask"}
	}
	browserJSON := ""
	if browser != nil {
		if b, ok := browser.(map[string]any); ok && len(b) > 0 {
			browserJSON = jsonText(b)
		}
	}
	discoverJSON := ""
	if m, ok := discoverSpec.(map[string]any); ok && len(m) > 0 {
		discoverJSON = jsonText(m)
	}
	askJSON := ""
	if m, ok := askSpec.(map[string]any); ok && len(m) > 0 {
		askJSON = jsonText(m)
	}
	confirm := env != "" && h.targetRequiresConfirmation(env) && h.approval(env) == nil
	status := StatusQueued
	confirmReason := ""
	if confirm {
		status = StatusWaiting
		confirmReason = "remote target requires human confirmation"
	}
	_, err := h.store.exec(`INSERT INTO tasks(id,env,title,prompt,context,cwd,verify,status,retry,max_retry,
		timeout_s,lease_s,max_recoveries,recoveries,kind,browser,discover_spec,ask_spec,
		artifact_globs,required_caps,priority,parent_id,claim_token,last_claim_token,
		cancel_requested,cancel_reason,created_at,updated_at,confirm_required,confirm_reason)
		VALUES(?,?,?,?,?,?,?,?,0,?,?,?,?,0,?,?,?,?,?,?,?,?,NULL,NULL,0,NULL,?,?,?,?)`,
		tid, env, asString(a["title"]), prompt, ctx, asString(a["cwd"]), asString(a["verify"]),
		status, maxRetry, timeout, lease, maxRec, kind, browserJSON, discoverJSON, askJSON,
		jsonText(globs), jsonText(required), priority, asString(a["parent_id"]), nowText(), nowText(),
		boolInt(confirm), confirmReason)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	h.emit("task.created", map[string]any{"id": tid, "env": env, "title": asString(a["title"]), "kind": kind})
	if confirm {
		h.emit("task.waiting_confirmation", map[string]any{"id": tid, "env": env})
		return map[string]any{"task_id": tid, "status": StatusWaiting,
			"confirmation_required": true, "target": h.targetInfo(env),
			"message": "human confirmation required before dispatch"}
	}
	wait := asFloat(a["wait_s"])
	if wait > 0 {
		out := h.awaitIDs([]string{tid}, wait)
		return map[string]any{"task_id": tid, "done": out["done"], "pending": out["pending"], "results": out["results"]}
	}
	return map[string]any{"task_id": tid, "status": StatusQueued}
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		var f float64
		fmt.Sscan(x, &f)
		return f
	}
	return 0
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (h *Hub) toolEnvs() map[string]any {
	rows, _ := h.store.query("SELECT * FROM workers ORDER BY env,name")
	out := []map[string]any{}
	envSet := map[string]bool{}
	for _, w := range rows {
		caps := asList(w["caps"])
		last := parseText(asString(w["last_seen"]))
		age := 9999
		if !last.IsZero() {
			age = int(time.Since(last).Seconds())
		}
		w["caps"] = caps
		w["online"] = age < 45
		w["idle_s"] = age
		w["has_browser"] = capsMatch(caps, []string{"browser"})
		w["remote"] = asBool(w["remote"]) || workerIsRemote(asString(w["hostname"]), asString(w["addr"]))
		w["cwd"] = asString(w["cwd"])
		if ap := h.approval(asString(w["env"])); ap != nil {
			w["approved"] = true
			w["approval_expires_at"] = ap["expires_at"]
			w["approved_by"] = ap["approved_by"]
		} else {
			w["approved"] = false
		}
		delete(w, "current_task")
		out = append(out, w)
		if env := asString(w["env"]); env != "" {
			envSet[env] = true
		}
	}
	envs := []string{}
	for e := range envSet {
		envs = append(envs, e)
	}
	return map[string]any{"workers": out, "envs": envs}
}

func capsMatch(have, required []string) bool {
	for _, req := range required {
		found := false
		for _, x := range have {
			if x == req || strings.HasPrefix(x, req+":") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (h *Hub) toolList(a map[string]any) map[string]any {
	limit := asInt(a["limit"])
	if limit <= 0 {
		limit = 30
	}
	if limit > 500 {
		limit = 500
	}
	status := asString(a["status"])
	env := asString(a["env"])
	kind := asString(a["kind"])
	rows, _ := h.store.query(`SELECT * FROM tasks WHERE (?='' OR status=?) AND (?='' OR env=?)
		AND (?='' OR kind=?) ORDER BY created_at DESC LIMIT ?`,
		status, status, env, env, kind, kind, limit)
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, slim(r))
	}
	return map[string]any{"tasks": out}
}

func (h *Hub) resultMap(r map[string]any, includeImages bool) map[string]any {
	tid := asString(r["id"])
	row := map[string]any{}
	for k, v := range r {
		row[k] = v
	}
	arts, _ := h.store.query("SELECT * FROM artifacts WHERE task_id=? ORDER BY name", tid)
	am := []map[string]any{}
	urls := map[string]any{}
	for _, a := range arts {
		name := asString(a["name"])
		m := map[string]any{"name": name, "relpath": asString(a["relpath"]),
			"mime": asString(a["mime"]), "size": a["size"], "sha256": asString(a["sha256"]),
			"created_at": a["created_at"], "url": "/artifacts/" + url.PathEscape(tid) + "/" + url.PathEscape(name)}
		am = append(am, m)
		urls[name] = m["url"]
	}
	row["artifacts"] = am
	row["artifact_urls"] = urls
	logs, _ := h.store.query("SELECT seq,stream,text,created_at FROM (SELECT * FROM task_logs WHERE task_id=? ORDER BY seq DESC LIMIT 60) ORDER BY seq", tid)
	row["logs"] = logs
	tail := []string{}
	for _, l := range logs {
		txt := asString(l["text"])
		if s := asString(l["stream"]); s != "" && s != "stdout" {
			txt = "[" + s + "] " + txt
		}
		tail = append(tail, txt)
	}
	row["log_tail"] = strings.Join(tail, "\n")
	row["log_count"] = asInt(r["log_lines"])
	delete(row, "claim_token")
	delete(row, "last_claim_token")
	for _, k := range []string{"result", "verify_out", "feedback", "review_note"} {
		if s := asString(row[k]); len(s) > 20000 {
			row[k] = s[:20000]
		}
	}
	if len(asString(row["diff"])) > 200000 {
		row["diff"] = asString(row["diff"])[:200000]
	}
	if len(asString(row["composed_prompt"])) > 40000 {
		row["composed_prompt"] = asString(row["composed_prompt"])[:40000]
	}
	for _, k := range []string{"browser", "discover_spec", "ask_spec", "artifact_globs", "required_caps"} {
		if s := asString(row[k]); s != "" {
			var v any
			if json.Unmarshal([]byte(s), &v) == nil {
				row[k] = v
			}
		}
	}
	if row["discover_spec"] != nil {
		if m, ok := row["discover_spec"].(map[string]any); ok {
			delete(m, "api_key")
		}
	}
	if row["ask_spec"] != nil {
		if m, ok := row["ask_spec"].(map[string]any); ok {
			delete(m, "api_key")
		}
	}
	if includeImages {
		imgs := []map[string]any{}
		for _, a := range arts {
			if !strings.HasPrefix(asString(a["mime"]), "image/") {
				continue
			}
			if asInt(a["size"]) > 8*1024*1024 {
				continue
			}
			data, err := os.ReadFile(asString(a["path"]))
			if err != nil {
				continue
			}
			imgs = append(imgs, map[string]any{
				"name": asString(a["name"]), "mimeType": asString(a["mime"]),
				"data": base64.StdEncoding.EncodeToString(data),
			})
			if len(imgs) >= 4 {
				break
			}
		}
		if len(imgs) > 0 {
			row["_images"] = imgs
		}
	}
	return row
}

func (h *Hub) toolResult(a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	row, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if row == nil {
		return map[string]any{"error": "not found", "task_id": tid}
	}
	include := true
	if v, ok := a["include_images"]; ok {
		include = asBool(v)
	}
	return h.resultMap(row, include)
}

func (h *Hub) toolAwait(a map[string]any) map[string]any {
	ids := asList(a["task_ids"])
	wait := asFloat(a["wait_s"])
	if wait <= 0 {
		wait = 60
	}
	return h.awaitIDs(ids, wait)
}

func (h *Hub) toolReview(a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	verdict := strings.ToLower(asString(a["verdict"]))
	row, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if row == nil {
		return map[string]any{"error": "not found", "task_id": tid}
	}
	if verdict == "approve" {
		if asString(row["status"]) != StatusSubmitted {
			return map[string]any{"error": "task is not submitted", "status": row["status"]}
		}
		_, _ = h.store.exec("UPDATE tasks SET status='approved', updated_at=?, review_note=? WHERE id=?", nowText(), asString(a["feedback"]), tid)
		h.emit("task.approved", map[string]any{"id": tid})
		return map[string]any{"task_id": tid, "status": StatusApproved}
	}
	if verdict == "rework" {
		retry := asInt(row["retry"]) + 1
		maxRetry := asInt(row["max_retry"])
		if maxRetry == 0 {
			maxRetry = 2
		}
		if retry > maxRetry {
			_, _ = h.store.exec("UPDATE tasks SET status='failed', retry=?, feedback=?, updated_at=?, review_note=? WHERE id=?", retry, asString(a["feedback"]), nowText(), "rework retries exhausted", tid)
			h.emit("task.failed", map[string]any{"id": tid})
			return map[string]any{"task_id": tid, "status": StatusFailed, "reason": "rework retries exhausted"}
		}
		_, _ = h.store.exec(`UPDATE tasks SET status='queued', retry=?, feedback=?, worker=NULL,
			claim_token=NULL, last_claim_token=NULL, started_at=NULL, finished_at=NULL,
			lease_expires_at=NULL, heartbeat_at=NULL, cancel_requested=0, updated_at=? WHERE id=?`,
			retry, asString(a["feedback"]), nowText(), tid)
		h.emit("task.rework", map[string]any{"id": tid, "retry": retry})
		return map[string]any{"task_id": tid, "status": StatusQueued, "retry": retry}
	}
	return map[string]any{"error": "verdict must be approve|rework"}
}

func (h *Hub) toolCancel(a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	row, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if row == nil {
		return map[string]any{"error": "not found", "task_id": tid}
	}
	if terminal[asString(row["status"])] {
		return map[string]any{"task_id": tid, "status": row["status"], "already": true}
	}
	_, _ = h.store.exec(`UPDATE tasks SET status='cancelled', cancel_requested=1,
		cancel_reason=?, lease_expires_at=NULL, finished_at=?, updated_at=? WHERE id=?`,
		asString(a["reason"]), nowText(), nowText(), tid)
	h.emit("task.cancelled", map[string]any{"id": tid})
	return map[string]any{"task_id": tid, "status": StatusCancelled, "was": row["status"]}
}

func (h *Hub) toolInterrupt(a map[string]any) map[string]any {
	out := h.toolCancel(a)
	if _, ok := out["error"]; !ok {
		out["ctrl_c_equivalent"] = true
	}
	return out
}

func (h *Hub) toolLogs(a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	since := asInt(a["since"])
	tail := asInt(a["tail"])
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	wait := asFloat(a["wait_s"])
	deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))
	var rows []map[string]any
	status := ""
	for {
		if since > 0 {
			rows, _ = h.store.query("SELECT seq,stream,text,created_at FROM task_logs WHERE task_id=? AND seq>? ORDER BY seq LIMIT 2000", tid, since)
		} else {
			rows, _ = h.store.query("SELECT * FROM (SELECT seq,stream,text,created_at FROM task_logs WHERE task_id=? ORDER BY seq DESC LIMIT ?) ORDER BY seq", tid, tail)
		}
		task, _ := h.store.one("SELECT id,status FROM tasks WHERE id=?", tid)
		status = ""
		if task != nil {
			status = asString(task["status"])
		}
		if len(rows) > 0 || !active[status] || wait <= 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return map[string]any{"task_id": tid, "status": status, "logs": rows,
		"next_since": maxSeq(rows, since), "running": active[status]}
}
