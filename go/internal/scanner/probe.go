package scanner

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var insecureTransport = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	DialContext:     (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	MaxIdleConns:    32,
}

func httpReq(scheme, ip string, port int, method, path string, body []byte, timeout float64, apiKey string) (int, http.Header, []byte) {
	url := scheme + "://" + net.JoinHostPort(ip, strconv.Itoa(port)) + path
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, nil, nil
	}
	req.Header.Set("User-Agent", "lanbus-discovery-go/1.0")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Transport: insecureTransport, Timeout: time.Duration(timeout * float64(time.Second))}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return resp.StatusCode, resp.Header, data
}

func jsonMap(b []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func modelsFrom(b []byte) []string {
	m := jsonMap(b)
	if m == nil {
		return nil
	}
	var arr []any
	if x, ok := m["models"].([]any); ok {
		arr = x
	} else if x, ok := m["data"].([]any); ok {
		arr = x
	}
	out := []string{}
	for _, v := range arr {
		switch x := v.(type) {
		case map[string]any:
			name := asString(x["name"])
			if name == "" {
				name = asString(x["id"])
			}
			if name == "" {
				name = asString(x["model"])
			}
			if name != "" {
				out = append(out, name)
			}
		case string:
			out = append(out, x)
		}
	}
	return out
}

func probeHTTP(ip string, port int, tlsMode bool, timeout float64) []map[string]any {
	scheme := "http"
	if tlsMode {
		scheme = "https"
	}
	services := []map[string]any{}
	seen := map[string]bool{}
	authSeen := false
	st0, _, _ := httpReq(scheme, ip, port, "GET", "/", nil, timeout, "")
	if st0 == 0 {
		return nil
	}
	add := func(kind string, extra map[string]any) {
		if seen[kind] {
			return
		}
		seen[kind] = true
		s := map[string]any{
			"kind": kind, "name": kind, "port": port,
			"transport": "http", "ai": aiKind(kind),
		}
		for k, v := range extra {
			s[k] = v
		}
		services = append(services, s)
	}
	get := func(path string) (int, http.Header, []byte) {
		return httpReq(scheme, ip, port, "GET", path, nil, timeout, "")
	}
	endpoint := scheme + "://" + net.JoinHostPort(ip, strconv.Itoa(port))

	st, _, data := get("/api/tags")
	obj := jsonMap(data)
	if st == 200 && obj != nil {
		if _, ok := obj["models"]; ok {
			add("ollama", map[string]any{"models": modelsFrom(data), "endpoint": endpoint})
		}
	}
	st2, _, data2 := get("/api/version")
	o2 := jsonMap(data2)
	if st2 == 200 && o2 != nil {
		if _, ok := o2["version"]; ok {
			add("ollama", map[string]any{"models": modelsFrom(data), "endpoint": endpoint, "version": asString(o2["version"])})
		}
	}
	st, hdr, data := get("/v1/models")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if _, ok := obj["data"].([]any); ok {
			product := "openai"
			server := strings.ToLower(hdr.Get("Server"))
			if port == 1234 {
				product = "lmstudio"
			} else if port == 11434 {
				product = "ollama"
			} else if strings.Contains(server, "llama") {
				product = "llamacpp"
			} else if strings.Contains(server, "uvicorn") && (port == 8000 || port == 8001) {
				product = "vllm"
			}
			add(product, map[string]any{"models": modelsFrom(data), "endpoint": endpoint + "/v1", "server": hdr.Get("Server"), "api": "openai"})
		}
	}
	if st == 401 || st == 403 {
		authSeen = true
	}
	st, _, data = get("/api/config")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		_, hasVer := obj["version"]
		_, hasFeat := obj["features"]
		if hasVer && (hasFeat || obj["default_models"] != nil || obj["onboarding"] != nil) {
			add("openwebui", map[string]any{"endpoint": endpoint, "version": asString(obj["version"])})
		}
	}
	st, _, data = get("/system_stats")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if obj["system"] != nil || obj["devices"] != nil {
			add("comfyui", map[string]any{"endpoint": endpoint})
		}
	}
	st, _, data = get("/props")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if obj["default_generation_settings"] != nil || obj["total_slots"] != nil {
			add("llamacpp", map[string]any{"endpoint": endpoint})
		}
	}
	st, _, data = get("/api")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if _, ok := obj["version"]; ok {
			add("jupyter", map[string]any{"endpoint": endpoint, "version": asString(obj["version"])})
		}
	}
	st, _, data = get("/config")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if obj["dependencies"] != nil || (obj["version"] != nil && obj["components"] != nil) {
			add("gradio", map[string]any{"endpoint": endpoint, "version": asString(obj["version"])})
		}
	}
	st, _, data = get("/json/version")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if _, ok := obj["Browser"]; ok {
			add("chrome-devtools", map[string]any{"endpoint": endpoint, "version": asString(obj["Browser"])})
		}
	}
	st, _, data = get("/openapi.json")
	obj = jsonMap(data)
	if st == 200 && obj != nil && obj["openapi"] != nil {
		info, _ := obj["info"].(map[string]any)
		title := asString(info["title"])
		aiHint := regexpTitleAI(title)
		pathsMap, _ := obj["paths"].(map[string]any)
		aiPaths := false
		for p := range pathsMap {
			lp := strings.ToLower(p)
			if strings.Contains(lp, "chat") || strings.Contains(lp, "agent") || strings.Contains(lp, "llm") || strings.Contains(lp, "ai/") {
				aiPaths = true
				break
			}
		}
		if aiHint || aiPaths {
			add("ai-api", map[string]any{
				"name": title, "title": title, "endpoint": endpoint,
				"docs": endpoint + "/docs", "version": asString(info["version"]),
				"paths": len(pathsMap), "api": "openapi",
			})
		}
	}
	st, _, data = get("/health")
	obj = jsonMap(data)
	if st == 200 && obj != nil {
		if asBool(obj["ok"], false) && obj["counts"] != nil {
			add("lanbus", map[string]any{"endpoint": endpoint, "version": asString(obj["version"]), "mcp": endpoint + "/mcp"})
		}
	}
	mcpBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}},
	})
	st, _, data = httpReq(scheme, ip, port, "POST", "/mcp", mcpBody, timeout, "")
	obj = jsonMap(data)
	if st == 200 || st == 202 {
		if result, ok := obj["result"].(map[string]any); ok {
			if info, ok := result["serverInfo"].(map[string]any); ok {
				if asString(info["name"]) != "" {
					add("mcp", map[string]any{"endpoint": endpoint + "/mcp", "server": asString(info["name"]), "version": asString(info["version"])})
				}
			}
		}
	}
	if len(services) == 0 && authSeen {
		add("http-auth", map[string]any{"endpoint": endpoint, "auth_required": true})
	}
	return services
}

func aiKind(kind string) bool {
	switch kind {
	case "ollama", "lmstudio", "openai", "vllm", "localai", "llamacpp", "openwebui",
		"comfyui", "gradio", "jupyter", "chrome-devtools", "mcp", "lanbus",
		"anythingllm", "dify", "n8n", "langflow", "textgen", "sillytavern",
		"koboldcpp", "streamlit", "ai-api":
		return true
	}
	return false
}

func regexpTitleAI(s string) bool {
	return regexpMust(`(?i)(^|[^a-z])(ai|agent|llm|model|chat|inference|assistant|copilot)([^a-z]|$)`).MatchString(s)
}

func regexpMust(s string) *regexp.Regexp {
	return regexp.MustCompile(s)
}

func scanHosts(targets []string, ports []int, timeout, httpTimeout float64, arp map[string]string) []map[string]any {
	jobs := make(chan string)
	results := make(chan map[string]any, len(targets))
	var wg sync.WaitGroup
	workers := 256
	if len(targets) < workers {
		workers = len(targets)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				results <- scanHost(ip, ports, timeout, httpTimeout, arp[ip])
			}
		}()
	}
	go func() {
		for _, ip := range targets {
			jobs <- ip
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	out := []map[string]any{}
	for h := range results {
		if asBool(h["alive"], false) || asString(h["mac"]) != "" {
			out = append(out, h)
		}
	}
	return out
}

func scanHost(ip string, ports []int, timeout, httpTimeout float64, mac string) map[string]any {
	open := []int{}
	services := []map[string]any{}
	for _, port := range ports {
		d := net.Dialer{Timeout: time.Duration(timeout * float64(time.Second))}
		c, err := d.Dial("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		c.Close()
		open = append(open, port)
		if port == 22 {
			services = append(services, map[string]any{"kind": "ssh", "name": "ssh", "port": 22, "transport": "tcp", "ai": false})
			continue
		}
		svcs := probeHTTP(ip, port, port == 443, httpTimeout)
		if len(svcs) == 0 {
			kind := "tcp"
			if port == 443 {
				kind = "tls"
			}
			svcs = []map[string]any{{"kind": kind, "name": kind, "port": port, "transport": "tcp", "ai": false}}
		}
		services = append(services, svcs...)
	}
	agents := []map[string]any{}
	for _, s := range services {
		if asBool(s["ai"], false) {
			agents = append(agents, s)
		}
	}
	return map[string]any{
		"ip": ip, "alive": len(open) > 0, "os_guess": guessOS(open),
		"ports": open, "services": services, "agents": agents, "mac": mac,
	}
}
