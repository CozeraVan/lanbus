package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	StatusQueued    = "queued"
	StatusWaiting   = "waiting_confirmation"
	StatusClaimed   = "claimed"
	StatusRunning   = "running"
	StatusSubmitted = "submitted"
	StatusApproved  = "approved"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusRework    = "rework"
)

var terminal = map[string]bool{StatusApproved: true, StatusFailed: true, StatusCancelled: true}
var awake = map[string]bool{StatusSubmitted: true, StatusApproved: true, StatusFailed: true, StatusCancelled: true}
var active = map[string]bool{StatusClaimed: true, StatusRunning: true}

type Config struct {
	Bind             string
	Port             int
	DataDir          string
	Token            string
	WorkerToken      string
	OperatorToken    string
	Version          string
	ReapS            float64
	LeaseS           int
	MaxArtifactBytes int64
	DownloadDir      string
}

type Event struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
	Time    string         `json:"ts"`
}

type Hub struct {
	cfg      Config
	store    *Store
	mu       sync.Mutex
	subs     map[chan Event]bool
	stop     chan struct{}
	stopOnce sync.Once
}

func New(cfg Config) (*Hub, error) {
	if cfg.Bind == "" {
		cfg.Bind = "0.0.0.0"
	}
	if cfg.Port == 0 {
		cfg.Port = 8788
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	if cfg.Token == "" {
		cfg.Token = "devtoken"
	}
	if cfg.WorkerToken == "" {
		cfg.WorkerToken = cfg.Token
	}
	if cfg.Version == "" {
		cfg.Version = "1.9.0-go"
	}
	if cfg.ReapS <= 0 {
		cfg.ReapS = 5
	}
	if cfg.LeaseS <= 0 {
		cfg.LeaseS = 120
	}
	if cfg.MaxArtifactBytes <= 0 {
		cfg.MaxArtifactBytes = 32 * 1024 * 1024
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = "bin"
	}
	st, err := OpenStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if cfg.OperatorToken == "" {
		cfg.OperatorToken = loadOperatorToken(cfg.DataDir)
	}
	if cfg.OperatorToken == "" {
		cfg.OperatorToken = randomToken()
		_ = os.WriteFile(cfg.DataDir+string(os.PathSeparator)+"operator-token.txt",
			[]byte(cfg.OperatorToken+"\n"), 0o600)
	}
	return &Hub{cfg: cfg, store: st, subs: map[chan Event]bool{}, stop: make(chan struct{})}, nil
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	return home + string(os.PathSeparator) + ".lanbus"
}

func (h *Hub) Store() *Store  { return h.store }
func (h *Hub) Config() Config { return h.cfg }

func (h *Hub) Close() error {
	h.stopOnce.Do(func() { close(h.stop) })
	return h.store.Close()
}

func (h *Hub) Handler() http.Handler { return http.HandlerFunc(h.route) }

func (h *Hub) Run() error {
	go h.reaper()
	addr := net.JoinHostPort(h.cfg.Bind, strconv.Itoa(h.cfg.Port))
	srv := &http.Server{Addr: addr, Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("[lanbusd] listening %s version=%s data=%s\n", addr, h.cfg.Version, h.cfg.DataDir)
	return srv.ListenAndServe()
}

func (h *Hub) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/health":
		h.health(w, r)
	case path == "/events":
		h.sse(w, r)
	case path == "/mcp":
		h.mcp(w, r)
	case path == "/" || path == "/ui" || path == "/ui/":
		h.ui(w, r)
	case path == "/api/envs" || path == "/envs":
		h.apiEnvs(w, r)
	case path == "/api/tasks" || path == "/tasks":
		h.apiTasks(w, r)
	case path == "/chat" || path == "/api/chat":
		h.apiChat(w, r)
	case path == "/conversations" || path == "/api/conversations":
		h.conversations(w, r, "")
	case strings.HasPrefix(path, "/conversations/"):
		h.conversations(w, r, strings.Trim(strings.TrimPrefix(path, "/conversations/"), "/"))
	case strings.HasPrefix(path, "/api/conversations/"):
		h.conversations(w, r, strings.Trim(strings.TrimPrefix(path, "/api/conversations/"), "/"))
	case strings.HasPrefix(path, "/worker/"):
		h.worker(w, r)
	case strings.HasPrefix(path, "/artifacts/"):
		h.artifact(w, r)
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/logs"):
		h.taskLogs(w, r)
	case strings.HasPrefix(path, "/tasks/"):
		h.taskGet(w, r)
	case strings.HasPrefix(path, "/download/"):
		h.download(w, r)
	case path == "/dispatch":
		h.dispatchREST(w, r)
	case path == "/confirmations" || path == "/api/confirmations" ||
		path == "/confirm/approve" || path == "/api/confirm/approve" ||
		path == "/confirm/revoke" || path == "/api/confirm/revoke":
		h.approvals(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Hub) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	counts := map[string]int{}
	for _, s := range []string{StatusQueued, StatusWaiting, StatusClaimed, StatusRunning, StatusSubmitted,
		StatusApproved, StatusFailed, StatusCancelled, StatusRework} {
		rows, _ := h.store.query("SELECT COUNT(*) AS n FROM tasks WHERE status=?", s)
		if len(rows) > 0 {
			counts[s] = asInt(rows[0]["n"])
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "ts": nowText(), "version": h.cfg.Version, "counts": counts})
}

func (h *Hub) auth(r *http.Request, worker bool) bool {
	tok := requestToken(r)
	if worker {
		return tokenEqual(tok, h.cfg.WorkerToken) || tokenEqual(tok, h.cfg.Token)
	}
	return tokenEqual(tok, h.cfg.Token) || tokenEqual(tok, h.cfg.OperatorToken)
}

func (h *Hub) authOperator(r *http.Request) bool {
	return tokenEqual(requestToken(r), h.cfg.OperatorToken)
}

func requestToken(r *http.Request) string {
	tok := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(tok), "bearer ") {
		tok = strings.TrimSpace(tok[7:])
	}
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" {
		tok = r.Header.Get("X-API-Key")
	}
	return tok
}

func tokenEqual(a, b string) bool {
	return len(a) == len(b) && len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func loadOperatorToken(dir string) string {
	b, err := os.ReadFile(dir + string(os.PathSeparator) + "operator-token.txt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readJSON(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 128*1024*1024))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (h *Hub) emit(typ string, payload map[string]any) {
	txt := jsonText(payload)
	_, _ = h.store.exec("INSERT INTO events(type,payload,created_at) VALUES(?,?,?)", typ, txt, nowText())
	ev := Event{Type: typ, Payload: payload, Time: nowText()}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *Hub) subscribe() chan Event {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) reaper() {
	t := time.NewTicker(time.Duration(h.cfg.ReapS * float64(time.Second)))
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.reapOnce()
		}
	}
}

func (h *Hub) reapOnce() {
	rows, err := h.store.query("SELECT * FROM tasks WHERE status IN ('claimed','running')")
	if err != nil {
		return
	}
	for _, r := range rows {
		lease := asString(r["lease_expires_at"])
		expired := lease == "" || parseText(lease).Before(time.Now())
		if expired {
			_ = h.recoverTask(r, "lease expired")
		}
	}
}

func (h *Hub) awaitIDs(ids []string, waitSec float64) map[string]any {
	deadline := time.Now().Add(time.Duration(waitSec * float64(time.Second)))
	for {
		rows := []map[string]any{}
		if len(ids) > 0 {
			marks := make([]string, len(ids))
			args := make([]any, len(ids))
			for i, id := range ids {
				marks[i] = "?"
				args[i] = id
			}
			rows, _ = h.store.query("SELECT * FROM tasks WHERE id IN ("+strings.Join(marks, ",")+")", args...)
		}
		done := []string{}
		pending := []string{}
		for _, r := range rows {
			if awake[asString(r["status"])] {
				done = append(done, asString(r["id"]))
			} else {
				pending = append(pending, asString(r["id"]))
			}
		}
		if len(pending) == 0 || time.Now().After(deadline) {
			out := []map[string]any{}
			for _, r := range rows {
				out = append(out, slim(r))
			}
			return map[string]any{"done": done, "pending": pending, "results": out}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (h *Hub) recoverTask(r map[string]any, reason string) string {
	tid := asString(r["id"])
	rec := asInt(r["recoveries"]) + 1
	maxRec := asInt(r["max_recoveries"])
	if maxRec == 0 {
		maxRec = 3
	}
	if rec > maxRec {
		_, _ = h.store.exec(`UPDATE tasks SET status='failed', review_note=?, updated_at=?, finished_at=? WHERE id=?`,
			"[auto recover exhausted] "+reason, nowText(), nowText(), tid)
		h.emit("task.failed", map[string]any{"id": tid, "reason": reason})
		return "failed"
	}
	_, _ = h.store.exec(`UPDATE tasks SET status='queued', recoveries=?, worker=NULL,
		last_claim_token=claim_token, claim_token=NULL, lease_expires_at=NULL,
		heartbeat_at=NULL, cancel_requested=0, cancel_reason=NULL,
		composed_prompt=NULL, review_note=?, updated_at=? WHERE id=?`,
		rec, "[auto recover "+strconv.Itoa(rec)+"/"+strconv.Itoa(maxRec)+"] "+reason, nowText(), tid)
	h.emit("task.recovered", map[string]any{"id": tid, "recoveries": rec, "reason": reason, "status": "queued"})
	return "queued"
}
