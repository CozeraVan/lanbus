package hub

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks(
 id TEXT PRIMARY KEY, env TEXT, title TEXT, prompt TEXT, context TEXT, cwd TEXT,
 verify TEXT, status TEXT, retry INTEGER DEFAULT 0, max_retry INTEGER DEFAULT 2,
 feedback TEXT, worker TEXT, result TEXT, exit_code INTEGER,
 verify_ok INTEGER DEFAULT 0, verify_out TEXT, diff TEXT, changed TEXT,
 timeout_s INTEGER DEFAULT 900, lease_s INTEGER DEFAULT 120,
 max_recoveries INTEGER DEFAULT 3, recoveries INTEGER DEFAULT 0,
 kind TEXT DEFAULT 'agent', browser TEXT, discover_spec TEXT, ask_spec TEXT,
 artifact_globs TEXT, required_caps TEXT, priority INTEGER DEFAULT 0, parent_id TEXT,
 claim_token TEXT, last_claim_token TEXT,
 cancel_requested INTEGER DEFAULT 0, cancel_reason TEXT,
 created_at TEXT, updated_at TEXT, started_at TEXT, finished_at TEXT,
 lease_expires_at TEXT, heartbeat_at TEXT, composed_prompt TEXT,
 log_seq INTEGER DEFAULT 0, log_lines INTEGER DEFAULT 0, last_log_at TEXT,
 review_note TEXT
);
CREATE TABLE IF NOT EXISTS workers(
 name TEXT PRIMARY KEY, env TEXT, agent TEXT, caps TEXT,
 started_at TEXT, last_seen TEXT, current_task TEXT,
 version TEXT, pid INTEGER, hostname TEXT
);
CREATE TABLE IF NOT EXISTS task_logs(
 id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, seq INTEGER,
 stream TEXT, text TEXT, created_at TEXT,
 UNIQUE(task_id, seq)
);
CREATE TABLE IF NOT EXISTS artifacts(
 id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, name TEXT, relpath TEXT,
 path TEXT, mime TEXT, sha256 TEXT, size INTEGER, created_at TEXT
);
CREATE TABLE IF NOT EXISTS conversations(
 id TEXT PRIMARY KEY, title TEXT, envs TEXT, seed TEXT, context TEXT,
 rounds INTEGER DEFAULT 0, turns TEXT, status TEXT, created_at TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS approvals(
  env TEXT PRIMARY KEY, name TEXT, host TEXT, cwd TEXT, approved_by TEXT,
  approved_at TEXT, expires_at TEXT, note TEXT
);
CREATE TABLE IF NOT EXISTS events(
 id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, payload TEXT, created_at TEXT
);
CREATE INDEX IF NOT EXISTS ix_tasks_status ON tasks(status, env, created_at);
CREATE INDEX IF NOT EXISTS ix_logs_task ON task_logs(task_id, seq);
CREATE INDEX IF NOT EXISTS ix_artifacts_task ON artifacts(task_id, name);
CREATE INDEX IF NOT EXISTS ix_approvals_env ON approvals(env);
`

type Store struct {
	db      *sql.DB
	dataDir string
	mu      sync.Mutex
}

func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "artifacts"), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "bus.db") + "?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db, dataDir: dataDir}
	if err := st.ensureColumns(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ensureColumns() error {
	add := func(table, name, ddl string) error {
		rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		has := false
		for rows.Next() {
			var cid int
			var n, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &n, &typ, &notnull, &dflt, &pk); err == nil && n == name {
				has = true
			}
		}
		rows.Close()
		if has {
			return nil
		}
		_, err = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + ddl)
		return err
	}
	for _, c := range []struct{ t, n, d string }{
		{"workers", "cwd", "TEXT"}, {"workers", "addr", "TEXT"},
		{"workers", "remote", "INTEGER DEFAULT 0"},
		{"tasks", "confirm_required", "INTEGER DEFAULT 0"},
		{"tasks", "confirm_reason", "TEXT"},
		{"tasks", "confirmed_by", "TEXT"},
		{"tasks", "confirmed_at", "TEXT"},
	} {
		if err := add(c.t, c.n, c.d); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DataDir() string { return s.dataDir }

func nowText() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func laterText(seconds int) string {
	return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format("2006-01-02T15:04:05Z")
}

func parseText(v string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05Z", strings.TrimSpace(v))
	return t
}

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Exec(query, args...)
}

func (s *Store) query(query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func (s *Store) one(query string, args ...any) (map[string]any, error) {
	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[c] = string(v)
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func jsonText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		var n int
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x == "1" || strings.EqualFold(x, "true")
	}
	return false
}

func asList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, y := range x {
			out = append(out, asString(y))
		}
		return out
	case string:
		var out []string
		if json.Unmarshal([]byte(x), &out) == nil {
			return out
		}
	}
	return nil
}

func slim(r map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "env", "title", "status", "retry", "max_retry",
		"kind", "verify_ok", "exit_code", "worker", "recoveries",
		"cancel_requested", "created_at", "updated_at"} {
		if v, ok := r[k]; ok {
			out[k] = v
		}
	}
	return out
}
