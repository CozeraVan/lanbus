package worker

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

type askResult struct {
	OK           bool     `json:"ok"`
	Endpoint     string   `json:"endpoint,omitempty"`
	API          string   `json:"api,omitempty"`
	Model        string   `json:"model,omitempty"`
	Reply        string   `json:"reply,omitempty"`
	Status       int      `json:"status,omitempty"`
	Error        string   `json:"error,omitempty"`
	AuthRequired bool     `json:"auth_required,omitempty"`
	Models       []string `json:"models,omitempty"`
	Raw          any      `json:"raw,omitempty"`
}

func (w *Worker) runAsk(t *Task, log *logSender) (int, string, []artifact) {
	spec := decodeSpec(t.AskSpec)
	host := strings.TrimSpace(asString(spec["host"]))
	port := asInt(spec["port"], 0)
	prompt := strings.TrimSpace(asString(spec["prompt"]))
	if host == "" || port <= 0 || prompt == "" {
		return 1, `{"ok":false,"error":"host, port and prompt are required"}`, nil
	}
	timeout := asFloat(spec["timeout_s"], 120)
	if timeout <= 0 || timeout > 1800 {
		timeout = 120
	}
	res := askEndpoint(host, port, prompt,
		asString(spec["model"]), asString(spec["service"]),
		asString(spec["api_key"]), asBool(spec["tls"], false),
		time.Duration(timeout*float64(time.Second)),
		asInt(spec["max_tokens"], 1024))
	blob, _ := json.MarshalIndent(res, "", "  ")
	if log != nil {
		if res.OK {
			log.write("ask", fmt.Sprintf("[go] ask ok endpoint=%s model=%s", res.Endpoint, res.Model))
		} else {
			log.write("ask", "[go] ask failed: "+res.Error)
		}
	}
	if res.OK {
		return 0, string(blob), []artifact{artifactFromBytes("ask.json", blob)}
	}
	return 1, string(blob), []artifact{artifactFromBytes("ask.json", blob)}
}

func askEndpoint(host string, port int, prompt, model, service, apiKey string, useTLS bool, timeout time.Duration, maxTokens int) askResult {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	base := fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, fmt.Sprint(port)))
	client := &http.Client{Timeout: timeout}
	if useTLS {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	get := func(path string) (int, []byte) {
		st, body := askHTTP(client, "GET", base+path, nil, apiKey)
		return st, body
	}
	post := func(path string, payload map[string]any) (int, []byte) {
		body, _ := json.Marshal(payload)
		return askHTTP(client, "POST", base+path, body, apiKey)
	}
	modelsFrom := func(body []byte, key string) []string {
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
		st, body := get("/v1/models")
		models := []string{}
		if st == 200 {
			models = modelsFrom(body, "data")
		}
		use := model
		if use == "" && len(models) > 0 {
			use = models[0]
		}
		if use != "" {
			st, body = post("/v1/chat/completions", map[string]any{
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
				return askResult{OK: true, Endpoint: base + "/v1", API: "openai", Model: use, Reply: reply, Raw: obj}
			}
			if st == 401 || st == 403 {
				return askResult{OK: false, Endpoint: base + "/v1", Status: st, AuthRequired: true,
					Error: "endpoint requires authentication"}
			}
		}
	}

	st, body := get("/api/tags")
	models := []string{}
	if st == 200 {
		models = modelsFrom(body, "models")
	}
	use := model
	if use == "" && len(models) > 0 {
		use = models[0]
	}
	if use != "" {
		st, body = post("/api/chat", map[string]any{
			"model": use, "messages": []map[string]string{{"role": "user", "content": prompt}}, "stream": false,
		})
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		reply := ""
		if msg, ok := obj["message"].(map[string]any); ok {
			reply = asString(msg["content"])
		}
		if st == 200 && reply != "" {
			return askResult{OK: true, Endpoint: base, API: "ollama", Model: use, Reply: reply, Raw: obj}
		}
		st, body = post("/api/generate", map[string]any{
			"model": use, "prompt": prompt, "stream": false,
		})
		_ = json.Unmarshal(body, &obj)
		reply = asString(obj["response"])
		if st == 200 && reply != "" {
			return askResult{OK: true, Endpoint: base, API: "ollama-generate", Model: use, Reply: reply, Raw: obj}
		}
	}
	return askResult{OK: false, Endpoint: base, Error: "no compatible model endpoint or no model available", Models: models}
}

func askHTTP(client *http.Client, method, url string, body []byte, apiKey string) (int, []byte) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if body != nil {
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode, data
}
