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
	result, err := client.RequestV2(grant, message, scope, asString(body["cwd"]), timeout, statusTimeout)
	if err != nil {
		return 502, map[string]any{"error": err.Error()}
	}
	return 200, result
}