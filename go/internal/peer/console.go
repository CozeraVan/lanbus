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
	client, err := NewClient(targetURL, dir)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	health, err := JSONRequest(targetURL+"/peer/health", nil, nil, 10)
	if err != nil {
		return 502, map[string]any{"error": err.Error()}
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
	// Multi-turn continuity: the target spawns a fresh agent per request, so we
	// prepend the stored conversation history to each new message. The first
	// turn (no history) is sent unchanged, preserving one-shot behavior.
	convKey := targetURL
	priorTurns := convTurns(loadConversations(dir), convKey)
	outgoing := buildContextMessage(priorTurns, message)
	result, err := client.RequestV2(grant, outgoing, scope, asString(body["cwd"]), timeout, statusTimeout)
	if err != nil {
		return 502, map[string]any{"error": err.Error()}
	}
	// Record the clean exchange (original user text, not the context-expanded
	// prompt) so history never compounds prior context blobs.
	recordExchange(dir, convKey, targetURL, scope, message, asString(result["reply"]))
	result["conversation_turns"] = PyFloat(float64(len(priorTurns) + 2))
	return 200, result
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
	turns := convTurns(loadConversations(dir), key)
	if turns == nil {
		turns = []any{}
	}
	return 200, map[string]any{"target_url": key, "turns": turns}
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
	consoleMaxTurns     = 16    // most recent turns kept in the context prompt
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

func recordExchange(dir, key, targetURL, scope, userText, agentText string) {
	convs := loadConversations(dir)
	conv, _ := convs[key].(map[string]any)
	if conv == nil {
		conv = map[string]any{}
	}
	turns, _ := conv["turns"].([]any)
	ts := now()
	turns = append(turns, map[string]any{"role": "user", "text": userText, "ts": ts})
	turns = append(turns, map[string]any{"role": "agent", "text": agentText, "ts": ts})
	conv["target_url"] = targetURL
	conv["scope"] = scope
	conv["turns"] = turns
	conv["updated_at"] = ts
	convs[key] = conv
	_ = saveConversations(dir, convs)
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
func buildContextMessage(turns []any, message string) string {
	if len(turns) == 0 {
		return message
	}
	start := len(turns) - consoleMaxTurns
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