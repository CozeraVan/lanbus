package hub

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type toolDef struct {
	Desc   string
	Schema map[string]any
	Run    func(map[string]any) map[string]any
}

func (h *Hub) tools() map[string]toolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	return map[string]toolDef{
		"lan_dispatch": {"把一张任务派给指定环境的 agent。", obj(map[string]any{
			"env": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
			"prompt": map[string]any{"type": "string"}, "context": map[string]any{"type": "string"},
			"cwd": map[string]any{"type": "string"}, "verify": map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string"}, "max_retry": map[string]any{"type": "integer"},
			"timeout_s": map[string]any{"type": "integer"}, "lease_s": map[string]any{"type": "integer"},
			"priority": map[string]any{"type": "integer"}, "parent_id": map[string]any{"type": "string"},
			"artifact_globs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"required_caps":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"browser":        map[string]any{"type": "object"}, "discover": map[string]any{"type": "object"},
			"ask": map[string]any{"type": "object"}, "wait_s": map[string]any{"type": "number"},
		}, "prompt"), h.toolDispatch},
		"lan_dispatch_many": {"一次扇出多张任务，可等待全部完成。", obj(map[string]any{
			"tasks":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"wait_s": map[string]any{"type": "number"},
		}, "tasks"), h.toolDispatchMany},
		"lan_chat": {"让 2-4 个本机 env 进行多轮对话；可续聊。", obj(map[string]any{
			"envs":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"message": map[string]any{"type": "string"}, "conversation_id": map[string]any{"type": "string"},
			"rounds": map[string]any{"type": "integer"}, "context": map[string]any{"type": "string"},
			"title": map[string]any{"type": "string"}, "timeout_s": map[string]any{"type": "integer"},
			"total_timeout_s": map[string]any{"type": "integer"},
		}, "message"), h.toolChat},
		"lan_conversations": {"列出或读取持久化对话。", obj(map[string]any{
			"conversation_id": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}), h.toolConversations},
		"lan_discover": {"侦查局域网 AI agent；env 为空时在 hub 本机扫描。", obj(map[string]any{
			"env": map[string]any{"type": "string"}, "cidr": map[string]any{"type": "string"},
			"ports": map[string]any{"type": "string"}, "deep": map[string]any{"type": "boolean"},
			"timeout": map[string]any{"type": "number"}, "http_timeout": map[string]any{"type": "number"},
			"max_hosts": map[string]any{"type": "integer"}, "include_local": map[string]any{"type": "boolean"},
			"include_processes": map[string]any{"type": "boolean"}, "wait_s": map[string]any{"type": "number"},
		}), h.toolDiscover},
		"lan_ask": {"向发现的 OpenAI/Ollama 端点提问；env 指定时派给 worker。", obj(map[string]any{
			"host": map[string]any{"type": "string"}, "port": map[string]any{"type": "integer"},
			"endpoint": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"},
			"model": map[string]any{"type": "string"}, "service": map[string]any{"type": "string"},
			"api_key": map[string]any{"type": "string"}, "tls": map[string]any{"type": "boolean"},
			"env": map[string]any{"type": "string"}, "wait_s": map[string]any{"type": "number"},
		}), h.toolAsk},
		"lan_shot": {"浏览器打开 URL、执行动作并截图。", obj(map[string]any{
			"env": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
			"cwd": map[string]any{"type": "string"}, "out": map[string]any{"type": "string"},
			"pdf": map[string]any{"type": "string"}, "full_page": map[string]any{"type": "boolean"},
			"viewport": map[string]any{"type": "object"}, "actions": map[string]any{"type": "array"},
			"wait_ms": map[string]any{"type": "integer"}, "timeout_ms": map[string]any{"type": "integer"},
			"context": map[string]any{"type": "string"}, "wait_s": map[string]any{"type": "number"},
		}), h.toolShot},
		"lan_envs": {"列出在线 worker 与 env。", obj(map[string]any{}), func(a map[string]any) map[string]any { return h.toolEnvs() }},
		"lan_list": {"列出任务。", obj(map[string]any{
			"status": map[string]any{"type": "string"}, "env": map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}), h.toolList},
		"lan_result": {"读取任务结果、日志、diff 和产物。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "include_images": map[string]any{"type": "boolean"},
		}, "task_id"), h.toolResult},
		"lan_await": {"等待任务进入可验收状态。", obj(map[string]any{
			"task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"wait_s":   map[string]any{"type": "number"},
		}, "task_ids"), h.toolAwait},
		"lan_logs": {"读取任务实时日志。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "since": map[string]any{"type": "integer"},
			"tail": map[string]any{"type": "integer"},
		}, "task_id"), h.toolLogs},
		"lan_review": {"验收 approve 或 rework。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "verdict": map[string]any{"type": "string"},
			"feedback": map[string]any{"type": "string"},
		}, "task_id", "verdict"), h.toolReview},
		"lan_cancel": {"取消任务并通知 worker。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"},
		}, "task_id"), h.toolCancel},
		"lan_interrupt": {"强制中断任务（worker 杀进程树）。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"},
		}, "task_id"), h.toolInterrupt},
		"lan_artifact": {"列出或读取任务产物。", obj(map[string]any{
			"task_id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
		}, "task_id"), h.toolArtifact},
	}
}

func (h *Hub) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, 405, map[string]any{"error": "method"})
		return
	}
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	var msg map[string]any
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeJSON(w, 400, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	mid := msg["id"]
	method := asString(msg["method"])
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	if mid == nil {
		w.WriteHeader(202)
		return
	}
	switch method {
	case "initialize":
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid, "result": map[string]any{
			"protocolVersion": asString(params["protocolVersion"]), "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":   map[string]any{"name": "lanbusd", "version": h.cfg.Version},
			"instructions": "Go hub: lan_envs -> lan_dispatch -> lan_logs/lan_result -> lan_review.",
		}})
	case "ping":
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid, "result": map[string]any{}})
	case "tools/list":
		tools := []map[string]any{}
		for n, t := range h.tools() {
			tools = append(tools, map[string]any{"name": n, "description": t.Desc, "inputSchema": t.Schema})
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid, "result": map[string]any{"tools": tools}})
	case "tools/call":
		name := asString(params["name"])
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		def, ok := h.tools()[name]
		if !ok {
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid,
				"error": map[string]any{"code": -32602, "message": "unknown tool " + name}})
			return
		}
		if req, ok := def.Schema["required"].([]string); ok {
			for _, k := range req {
				if asString(args[k]) == "" {
					writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid,
						"error": map[string]any{"code": -32602, "message": "missing required: [" + k + "]"}})
					return
				}
			}
		}
		out := def.Run(args)
		images, _ := out["_images"].([]map[string]any)
		if images == nil {
			if raw, ok := out["_images"].([]any); ok {
				for _, x := range raw {
					if m, ok := x.(map[string]any); ok {
						images = append(images, m)
					}
				}
			}
		}
		textOut := map[string]any{}
		for k, v := range out {
			if k != "_images" {
				textOut[k] = v
			}
		}
		content := []map[string]any{{"type": "text", "text": jsonText(textOut)}}
		for _, img := range images {
			content = append(content, map[string]any{
				"type": "image", "data": asString(img["data"]),
				"mimeType": asString(img["mimeType"]),
			})
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid, "result": map[string]any{
			"content": content, "isError": false,
		}})
	default:
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": mid,
			"error": map[string]any{"code": -32601, "message": "unknown method " + method}})
	}
}

func (h *Hub) toolArtifact(a map[string]any) map[string]any {
	tid := asString(a["task_id"])
	name := asString(a["name"])
	rows, _ := h.store.query("SELECT * FROM artifacts WHERE task_id=? ORDER BY name", tid)
	if name == "" {
		out := []map[string]any{}
		for _, r := range rows {
			out = append(out, artifactMeta(r))
		}
		return map[string]any{"task_id": tid, "artifacts": out}
	}
	for _, r := range rows {
		if asString(r["name"]) == name || asString(r["relpath"]) == name {
			path := asString(r["path"])
			data, err := os.ReadFile(path)
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			m := artifactMeta(r)
			if strings.HasPrefix(asString(r["mime"]), "text/") || strings.HasSuffix(asString(r["name"]), ".json") {
				m["text"] = string(data)
			} else {
				b64 := base64.StdEncoding.EncodeToString(data)
				m["data"] = b64
				if strings.HasPrefix(asString(r["mime"]), "image/") {
					m["_images"] = []map[string]any{{"name": asString(r["name"]),
						"mimeType": asString(r["mime"]), "data": b64}}
				}
			}
			return m
		}
	}
	return map[string]any{"error": "artifact not found", "task_id": tid, "name": name}
}

func artifactMeta(r map[string]any) map[string]any {
	return map[string]any{"name": asString(r["name"]), "relpath": asString(r["relpath"]),
		"mime": asString(r["mime"]), "size": r["size"], "sha256": asString(r["sha256"]),
		"created_at": r["created_at"], "url": "/artifacts/" + asString(r["task_id"]) + "/" + asString(r["name"])}
}

func (h *Hub) artifact(w http.ResponseWriter, r *http.Request) {
	if !h.auth(r, false) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	tid := parts[0]
	name := strings.TrimPrefix(parts[1], "/")
	row, _ := h.store.one("SELECT * FROM artifacts WHERE task_id=? AND name=?", tid, name)
	if row == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	path := asString(row["path"])
	if !safeUnder(filepath.Join(h.cfg.DataDir, "artifacts"), path) {
		writeJSON(w, 403, map[string]any{"error": "forbidden"})
		return
	}
	http.ServeFile(w, r, path)
}

func safeUnder(root, path string) bool {
	a, err1 := filepath.Abs(root)
	b, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.HasPrefix(b, a+string(os.PathSeparator))
}
