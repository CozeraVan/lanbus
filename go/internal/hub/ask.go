package hub

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func askEndpoint(host string, port int, prompt, model, service, apiKey string, useTLS bool, timeout float64, maxTokens int) map[string]any {
	if strings.TrimSpace(prompt) == "" {
		return map[string]any{"ok": false, "error": "prompt required"}
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	base := fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, fmt.Sprint(port)))
	client := &http.Client{Timeout: durationSeconds(timeout)}
	if useTLS {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	do := func(method, path string, payload any) (int, []byte) {
		var body io.Reader
		if payload != nil {
			b, _ := json.Marshal(payload)
			body = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, base+path, body)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return resp.StatusCode, b
	}
	models := func(body []byte, key string) []string {
		var obj map[string]any
		if json.Unmarshal(body, &obj) != nil {
			return nil
		}
		list, _ := obj[key].([]any)
		out := []string{}
		for _, x := range list {
			m, _ := x.(map[string]any)
			name := asString(m["id"])
			if name == "" {
				name = asString(m["name"])
			}
			if name == "" {
				name = asString(m["model"])
			}
			if name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	if service != "ollama" {
		st, body := do("GET", "/v1/models", nil)
		ms := []string{}
		if st == 200 {
			ms = models(body, "data")
		}
		use := model
		if use == "" && len(ms) > 0 {
			use = ms[0]
		}
		if use != "" {
			st, body = do("POST", "/v1/chat/completions", map[string]any{
				"model": use, "messages": []map[string]string{{"role": "user", "content": prompt}},
				"max_tokens": maxTokens, "temperature": 0.2, "stream": false,
			})
			var obj map[string]any
			_ = json.Unmarshal(body, &obj)
			reply := ""
			if choices, ok := obj["choices"].([]any); ok && len(choices) > 0 {
				if c, ok := choices[0].(map[string]any); ok {
					if msg, ok := c["message"].(map[string]any); ok {
						reply = asString(msg["content"])
					}
				}
			}
			if st == 200 && reply != "" {
				return map[string]any{"ok": true, "endpoint": base + "/v1", "api": "openai", "model": use, "reply": reply, "raw": obj}
			}
			if st == 401 || st == 403 {
				return map[string]any{"ok": false, "endpoint": base + "/v1", "status": st, "auth_required": true, "error": "endpoint requires authentication"}
			}
		}
	}
	st, body := do("GET", "/api/tags", nil)
	ms := []string{}
	if st == 200 {
		ms = models(body, "models")
	}
	use := model
	if use == "" && len(ms) > 0 {
		use = ms[0]
	}
	if use != "" {
		st, body = do("POST", "/api/chat", map[string]any{
			"model": use, "messages": []map[string]string{{"role": "user", "content": prompt}}, "stream": false,
		})
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		reply := ""
		if msg, ok := obj["message"].(map[string]any); ok {
			reply = asString(msg["content"])
		}
		if st == 200 && reply != "" {
			return map[string]any{"ok": true, "endpoint": base, "api": "ollama", "model": use, "reply": reply, "raw": obj}
		}
		st, body = do("POST", "/api/generate", map[string]any{"model": use, "prompt": prompt, "stream": false})
		_ = json.Unmarshal(body, &obj)
		reply = asString(obj["response"])
		if st == 200 && reply != "" {
			return map[string]any{"ok": true, "endpoint": base, "api": "ollama-generate", "model": use, "reply": reply, "raw": obj}
		}
	}
	return map[string]any{"ok": false, "endpoint": base, "error": "no compatible model endpoint or no model available", "models": ms}
}
