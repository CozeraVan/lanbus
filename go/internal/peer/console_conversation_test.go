package peer

import (
	"strings"
	"sync"
	"testing"
)

// consolePairApprove walks the pairing confirmation + target approval + status
// poll so a test can start sending messages immediately.
func consolePairApprove(t *testing.T, host, target *Server) {
	t.Helper()
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/pair", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "请求对话", "scope": "chat"})
	if code != 200 {
		t.Fatalf("pair code=%d out=%v", code, out)
	}
	if asString(out["status"]) == "approved" {
		return
	}
	requestID := asString(out["request_id"])
	code, out = consoleReq(t, "POST", target.URL+"/peer/approve", target.approvalSecret,
		map[string]any{"request_id": requestID, "mode": "pair", "scope": "chat"})
	if code != 200 || asString(out["status"]) != "approved" {
		t.Fatalf("approve code=%d out=%v", code, out)
	}
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/pair-status", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "request_id": requestID})
	if code != 200 || asString(out["status"]) != "approved" {
		t.Fatalf("pair-status code=%d out=%v", code, out)
	}
}

// TestConsoleMultiTurnContext proves the second message carries the first
// exchange as context, while the first message stays raw (backward compatible).
func TestConsoleMultiTurnContext(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	var received []string
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		mu.Lock()
		received = append(received, message)
		mu.Unlock()
		return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": "target"}
	})
	consolePairApprove(t, host, target)

	// Turn 1: no history yet → executor sees the raw message.
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	if code != 200 || asString(out["reply"]) != "echo:hello" {
		t.Fatalf("turn1 code=%d out=%v", code, out)
	}

	// Turn 2: executor must see turn 1 (user + reply) plus the new message.
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "again", "scope": "chat"})
	if code != 200 {
		t.Fatalf("turn2 code=%d out=%v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 executor calls, got %d", len(received))
	}
	if received[0] != "hello" {
		t.Fatalf("turn1 should be the raw message, got %q", received[0])
	}
	if !strings.Contains(received[1], "hello") || !strings.Contains(received[1], "echo:hello") || !strings.Contains(received[1], "again") {
		t.Fatalf("turn2 missing conversation context: %q", received[1])
	}

	// History endpoint returns all four turns in order.
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	turns, _ := out["turns"].([]any)
	if len(turns) != 4 {
		t.Fatalf("expected 4 turns, got %d (%v)", len(turns), out)
	}
	first, _ := turns[0].(map[string]any)
	if asString(first["role"]) != "user" || asString(first["text"]) != "hello" {
		t.Fatalf("first turn wrong: %v", first)
	}
}

// TestConsoleHistoryReset proves reset clears history and the next send is raw.
func TestConsoleHistoryReset(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	var received []string
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		mu.Lock()
		received = append(received, message)
		mu.Unlock()
		return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": "target"}
	})
	consolePairApprove(t, host, target)

	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})

	code, out := consoleReq(t, "POST", host.URL+"/peer/console/reset", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("reset code=%d out=%v", code, out)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("reset not ok: %v", out)
	}

	code, out = consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	if turns, _ := out["turns"].([]any); len(turns) != 0 {
		t.Fatalf("expected empty history after reset, got %v", out)
	}

	// After reset the next message has no context again.
	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "fresh", "scope": "chat"})
	mu.Lock()
	defer mu.Unlock()
	if received[len(received)-1] != "fresh" {
		t.Fatalf("post-reset send should be raw, got %q", received[len(received)-1])
	}
}

// TestConsoleConversationIsolation proves one target's history never leaks into
// another target's context.
func TestConsoleConversationIsolation(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	got := map[string][]string{}
	mk := func(name string) *Server {
		return newConsolePeer(t, root, name, "pair", func(message, scope, cwd string, timeout float64) map[string]any {
			mu.Lock()
			got[name] = append(got[name], message)
			mu.Unlock()
			return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": name}
		})
	}
	targetA := mk("targeta")
	targetB := mk("targetb")
	consolePairApprove(t, host, targetA)
	consolePairApprove(t, host, targetB)

	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": targetA.URL, "message": "secretA", "scope": "chat"})
	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": targetB.URL, "message": "helloB", "scope": "chat"})

	mu.Lock()
	defer mu.Unlock()
	if len(got["targetb"]) != 1 || got["targetb"][0] != "helloB" {
		t.Fatalf("target B should see only its own raw first message, got %v", got["targetb"])
	}
	if strings.Contains(got["targetb"][0], "secretA") {
		t.Fatalf("target A history leaked into B: %q", got["targetb"][0])
	}
}

// TestBuildContextMessage checks first-turn passthrough and the context budget.
func TestBuildContextMessage(t *testing.T) {
	if got := buildContextMessage(nil, "hi", 0); got != "hi" {
		t.Fatalf("first turn should pass through unchanged, got %q", got)
	}
	var turns []any
	for i := 0; i < 40; i++ {
		turns = append(turns, map[string]any{"role": "user", "text": strings.Repeat("x", 5000)})
	}
	out := buildContextMessage(turns, "latest", 0)
	if !strings.Contains(out, "latest") || !strings.Contains(out, "最新消息") {
		t.Fatalf("context missing latest message marker: %q", out[:min(200, len(out))])
	}
	if n := len([]rune(out)); n > consoleContextRunes+2000 {
		t.Fatalf("context not bounded: %d runes", n)
	}
}

// TestBuildContextMessageMaxTurns checks the caller-supplied turn limit keeps
// only the most recent turns.
func TestBuildContextMessageMaxTurns(t *testing.T) {
	turns := []any{
		map[string]any{"role": "user", "text": "one"},
		map[string]any{"role": "agent", "text": "r1"},
		map[string]any{"role": "user", "text": "two"},
		map[string]any{"role": "agent", "text": "r2"},
	}
	out := buildContextMessage(turns, "new", 2)
	if strings.Contains(out, "one") || strings.Contains(out, "r1") {
		t.Fatalf("maxTurns=2 should drop the oldest exchange: %q", out)
	}
	if !strings.Contains(out, "two") || !strings.Contains(out, "r2") || !strings.Contains(out, "new") {
		t.Fatalf("maxTurns=2 missing recent turns: %q", out)
	}
}

// TestConsoleContextToggle proves context:false sends the raw message but still
// records the exchange in history.
func TestConsoleContextToggle(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	var received []string
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		mu.Lock()
		received = append(received, message)
		mu.Unlock()
		return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": "target"}
	})
	consolePairApprove(t, host, target)

	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "again", "scope": "chat", "context": false})
	if code != 200 {
		t.Fatalf("send code=%d out=%v", code, out)
	}
	mu.Lock()
	if received[1] != "again" {
		t.Fatalf("context:false should send raw message, got %q", received[1])
	}
	mu.Unlock()
	// History still records both exchanges.
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	if turns, _ := out["turns"].([]any); len(turns) != 4 {
		t.Fatalf("expected 4 recorded turns, got %d", len(turns))
	}
}

// TestConsoleSettingsPersist proves context/max_turns are stored per conversation,
// reported by history, and honored when a later send omits them.
func TestConsoleSettingsPersist(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	var received []string
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		mu.Lock()
		received = append(received, message)
		mu.Unlock()
		return map[string]any{"ok": true, "exit_code": 0, "reply": "ack", "peer": "target"}
	})
	consolePairApprove(t, host, target)

	// First send stores context:false, max_turns:3.
	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat", "context": false, "max_turns": 3})

	code, out := consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	if ctx, _ := out["context"].(bool); ctx {
		t.Fatalf("expected persisted context=false, got %v", out["context"])
	}
	if mt := asFloat(out["max_turns"]); mt != 3 {
		t.Fatalf("expected persisted max_turns=3, got %v", out["max_turns"])
	}

	// A later send that omits settings must honor the stored context:false (raw).
	consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "again", "scope": "chat"})
	mu.Lock()
	defer mu.Unlock()
	if received[len(received)-1] != "again" {
		t.Fatalf("stored context=false should make an omitted-context send raw, got %q", received[len(received)-1])
	}
}

// TestConsoleMaxTurns proves max_turns limits how much history is injected.
func TestConsoleMaxTurns(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	var mu sync.Mutex
	var received []string
	// Short reply (not an echo of the whole prompt) so stored agent turns do not
	// nest earlier context, which would mask the turn-limit assertion.
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		mu.Lock()
		received = append(received, message)
		mu.Unlock()
		return map[string]any{"ok": true, "exit_code": 0, "reply": "ack", "peer": "target"}
	})
	consolePairApprove(t, host, target)

	for _, m := range []string{"alpha-one", "beta-two", "gamma-three"} {
		consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
			map[string]any{"controller": "host", "target_url": target.URL, "message": m, "scope": "chat"})
	}
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "delta-four", "scope": "chat", "max_turns": 2})
	if code != 200 {
		t.Fatalf("send code=%d out=%v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	last := received[len(received)-1]
	if !strings.Contains(last, "gamma-three") || !strings.Contains(last, "delta-four") {
		t.Fatalf("max_turns=2 should keep the newest exchange: %q", last)
	}
	if strings.Contains(last, "alpha-one") {
		t.Fatalf("max_turns=2 should drop the oldest exchange: %q", last)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestConsoleSendReceiptsInHistory proves each recorded agent turn carries a
// receipt (request_id + status + exit_code) for both success and executor failure.
func TestConsoleSendReceiptsInHistory(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	target := newConsolePeer(t, root, "target", "pair", func(m, sc, cw string, to float64) map[string]any {
		if strings.Contains(m, "boom") {
			return map[string]any{"ok": false, "exit_code": 2, "reply": "exploded", "peer": "target"}
		}
		return map[string]any{"ok": true, "exit_code": 0, "reply": "ok", "peer": "target"}
	})
	consolePairApprove(t, host, target)

	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	if code != 200 || asString(out["status"]) != "completed" {
		t.Fatalf("success send: code=%d status=%v", code, out["status"])
	}
	if asString(out["request_id"]) == "" {
		t.Fatalf("success send missing request_id: %v", out)
	}

	code, out = consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "boom", "scope": "chat"})
	if code != 200 || asString(out["status"]) != "failed" {
		t.Fatalf("failed send: code=%d status=%v", code, out["status"])
	}

	code, out = consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	turns, _ := out["turns"].([]any)
	if len(turns) != 4 {
		t.Fatalf("expected 4 turns, got %d", len(turns))
	}
	a1, _ := turns[1].(map[string]any)
	r1, _ := a1["receipt"].(map[string]any)
	if asString(r1["status"]) != "completed" || asString(r1["request_id"]) == "" {
		t.Fatalf("turn1 receipt = %v", r1)
	}
	a2, _ := turns[3].(map[string]any)
	r2, _ := a2["receipt"].(map[string]any)
	if asString(r2["status"]) != "failed" || asFloat(r2["exit_code"]) != 2 {
		t.Fatalf("turn2 receipt = %v", r2)
	}
}

// TestConsoleSendUnreachableRecorded proves a send that never reaches the target
// is still recorded in history with a failure receipt (send success != received).
func TestConsoleSendUnreachableRecorded(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	target := newConsolePeer(t, root, "target", "pair", func(m, sc, cw string, to float64) map[string]any {
		return map[string]any{"ok": true, "exit_code": 0, "reply": "ok", "peer": "target"}
	})
	consolePairApprove(t, host, target)
	target.Stop() // target goes offline after pairing

	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	if code != 502 {
		t.Fatalf("expected 502 for unreachable target, got %d (%v)", code, out)
	}

	code, out = consoleReq(t, "POST", host.URL+"/peer/console/history", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL})
	if code != 200 {
		t.Fatalf("history code=%d out=%v", code, out)
	}
	turns, _ := out["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("failed send should be recorded as 2 turns, got %d (%v)", len(turns), out)
	}
	a, _ := turns[1].(map[string]any)
	r, _ := a["receipt"].(map[string]any)
	if asString(r["status"]) == "" || asString(r["status"]) == "completed" {
		t.Fatalf("unreachable receipt should be a failure, got %v", r)
	}
}
