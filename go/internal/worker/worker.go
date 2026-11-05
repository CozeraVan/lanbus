package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (w *Worker) Run() error {
	if err := w.register(); err != nil {
		return err
	}
	go w.heartbeatLoop()
	go w.outboxLoop()
	w.recover()
	for {
		resp, err := w.post("/worker/claim", map[string]any{"name": w.cfg.Name, "env": w.cfg.Env}, 70*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[worker:%s] claim failed: %v\n", w.cfg.Name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		raw, _ := resp["task"]
		if raw == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		b, _ := jsonMarshal(raw)
		var t Task
		if err := jsonUnmarshal(b, &t); err != nil {
			fmt.Fprintf(os.Stderr, "[worker:%s] bad task: %v\n", w.cfg.Name, err)
			time.Sleep(2 * time.Second)
			continue
		}
		w.exec(&t)
	}
}

func (w *Worker) register() error {
	payload := map[string]any{
		"name": w.cfg.Name, "env": w.cfg.Env, "agent": w.cfg.Agent,
		"caps": w.caps(), "version": w.version, "pid": os.Getpid(),
		"hostname": hostname(), "cwd": w.cfg.CWD,
	}
	for {
		resp, err := w.post("/worker/register", payload, 35*time.Second)
		if err == nil && asBool(resp["ok"], false) {
			fmt.Printf("[worker:%s] registered env=%s agent=%s caps=%v cwd=%s\n",
				w.cfg.Name, w.cfg.Env, w.cfg.Agent, w.caps(), w.cfg.CWD)
			return nil
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "[worker:%s] hub unreachable (%v); retry in 5s\n", w.cfg.Name, err)
		} else {
			fmt.Fprintf(os.Stderr, "[worker:%s] register rejected: %v\n", w.cfg.Name, resp)
		}
		time.Sleep(5 * time.Second)
	}
}

func (w *Worker) caps() []string {
	caps := []string{}
	for _, c := range strings.Split(w.cfg.Caps, ",") {
		if c = strings.TrimSpace(c); c != "" {
			caps = append(caps, c)
		}
	}
	if w.cfg.Agent != "" {
		caps = append(caps, w.cfg.Agent)
	}
	if w.cfg.Cmd != "" {
		caps = append(caps, "cmd")
	}
	caps = append(caps, "discover", "ask")
	if runtime.GOOS != "" {
		caps = append(caps, runtime.GOOS)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, c := range caps {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func (w *Worker) heartbeatLoop() {
	for {
		resp, err := w.post("/worker/heartbeat", map[string]any{"name": w.cfg.Name}, 25*time.Second)
		if err == nil {
			if list, ok := resp["cancel"].([]any); ok {
				for _, x := range list {
					w.cancelled(asString(x))
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func (w *Worker) cancelled(tid string) {
	if tid == "" {
		return
	}
	w.cancelIDs[tid] = true
	w.muCurrent(func(c *currentTask) {
		if c != nil && c.task.ID == tid {
			select {
			case <-c.cancel:
			default:
				close(c.cancel)
			}
		}
	})
}

func (w *Worker) recover() {
	resp, err := w.post("/worker/recover", map[string]any{
		"name": w.cfg.Name, "max_age_s": 5, "force": false,
	}, 35*time.Second)
	if err == nil {
		fmt.Printf("[worker:%s] recover: %v\n", w.cfg.Name, resp)
	}
}

func (w *Worker) progressLoop(t *Task, cancel chan struct{}, stop chan struct{}) {
	interval := time.Duration(w.cfg.ProgressS * float64(time.Second))
	for {
		select {
		case <-stop:
			return
		case <-time.After(interval):
		}
		resp, err := w.post("/worker/progress", map[string]any{
			"name": w.cfg.Name, "task_id": t.ID, "claim_token": t.ClaimToken,
		}, 15*time.Second)
		if err != nil {
			continue
		}
		if asBool(resp["invalid"], false) || asBool(resp["stale"], false) {
			select {
			case <-cancel:
			default:
				close(cancel)
			}
			return
		}
		if list, ok := resp["cancel"].([]any); ok && len(list) > 0 {
			w.cancelled(t.ID)
			return
		}
	}
}

func (w *Worker) setCurrent(c *currentTask) {
	w.current = c
}

func (w *Worker) muCurrent(fn func(*currentTask)) {
	// The worker has a single execution loop; current is only read by heartbeat.
	// Keep a small mutex-free critical section by using a package-level lock.
	mu.Lock()
	defer mu.Unlock()
	fn(w.current)
}

var mu sync.Mutex

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func asBool(v any, def bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "1" || strings.EqualFold(x, "true")
	case float64:
		return x != 0
	}
	return def
}

func asFloat(v any, def float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return def
}

func asInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return def
}
