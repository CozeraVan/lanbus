package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (w *Worker) submit(payload map[string]any, log *logSender) map[string]any {
	os.MkdirAll(w.outboxDir, 0755)
	tid := asString(payload["task_id"])
	claim := asString(payload["claim_token"])
	path := filepath.Join(w.outboxDir, tid+"-"+short(claim)+".json")
	if b, err := json.Marshal(payload); err == nil {
		os.WriteFile(path, b, 0644)
	}
	resp, err := w.post("/worker/submit", payload, 185*time.Second)
	if err != nil {
		if log != nil {
			log.write("system", "[lanbus] 提交失败，已落 outbox: "+err.Error())
		}
		fmt.Fprintf(os.Stderr, "[worker:%s] submit deferred: %v\n", w.cfg.Name, err)
		return nil
	}
	if strings.Contains(fmt.Sprint(resp), "ignored") {
		if log != nil {
			log.write("system", "[lanbus] hub ignored submit: "+fmt.Sprint(resp["ignored"]))
		}
	}
	if asBool(resp["ok"], false) || resp["ignored"] != nil {
		os.Remove(path)
	}
	return resp
}

func (w *Worker) outboxLoop() {
	for {
		w.flushOutbox()
		time.Sleep(10 * time.Second)
	}
}

func (w *Worker) flushOutbox() {
	files, _ := filepath.Glob(filepath.Join(w.outboxDir, "*.json"))
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		resp, err := w.post("/worker/submit", payload, 185*time.Second)
		if err == nil && (asBool(resp["ok"], false) || resp["ignored"] != nil) {
			os.Remove(p)
		}
	}
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
