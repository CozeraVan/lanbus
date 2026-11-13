package peer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// consoleReq issues a loopback console request with the approval secret header.
func consoleReq(t *testing.T, method, url, secret string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Lanbus-Approval", secret)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func newConsolePeer(t *testing.T, root, name, policy string, exec ExecutorFunc) *Server {
	t.Helper()
	s, err := NewServer(ServerConfig{
		Name: name, DataDir: filepath.Join(root, "peers", name),
		Port: 0, RequestPolicy: policy, Executor: exec,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	return s
}

func TestConsoleLocalsListsPeers(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	target := newConsolePeer(t, root, "target", "pair", nil)
	_ = target

	code, out := consoleReq(t, "GET", host.URL+"/peer/console/locals", host.approvalSecret, nil)
	if code != 200 {
		t.Fatalf("locals code=%d out=%v", code, out)
	}
	locals, _ := out["locals"].([]any)
	names := map[string]bool{}
	for _, l := range locals {
		m, _ := l.(map[string]any)
		names[asString(m["name"])] = true
	}
	if !names["host"] || !names["target"] {
		t.Fatalf("locals missing host/target: %v", out)
	}
}

func TestConsoleAuthRequired(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	// No secret header → 403.
	code, _ := consoleReq(t, "GET", host.URL+"/peer/console/locals", "", nil)
	if code != 403 {
		t.Fatalf("expected 403 without secret, got %d", code)
	}
	// Wrong secret → 403.
	code, _ = consoleReq(t, "GET", host.URL+"/peer/console/locals", "wrong", nil)
	if code != 403 {
		t.Fatalf("expected 403 with wrong secret, got %d", code)
	}
}

func TestConsoleRejectsBadController(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	for _, bad := range []string{"../evil", "a/b", "..", ""} {
		code, out := consoleReq(t, "POST", host.URL+"/peer/console/pair", host.approvalSecret,
			map[string]any{"controller": bad, "target_url": "http://127.0.0.1:1", "message": "hi"})
		if code != 400 {
			t.Fatalf("controller %q: expected 400, got %d (%v)", bad, code, out)
		}
	}
}

// TestConsolePairApproveSend walks the full GUI flow: discover local peers,
// send a pairing confirmation request, the target human approves, then the
// controller sends a message and receives the reply.
func TestConsolePairApproveSend(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": "target"}
	})

	// 1. Pairing confirmation request from controller "host" to target.
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/pair", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "请求对话", "scope": "chat"})
	if code != 200 {
		t.Fatalf("pair code=%d out=%v", code, out)
	}
	requestID := asString(out["request_id"])
	if asString(out["status"]) != "pending" || requestID == "" {
		t.Fatalf("expected pending request_id, got %v", out)
	}

	// 2. Target human approves (loopback + target secret), as /peer/ui does.
	code, out = consoleReq(t, "POST", target.URL+"/peer/approve", target.approvalSecret,
		map[string]any{"request_id": requestID, "mode": "pair", "scope": "chat"})
	if code != 200 || asString(out["status"]) != "approved" {
		t.Fatalf("approve code=%d out=%v", code, out)
	}

	// 3. Controller polls status; grant is cached on approval.
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/pair-status", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "request_id": requestID})
	if code != 200 || asString(out["status"]) != "approved" {
		t.Fatalf("pair-status code=%d out=%v", code, out)
	}

	// 4. Send a message through the dialog; reply comes back.
	code, out = consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	if code != 200 {
		t.Fatalf("send code=%d out=%v", code, out)
	}
	if ok, _ := out["ok"].(bool); !ok || asString(out["reply"]) != "echo:hello" {
		t.Fatalf("unexpected send result: %v", out)
	}
}

func TestConsoleSendWithoutPairing(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	target := newConsolePeer(t, root, "target", "pair", func(message, scope, cwd string, timeout float64) map[string]any {
		return map[string]any{"ok": true, "reply": "x", "peer": "target"}
	})
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/send", host.approvalSecret,
		map[string]any{"controller": "host", "target_url": target.URL, "message": "hello", "scope": "chat"})
	if code != 409 {
		t.Fatalf("expected 409 need_pairing, got %d (%v)", code, out)
	}
	if need, _ := out["need_pairing"].(bool); !need {
		t.Fatalf("expected need_pairing flag: %v", out)
	}
}

func TestConsolePageAndServe(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	resp, err := http.Get(host.URL + "/peer/console")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "lanbus") {
		t.Fatalf("console page code=%d len=%d", resp.StatusCode, len(raw))
	}
}

func TestConsoleDiscover(t *testing.T) {
	root := t.TempDir()
	host := newConsolePeer(t, root, "host", "pair", nil)
	code, out := consoleReq(t, "POST", host.URL+"/peer/console/discover", host.approvalSecret,
		map[string]any{"cidr": "127.0.0.1/32", "ports": "8790", "timeout": 0.05,
			"http_timeout": 0.2, "include_local": false, "include_processes": false})
	if code != 200 {
		t.Fatalf("discover code=%d out=%v", code, out)
	}
	if _, ok := out["agents"]; !ok {
		t.Fatalf("discover missing agents: %v", out)
	}
}