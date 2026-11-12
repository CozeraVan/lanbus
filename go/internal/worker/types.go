package worker

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Name          string
	Env           string
	Hub           string
	Token         string
	CWD           string
	Agent         string
	Cmd           string
	Caps          string
	ArtifactGlob  string
	Outbox        string
	LogDir        string
	ProgressS     float64
	MaxArtifactMB int
	NoBrowser     bool
	PTY           bool
	Insecure      bool
	Version       bool

	Direct              bool
	DirectPort          int
	DirectBind          string
	DirectDataDir       string
	AgentPermissionMode string
}

type Task struct {
	ID            string `json:"id"`
	Env           string `json:"env"`
	Title         string `json:"title"`
	Prompt        string `json:"prompt"`
	Context       string `json:"context"`
	CWD           string `json:"cwd"`
	Verify        string `json:"verify"`
	Status        string `json:"status"`
	Retry         int    `json:"retry"`
	MaxRetry      int    `json:"max_retry"`
	Feedback      string `json:"feedback"`
	Kind          string `json:"kind"`
	TimeoutS      int    `json:"timeout_s"`
	LeaseS        int    `json:"lease_s"`
	ClaimToken    string `json:"claim_token"`
	LogSeq        int    `json:"log_seq"`
	ArtifactGlobs string `json:"artifact_globs"`
	DiscoverSpec  any    `json:"discover_spec"`
	AskSpec       any    `json:"ask_spec"`
	BrowserSpec   any    `json:"browser"`
	ContextFile   string `json:"-"`
}

type TaskResponse struct {
	Task *Task `json:"task"`
}

type RegisterResponse struct {
	OK           bool   `json:"ok"`
	PreviousTask string `json:"previous_task"`
	Now          string `json:"now"`
}

type CancelResponse struct {
	OK      bool     `json:"ok"`
	Cancel  []string `json:"cancel"`
	Invalid bool     `json:"invalid"`
	Stale   bool     `json:"stale"`
	Status  string   `json:"status"`
}

type Worker struct {
	cfg           Config
	version       string
	httpClient    *http.Client
	current       *currentTask
	cancelIDs     map[string]bool
	outboxDir     string
	logDir        string
	artifactGlobs []string
	maxArtifact   int64
}

type currentTask struct {
	task   *Task
	cancel chan struct{}
}

func New(cfg Config) (*Worker, error) {
	if cfg.ProgressS <= 0 {
		cfg.ProgressS = 3
	}
	if cfg.MaxArtifactMB <= 0 {
		cfg.MaxArtifactMB = 32
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	home, _ := os.UserHomeDir()
	if cfg.Outbox == "" {
		cfg.Outbox = filepath.Join(home, ".lanbus", "outbox", cfg.Name)
	}
	if cfg.LogDir == "" {
		cfg.LogDir = filepath.Join(home, ".lanbus", "logs")
	}
	w := &Worker{
		cfg:         cfg,
		version:     "1.9.0-go",
		httpClient:  &http.Client{Transport: tr, Timeout: 190 * time.Second},
		cancelIDs:   map[string]bool{},
		outboxDir:   cfg.Outbox,
		logDir:      cfg.LogDir,
		maxArtifact: int64(cfg.MaxArtifactMB) * 1024 * 1024,
	}
	for _, g := range strings.Split(cfg.ArtifactGlob, ",") {
		g = strings.TrimSpace(g)
		if g != "" {
			w.artifactGlobs = append(w.artifactGlobs, g)
		}
	}
	return w, nil
}

func (w *Worker) post(path string, payload any, timeout time.Duration) (map[string]any, error) {
	data, _ := json.Marshal(payload)
	url := strings.TrimRight(w.cfg.Hub, "/") + path
	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequest("POST", url, strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
		req.Header.Set("User-Agent", "lanbus-worker-go/"+w.version)
		client := w.httpClient
		if timeout > 0 {
			client = &http.Client{Transport: w.httpClient.Transport, Timeout: timeout}
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var out map[string]any
				if len(body) > 0 {
					if e := json.Unmarshal(body, &out); e != nil {
						return nil, e
					}
				}
				return out, nil
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429 {
				return nil, fmt.Errorf("hub HTTP %d: %s", resp.StatusCode, string(body))
			}
			err = fmt.Errorf("hub HTTP %d", resp.StatusCode)
		}
		if attempt < 4 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		if err != nil && attempt == 4 {
			return nil, err
		}
	}
	return nil, fmt.Errorf("unreachable")
}

func (w *Worker) postLog(tid, claim string, lines []map[string]any) (map[string]any, error) {
	return w.post("/worker/log", map[string]any{
		"name": w.cfg.Name, "task_id": tid, "claim_token": claim, "lines": lines,
	}, 20*time.Second)
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
