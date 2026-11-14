package peer

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/CozeraVan/lanbus/go/internal/scanner"
)

//go:embed console.html
var consoleHTML string

// peersRoot is the directory holding sibling local peer data dirs. The console
// manages local controllers/targets that live beside this node's DataDir
// (default ~/.lanbus/peers).
func (s *Server) peersRoot() string { return filepath.Dir(s.cfg.DataDir) }

// consoleAuth enforces loopback + approval secret for the console JSON APIs.
// The console can drive any local identity, so it stays on the same trust
// boundary as the approval page: local only, secret required.
func (s *Server) consoleAuth(r *http.Request) bool {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	return isLoopback(remoteIP) && r.Header.Get("X-Lanbus-Approval") == s.approvalSecret
}

// controllerDir resolves a single-segment local peer name to its data dir,
// rejecting traversal so the console can only use real sibling peers.
func (s *Server) controllerDir(name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("controller 必须是本机 peer 名称")
	}
	dir := filepath.Join(s.peersRoot(), name)
	if !isDir(dir) {
		return "", fmt.Errorf("本机 peer 不存在：%s", name)
	}
	return dir, nil
}

// consoleLocals enumerates local direct peers (candidate controllers/targets).
func (s *Server) consoleLocals() (int, any) {
	items, err := os.ReadDir(s.peersRoot())
	if err != nil {
		return 200, map[string]any{"locals": []any{}}
	}
	locals := []any{}
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		dir := filepath.Join(s.peersRoot(), item.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "peer.json"))
		if err != nil {
			continue
		}
		info, err := ParseObject(raw)
		if err != nil {
			continue
		}
		url := asString(info["url"])
		if url == "" {
			continue
		}
		entry := map[string]any{
			"name": item.Name(), "dir": dir, "url": url,
			"peer_id": asString(info["peer_id"]), "online": false,
		}
		if health, err := JSONRequest(url+"/peer/health", nil, nil, 3); err == nil {
			entry["online"] = true
			entry["agent"] = asString(health["agent"])
			entry["display_name"] = asString(health["display_name"])
			entry["instance_label"] = asString(health["instance_label"])
			entry["request_policy"] = asString(health["request_policy"])
			entry["company_name"] = asString(health["company_name"])
			entry["hostname"] = asString(health["hostname"])
		}
		locals = append(locals, entry)
	}
	return 200, map[string]any{"locals": locals}
}

// consoleDiscover runs the local + LAN scanner and returns the agent report.
func (s *Server) consoleDiscover(body map[string]any) (int, any) {
	spec := map[string]any{}
	for _, k := range []string{"cidr", "ports", "deep", "timeout", "http_timeout",
		"max_hosts", "include_local", "include_processes", "include_wsl"} {
		if v, ok := body[k]; ok {
			spec[k] = v
		}
	}
	report, err := scanner.Discover(spec)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	// The scanner report uses typed slices/structs ([]int, Iface, …) that the
	// peer canonical encoder does not handle; round-trip through standard JSON
	// into generic types (json.Number / []any / map[string]any) first.
	raw, err := json.Marshal(report)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	norm, err := ParseObject(raw)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	return 200, map[string]any{
		"ok": norm["ok"], "local_agents": norm["local_agents"],
		"agents": norm["agents"], "hosts": norm["hosts"],
		"summary": norm["summary"], "notes": norm["notes"],
	}
}

// consolePair sends a pairing confirmation request as the chosen controller.
func (s *Server) consolePair(body map[string]any) (int, any) {
	dir, err := s.controllerDir(asString(body["controller"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	targetURL := strings.TrimRight(asString(body["target_url"]), "/")
	if targetURL == "" {
		return 400, map[string]any{"error": "缺少 target_url"}
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	scope := asString(body["scope"])
	if scope != "project" {
		scope = "chat"
	}
	message := asString(body["message"])
	if message == "" {
		message = "lanbus 指挥台请求对话"
	}
	resp, err := RequestPairing(targetURL, id, message, scope, asString(body["cwd"]))
	if err != nil {
		return 502, map[string]any{"error": err.Error()}
	}
	// Company auto-approval returns a grant immediately; cache it.
	if asString(resp["status"]) == "approved" {
		if grant, ok := resp["grant"].(map[string]any); ok {
			if client, err := NewClient(targetURL, dir); err == nil {
				_ = client.SaveGrant(grant)
			}
		}
	}
	resp["target_url"] = targetURL
	resp["controller"] = asString(body["controller"])
	return 200, resp
}

// consolePairStatus polls a pairing request and caches the grant on approval.
func (s *Server) consolePairStatus(body map[string]any) (int, any) {
	dir, err := s.controllerDir(asString(body["controller"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	targetURL := strings.TrimRight(asString(body["target_url"]), "/")
	requestID := asString(body["request_id"])
	if targetURL == "" || requestID == "" {
		return 400, map[string]any{"error": "缺少 target_url 或 request_id"}
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	state, err := PairingStatus(targetURL, requestID, id.PeerID)
	if err != nil {
		return 502, map[string]any{"error": err.Error()}
	}
	if asString(state["status"]) == "approved" {
		if grant, ok := state["grant"].(map[string]any); ok {
			if client, err := NewClient(targetURL, dir); err == nil {
				_ = client.SaveGrant(grant)
			}
		}
	}
	return 200, state
}

// consoleSend transmits one message from the controller to an approved target
// and returns the executor result ({ok, exit_code, reply, peer}).
func (s *Server) consoleSend(body map[string]any) (int, any) {
	dir, err := s.controllerDir(asString(body["controller"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	targetURL := strings.TrimRight(asString(body["target_url"]), "/")
	message := asString(body["message"])
	if targetURL == "" {
		return 400, map[string]any{"error": "缺少 target_url"}
	}
	if strings.TrimSpace(message) == "" {
		return 400, map[string]any{"error": "消息不能为空"}
	}
	scope := asString(body["scope"])
	if scope != "project" {
		scope = "chat"
	}
	// Multi-turn continuity: the target spawns a fresh agent per request, so we
	// prepend the stored conversation history to each new message. The first
	// turn (no history) is sent unchanged, preserving one-shot behavior.
	// Settings are resolved before sending so failed attempts are recorded with
	// the same context/turn-limit the exchange would have used.
	convKey := targetURL
	convs := loadConversations(dir)
	conv, _ := convs[convKey].(map[string]any)
	priorTurns := convTurns(convs, convKey)
	// Effective settings: request overrides stored, stored overrides default.
	// Context is on by default; context:false sends a clean one-shot message.
	// The exchange is recorded either way so history stays complete.
	carry := asBool(body["context"], storedContext(conv))
	maxTurns := int(asFloat(body["max_turns"]))
	if maxTurns <= 0 {
		maxTurns = storedMaxTurns(conv)
	}
	if maxTurns > consoleMaxTurnsHard {
		maxTurns = consoleMaxTurnsHard
	}
	client, err := NewClient(targetURL, dir)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	health, err := JSONRequest(targetURL+"/peer/health", nil, nil, 10)
	if err != nil {
		// Send never reached the target: record the attempt so "sent" is not
		// mistaken for "received", and surface the failure stage.
		recordExchange(dir, convKey, targetURL, scope, message, "", carry, maxTurns,
			map[string]any{"status": "unreachable", "error": err.Error()})
		return 502, map[string]any{"error": err.Error(), "status": "unreachable"}
	}
	grant := client.FindGrant(asString(health["peer_id"]), scope)
	if grant == nil {
		return 409, map[string]any{"error": "尚未配对，请先发送确认请求并等待目标同意", "need_pairing": true}
	}
	timeout := asFloat(body["timeout"])
	if timeout <= 0 {
		timeout = 300
	}
	statusTimeout := asFloat(body["status_timeout"])
	if statusTimeout <= 0 {
		statusTimeout = 600
	}
	outgoing := message
	if carry {
		outgoing = buildContextMessage(priorTurns, message, maxTurns)
	}
	result, err := client.RequestV2(grant, outgoing, scope, asString(body["cwd"]), timeout, statusTimeout)
	if err != nil {
		status := classifySendError(err)
		recordExchange(dir, convKey, targetURL, scope, message, "", carry, maxTurns,
			map[string]any{"status": status, "error": err.Error()})
		return 502, map[string]any{"error": err.Error(), "status": status}
	}
	status := "completed"
	if !asBool(result["ok"], false) {
		status = "failed"
	}
	// Record the clean exchange (original user text, not the context-expanded
	// prompt) so history never compounds prior context blobs, and persist settings.
	// The receipt correlates this turn with the receiver's receipt log.
	receipt := map[string]any{
		"request_id": asString(result["request_id"]),
		"status":     status,
		"exit_code":  result["exit_code"],
	}
	recordExchange(dir, convKey, targetURL, scope, message, asString(result["reply"]), carry, maxTurns, receipt)
	result["conversation_turns"] = PyFloat(float64(len(priorTurns) + 2))
	result["status"] = status
	return 200, result
}

// classifySendError maps a RequestV2 failure to a receipt status so the sender
// can tell denial/expiry/timeout apart from a generic execution failure.
func classifySendError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "拒绝"):
		return "denied"
	case strings.Contains(msg, "过期"):
		return "expired"
	case strings.Contains(msg, "超时"):
		return "timeout"
	default:
		return "failed"
	}
}

// consoleHistory returns the stored conversation turns for a target so the GUI
// can restore the dialog across reloads.
func (s *Server) consoleHistory(body map[string]any) (int, any) {
	dir, err := s.controllerDir(asString(body["controller"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	key := strings.TrimRight(asString(body["target_url"]), "/")
	if key == "" {
		return 400, map[string]any{"error": "缺少 target_url"}
	}
	convs := loadConversations(dir)
	conv, _ := convs[key].(map[string]any)
	turns := convTurns(convs, key)
	if turns == nil {
		turns = []any{}
	}
	return 200, map[string]any{
		"target_url": key, "turns": turns,
		"context": storedContext(conv), "max_turns": PyFloat(float64(storedMaxTurns(conv))),
	}
}

// consoleReset clears a conversation so the next message starts fresh.
func (s *Server) consoleReset(body map[string]any) (int, any) {
	dir, err := s.controllerDir(asString(body["controller"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	key := strings.TrimRight(asString(body["target_url"]), "/")
	if key == "" {
		return 400, map[string]any{"error": "缺少 target_url"}
	}
	convs := loadConversations(dir)
	delete(convs, key)
	if err := saveConversations(dir, convs); err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	return 200, map[string]any{"ok": true}
}

// --- multi-turn conversation store (controller-side, per controller dir) ---

const (
	consoleConvFile     = "console-conversations.json"
	consoleMaxTurns     = 16    // default most recent turns kept in the context prompt
	consoleMaxTurnsHard = 64    // upper bound for a caller-supplied turn limit
	consoleTurnRunes    = 1500  // per-turn truncation inside the context prompt
	consoleContextRunes = 12000 // total history budget in the context prompt
)

func consoleConvPath(dir string) string { return filepath.Join(dir, consoleConvFile) }

func loadConversations(dir string) map[string]any {
	raw, err := os.ReadFile(consoleConvPath(dir))
	if err != nil {
		return map[string]any{}
	}
	obj, err := ParseObject(raw)
	if err != nil {
		return map[string]any{}
	}
	return obj
}

func saveConversations(dir string, convs map[string]any) error {
	return atomicJSON(consoleConvPath(dir), convs, true)
}

func convTurns(convs map[string]any, key string) []any {
	conv, _ := convs[key].(map[string]any)
	if conv == nil {
		return nil
	}
	turns, _ := conv["turns"].([]any)
	return turns
}

func recordExchange(dir, key, targetURL, scope, userText, agentText string, carry bool, maxTurns int, receipt map[string]any) {
	convs := loadConversations(dir)
	conv, _ := convs[key].(map[string]any)
	if conv == nil {
		conv = map[string]any{}
	}
	turns, _ := conv["turns"].([]any)
	ts := now()
	turns = append(turns, map[string]any{"role": "user", "text": userText, "ts": ts})
	agentTurn := map[string]any{"role": "agent", "text": agentText, "ts": ts}
	if receipt != nil {
		agentTurn["receipt"] = receipt
	}
	turns = append(turns, agentTurn)
	conv["target_url"] = targetURL
	conv["scope"] = scope
	conv["turns"] = turns
	conv["context"] = carry
	conv["max_turns"] = PyFloat(float64(maxTurns))
	conv["updated_at"] = ts
	convs[key] = conv
	_ = saveConversations(dir, convs)
}

// storedContext returns a conversation's persisted context toggle (default on).
func storedContext(conv map[string]any) bool {
	if conv == nil {
		return true
	}
	return asBool(conv["context"], true)
}

// storedMaxTurns returns a conversation's persisted turn limit (default 16, clamped).
func storedMaxTurns(conv map[string]any) int {
	if conv == nil {
		return consoleMaxTurns
	}
	n := int(asFloat(conv["max_turns"]))
	if n <= 0 {
		return consoleMaxTurns
	}
	if n > consoleMaxTurnsHard {
		return consoleMaxTurnsHard
	}
	return n
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// buildContextMessage renders prior turns plus the new message into one prompt
// so a freshly spawned target agent sees the conversation. With no history it
// returns the message unchanged. History is bounded by turn count and runes.
func buildContextMessage(turns []any, message string, maxTurns int) string {
	if len(turns) == 0 {
		return message
	}
	if maxTurns <= 0 {
		maxTurns = consoleMaxTurns
	}
	if maxTurns > consoleMaxTurnsHard {
		maxTurns = consoleMaxTurnsHard
	}
	start := len(turns) - maxTurns
	if start < 0 {
		start = 0
	}
	var lines []string
	total := 0
	for _, tv := range turns[start:] {
		t, _ := tv.(map[string]any)
		if t == nil {
			continue
		}
		// Skip failed/empty replies so a broken turn does not pollute context.
		if asString(t["text"]) == "" {
			continue
		}
		label := "操控方"
		if asString(t["role"]) == "agent" {
			label = "目标 agent"
		}
		line := "[" + label + "] " + truncateRunes(asString(t["text"]), consoleTurnRunes)
		lines = append(lines, line)
		total += len([]rune(line))
	}
	// Drop oldest lines until within budget; always keep the newest line.
	for total > consoleContextRunes && len(lines) > 1 {
		total -= len([]rune(lines[0]))
		lines = lines[1:]
	}
	var b strings.Builder
	b.WriteString("# lanbus 指挥台 · 多轮对话上下文\n")
	b.WriteString("你正在与操控方进行多轮对话。请结合下面的历史记录回应「最新消息」。\n\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n## 最新消息\n")
	b.WriteString(message)
	return b.String()
}