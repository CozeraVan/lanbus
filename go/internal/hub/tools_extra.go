package hub

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/CozeraVan/lanbus/go/internal/scanner"
)

func taskStatus(out map[string]any) string {
	if s := asString(out["status"]); s != "" {
		return s
	}
	if rs, ok := out["results"].([]map[string]any); ok && len(rs) > 0 {
		return asString(rs[0]["status"])
	}
	return ""
}

func (h *Hub) artifactJSON(tid, name string) map[string]any {
	row, _ := h.store.one("SELECT * FROM artifacts WHERE task_id=? AND name=?", tid, name)
	if row == nil {
		return nil
	}
	b, err := os.ReadFile(asString(row["path"]))
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

func (h *Hub) toolDiscover(a map[string]any) map[string]any {
	spec := map[string]any{}
	for _, k := range []string{"cidr", "ports", "deep", "timeout", "http_timeout",
		"max_hosts", "include_local", "include_processes", "include_wsl", "scanner_bin"} {
		if v, ok := a[k]; ok && v != nil {
			spec[k] = v
		}
	}
	env := strings.TrimSpace(asString(a["env"]))
	wait := asFloat(a["wait_s"])
	if wait <= 0 {
		wait = 120
	}
	if env != "" {
		out := h.toolDispatch(map[string]any{
			"env": env, "kind": "discover", "prompt": defaultStr(asString(a["prompt"]), "侦查当前局域网内的 AI agent 环境"),
			"discover": spec, "timeout_s": asIntDefault(a["timeout_s"], 300), "wait_s": wait,
			"artifact_globs": []string{"discovery.json"},
		})
		tid := asString(out["task_id"])
		status := taskStatus(out)
		report := h.artifactJSON(tid, "discovery.json")
		res := map[string]any{"task_id": tid, "env": env, "status": status}
		if report != nil {
			res["report"] = report
			for _, k := range []string{"summary", "agents", "hosts", "local_agents",
				"local_services", "interfaces", "notes", "cancelled"} {
				if v, ok := report[k]; ok {
					res[k] = v
				}
			}
		}
		if p, ok := out["pending"]; ok {
			res["pending"] = p
		}
		return res
	}
	report, err := scanner.Discover(spec)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return report
}

func (h *Hub) toolAsk(a map[string]any) map[string]any {
	host := strings.TrimSpace(asString(a["host"]))
	if host == "" {
		host = strings.TrimSpace(asString(a["ip"]))
	}
	port := asInt(a["port"])
	endpoint := strings.TrimSpace(asString(a["endpoint"]))
	tls := asBool(a["tls"]) || strings.HasPrefix(strings.ToLower(endpoint), "https://")
	if endpoint != "" {
		m := regexp.MustCompile(`^(?:https?://)?([^/:]+)(?::(\d+))?/?$`).FindStringSubmatch(endpoint)
		if len(m) > 0 {
			host = m[1]
			if port == 0 && len(m) > 2 {
				port = asInt(m[2])
			}
		}
	}
	if host == "" || port == 0 {
		return map[string]any{"error": "host and port are required (or endpoint=ip:port)"}
	}
	spec := map[string]any{
		"host": host, "port": port, "prompt": asString(a["prompt"]),
		"model": asString(a["model"]), "service": asString(a["service"]),
		"api_key": asString(a["api_key"]), "timeout_s": asFloatDefault(a["timeout_s"], 120),
		"max_tokens": asIntDefault(a["max_tokens"], 1024), "tls": tls,
	}
	env := strings.TrimSpace(asString(a["env"]))
	if env != "" {
		out := h.toolDispatch(map[string]any{
			"env": env, "kind": "ask", "ask": spec, "title": defaultStr(asString(a["title"]), "向局域网 AI 端点提问"),
			"prompt": asString(spec["prompt"]), "timeout_s": asInt(spec["timeout_s"]) + 30,
			"wait_s": asFloatDefault(a["wait_s"], 120),
		})
		tid := asString(out["task_id"])
		status := taskStatus(out)
		res := map[string]any{"task_id": tid, "env": env, "status": status}
		if status == StatusSubmitted || status == StatusFailed {
			if r := h.artifactJSON(tid, "ask.json"); r != nil {
				for k, v := range r {
					res[k] = v
				}
			}
		}
		return res
	}
	return askEndpoint(host, port, asString(spec["prompt"]), asString(spec["model"]),
		asString(spec["service"]), asString(spec["api_key"]), tls,
		asFloatDefault(a["timeout_s"], 120), asIntDefault(a["max_tokens"], 1024))
}

func (h *Hub) toolShot(a map[string]any) map[string]any {
	url := strings.TrimSpace(asString(a["url"]))
	if url == "" {
		url = strings.TrimSpace(asString(a["target"]))
	}
	if url == "" {
		return map[string]any{"error": "url required"}
	}
	out := defaultStr(asString(a["out"]), ".lanbus-shot.png")
	spec := map[string]any{
		"url": url, "out": out,
		"viewport":  map[string]any{"width": 1440, "height": 900},
		"full_page": true, "timeout_ms": asIntDefault(a["timeout_ms"], 30000),
		"wait_ms": asInt(a["wait_ms"]), "actions": a["actions"],
		"pdf": asString(a["pdf"]),
	}
	if v, ok := a["viewport"]; ok {
		spec["viewport"] = v
	}
	if v, ok := a["full_page"]; ok {
		spec["full_page"] = asBool(v)
	}
	merged := map[string]any{}
	for k, v := range a {
		merged[k] = v
	}
	merged["kind"] = "browser"
	merged["browser"] = spec
	merged["prompt"] = defaultStr(asString(a["prompt"]),
		"在浏览器中打开 "+url+"，执行动作并截图到 "+out)
	merged["artifact_globs"] = append(asList(a["artifact_globs"]), out)
	merged["title"] = defaultStr(asString(a["title"]), "浏览器截图")
	if a["wait_s"] == nil {
		merged["wait_s"] = 180
	}
	return h.toolDispatch(merged)
}

func (h *Hub) toolDispatchMany(a map[string]any) map[string]any {
	items, _ := a["tasks"].([]any)
	ids := []string{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		cp := map[string]any{}
		for k, v := range m {
			cp[k] = v
		}
		cp["wait_s"] = 0
		r := h.toolDispatch(cp)
		if id := asString(r["task_id"]); id != "" {
			ids = append(ids, id)
		}
	}
	h.emit("task.fanout", map[string]any{"ids": ids})
	res := map[string]any{"task_ids": ids}
	if w := asFloat(a["wait_s"]); w > 0 {
		for k, v := range h.awaitIDs(ids, w) {
			res[k] = v
		}
	}
	return res
}

func defaultStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func asIntDefault(v any, def int) int {
	n := asInt(v)
	if n == 0 {
		return def
	}
	return n
}

func asFloatDefault(v any, def float64) float64 {
	n := asFloat(v)
	if n == 0 {
		return def
	}
	return n
}
