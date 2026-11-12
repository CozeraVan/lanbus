package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CozeraVan/lanbus/go/internal/scanner"
)

func (w *Worker) exec(t *Task) {
	if t.Kind == "" {
		t.Kind = "agent"
	}
	cwd := t.CWD
	if cwd == "" {
		cwd = w.cfg.CWD
	}
	fmt.Printf("\n[worker:%s] > %s %s (retry=%d kind=%s)\n", w.cfg.Name, t.ID, t.Title, t.Retry, t.Kind)
	cancel := make(chan struct{})
	mu.Lock()
	w.current = &currentTask{task: t, cancel: cancel}
	mu.Unlock()
	if w.cancelIDs[t.ID] {
		close(cancel)
	}
	log := newLogSender(w, t)
	stopProgress := make(chan struct{})
	go w.progressLoop(t, cancel, stopProgress)
	composed := w.composePrompt(t)
	log.write("system", fmt.Sprintf("[lanbus] task=%s env=%s kind=%s retry=%d cwd=%s", t.ID, w.cfg.Env, t.Kind, t.Retry, cwd))
	log.write("context", composed)
	tmpPrompt := filepath.Join(os.TempDir(), "lanbus-"+t.ID+".txt")
	os.WriteFile(tmpPrompt, []byte(composed), 0644)
	defer os.Remove(tmpPrompt)

	start := time.Now()
	rc := 0
	out := ""
	cancelled := false
	preArtifacts := []artifact{}
	switch t.Kind {
	case "discover":
		rc, out, preArtifacts = w.runDiscover(t, log)
	case "ask":
		rc, out, preArtifacts = w.runAsk(t, log)
	case "browser":
		rc = 127
		out = "(go worker: kind " + t.Kind + " is handled by the Python worker)"
	case "shell":
		rc, out = runShell(t.Prompt, cwd, cancel, t.TimeoutS, log)
	case "agent":
		rc, out = w.runAgent(t, cwd, composed, tmpPrompt, cancel, log)
	default:
		rc, out = 125, "(unknown kind "+t.Kind+")"
	}
	if isCancelClosed(cancel) {
		cancelled = true
		rc = 130
	}
	elapsed := time.Since(start).Seconds()
	fmt.Printf("[worker:%s] agent rc=%d %.1fs\n", w.cfg.Name, rc, elapsed)

	verifyOK := true
	verifyOut := ""
	if t.Verify != "" && !cancelled {
		log.write("verify", "$ "+t.Verify)
		vrc, vout := runShell(t.Verify, cwd, cancel, 1800, log)
		verifyOK = vrc == 0
		verifyOut = limit(fmt.Sprintf("$ %s\n(exit %d)\n%s", t.Verify, vrc, vout), 20000)
		log.write("system", fmt.Sprintf("[lanbus] verify rc=%d", vrc))
	}
	if isCancelClosed(cancel) {
		cancelled = true
		rc = 130
	}
	diff, changed := "", ""
	arts := append([]artifact{}, preArtifacts...)
	if !cancelled {
		diff, changed = w.gitSnapshot(cwd)
		arts = append(arts, w.collectArtifacts(cwd, t, start)...)
		for _, a := range arts {
			log.write("system", "[lanbus] artifact "+a.Name)
		}
	}
	status := "submitted"
	if cancelled {
		status = "cancelled"
	} else if rc != 0 {
		status = "failed"
	}
	payload := map[string]any{
		"name": w.cfg.Name, "task_id": t.ID, "claim_token": t.ClaimToken,
		"status": status, "exit_code": rc,
		"verify_ok": verifyOK, "verify_out": verifyOut,
		"result": limit(out, 20000), "diff": diff, "changed": changed,
		"composed_prompt": composed, "artifacts": arts,
	}
	close(stopProgress)
	log.write("system", fmt.Sprintf("[lanbus] 结束 status=%s verify_ok=%v changed=%d 行", status, verifyOK, len(strings.Split(changed, "\n"))))
	log.close()
	resp := w.submit(payload, log)
	mu.Lock()
	w.current = nil
	mu.Unlock()
	if resp != nil && asBool(resp["ok"], false) {
		fmt.Printf("[worker:%s] submitted %s status=%s verify_ok=%v artifacts=%d\n", w.cfg.Name, t.ID, status, verifyOK, len(arts))
	} else if resp != nil {
		fmt.Printf("[worker:%s] ignored %s: %v\n", w.cfg.Name, t.ID, resp["ignored"])
	} else {
		fmt.Printf("[worker:%s] submit queued for retry %s\n", w.cfg.Name, t.ID)
	}
}

func (w *Worker) runAgent(t *Task, cwd, composed, promptFile string, cancel chan struct{}, log *logSender) (int, string) {
	if w.cfg.Cmd != "" {
		cmd := strings.ReplaceAll(w.cfg.Cmd, "{prompt_file}", promptFile)
		stdin := ""
		if !strings.Contains(w.cfg.Cmd, "{prompt_file}") {
			stdin = composed
		}
		if w.cfg.PTY {
			return runShellStdinPTY(cmd, cwd, stdin, cancel, t.TimeoutS, log)
		}
		return runShellStdin(cmd, cwd, stdin, cancel, t.TimeoutS, log)
	}
	agent := w.cfg.Agent
	permMode := w.cfg.AgentPermissionMode
	if permMode == "" {
		permMode = "acceptEdits"
	}
	switch agent {
	case "claude":
		if w.cfg.PTY {
			r := runStreamingPTY("claude", []string{"-p", "--permission-mode", permMode}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
			return r.code, r.output
		}
		r := runStreaming("claude", []string{"-p", "--permission-mode", permMode}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
		return r.code, r.output
	case "codex":
		if w.cfg.PTY {
			r := runStreamingPTY("codex", []string{"exec", "--skip-git-repo-check", "-"}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
			return r.code, r.output
		}
		r := runStreaming("codex", []string{"exec", "--skip-git-repo-check", "-"}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
		return r.code, r.output
	case "kimi":
		if w.cfg.PTY {
			r := runStreamingPTY("kimi", []string{"-p", composed}, "", cwd, "", cancel, t.TimeoutS, logFn(log))
			return r.code, r.output
		}
		r := runStreaming("kimi", []string{"-p", composed}, "", cwd, "", cancel, t.TimeoutS, logFn(log))
		return r.code, r.output
	case "deepseek":
		if w.cfg.PTY {
			r := runStreamingPTY("dsh", []string{"headless", "-"}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
			return r.code, r.output
		}
		r := runStreaming("dsh", []string{"headless", "-"}, "", cwd, composed, cancel, t.TimeoutS, logFn(log))
		return r.code, r.output
	case "shell":
		return runShell(t.Prompt, cwd, cancel, t.TimeoutS, log)
	case "echo":
		return runShell("echo "+t.Prompt, cwd, cancel, t.TimeoutS, log)
	default:
		return 127, "(unknown agent " + agent + ")"
	}
}

func (w *Worker) runDiscover(t *Task, log *logSender) (int, string, []artifact) {
	spec := decodeSpec(t.DiscoverSpec)
	if log != nil {
		log.write("discover", "[go] native scanner starting")
	}
	report, err := scanner.Discover(spec)
	if err != nil {
		msg := "discover failed: " + err.Error()
		if log != nil {
			log.write("discover", msg)
		}
		return 1, msg, nil
	}
	blob, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return 1, "marshal discovery: " + err.Error(), nil
	}
	if log != nil {
		log.write("discover", fmt.Sprintf("[go] native scanner done: %v", report["summary"]))
	}
	return 0, string(blob), []artifact{artifactFromBytes("discovery.json", blob)}
}

func decodeSpec(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(x), &m) == nil {
			return m
		}
	case []byte:
		var m map[string]any
		if json.Unmarshal(x, &m) == nil {
			return m
		}
	}
	return map[string]any{}
}

func logFn(log *logSender) func(string) {
	if log == nil {
		return nil
	}
	return func(s string) { log.write("stdout", s) }
}

func runShell(cmd, cwd string, cancel chan struct{}, timeout int, log *logSender) (int, string) {
	return runShellStdin(cmd, cwd, "", cancel, timeout, log)
}

func runShellStdin(cmd, cwd, stdin string, cancel chan struct{}, timeout int, log *logSender) (int, string) {
	var f func(string)
	if log != nil {
		f = func(s string) { log.write("stdout", s) }
	}
	r := runStreaming("", nil, cmd, cwd, stdin, cancel, timeout, f)
	return r.code, r.output
}

func runShellStdinPTY(cmd, cwd, stdin string, cancel chan struct{}, timeout int, log *logSender) (int, string) {
	var f func(string)
	if log != nil {
		f = func(s string) { log.write("stdout", s) }
	}
	r := runStreamingPTY("", nil, cmd, cwd, stdin, cancel, timeout, f)
	return r.code, r.output
}

func isCancelClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// keep sync imported for future PTY work
var _ sync.Mutex
