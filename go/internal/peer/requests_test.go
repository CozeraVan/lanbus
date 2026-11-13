package peer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func approvalTestServer(t *testing.T, executor ExecutorFunc, notices *[]Notification) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewServer(ServerConfig{
		Name: "go-window", DisplayName: "窗口 2", InstanceLabel: "frontend",
		Agent: "echo", CWD: dir, DataDir: filepath.Join(dir, "peer"),
		Host: "127.0.0.1", Port: 0, RequestPolicy: "each", Executor: executor,
		Notifier: func(n Notification) { *notices = append(*notices, n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	secretRaw, _ := os.ReadFile(filepath.Join(dir, "peer", "approval.json"))
	secretObj, _ := ParseObject(secretRaw)
	return s, asString(secretObj["secret"])
}

func pairForRequestV2(t *testing.T, s *Server, secret string, client *Client) map[string]any {
	t.Helper()
	pending, err := RequestPairing(s.URL, client.Identity, "pair", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := JSONRequest(s.URL+"/peer/approve", map[string]any{
		"request_id": asString(pending["request_id"]), "mode": "pair", "scope": "chat",
	}, map[string]string{"X-Lanbus-Approval": secret}, 5)
	if err != nil {
		t.Fatal(err)
	}
	grant, _ := approved["grant"].(map[string]any)
	return grant
}

func TestRequestV2RequiresApprovalAndRunsOnce(t *testing.T) {
	var calls atomic.Int32
	notices := []Notification{}
	s, secret := approvalTestServer(t, func(message, scope, cwd string, timeout float64) map[string]any {
		calls.Add(1)
		return map[string]any{"ok": true, "reply": message}
	}, &notices)
	client, err := NewClient(s.URL, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	grant := pairForRequestV2(t, s, secret, client)
	health, _ := JSONRequest(s.URL+"/peer/health", nil, nil, 5)
	if asString(health["display_name"]) != "窗口 2" || asString(health["instance_label"]) != "frontend" {
		t.Fatalf("identity metadata: %v", health)
	}
	resultCh := make(chan map[string]any, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := client.RequestV2(grant, "第二次也批准", "chat", "", 30, 5)
		resultCh <- result
		errCh <- err
	}()
	headers := map[string]string{"X-Lanbus-Approval": secret}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inbox, err := JSONRequest(s.URL+"/peer/inbox", nil, headers, 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range inbox["requests"].([]any) {
			item, _ := raw.(map[string]any)
			if asString(item["kind"]) == "execution" {
				if calls.Load() != 0 {
					t.Fatal("executor ran before approval")
				}
				_, err = JSONRequest(s.URL+"/peer/approve", map[string]any{
					"request_id": asString(item["request_id"]), "kind": "execution",
				}, headers, 5)
				if err != nil {
					t.Fatal(err)
				}
				result := <-resultCh
				if err := <-errCh; err != nil {
					t.Fatal(err)
				}
				if asString(result["reply"]) != "第二次也批准" || calls.Load() != 1 {
					t.Fatalf("result=%v calls=%d", result, calls.Load())
				}
				if len(notices) == 0 {
					t.Fatal("missing desktop notification")
				}
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("execution approval did not arrive")
}

func TestRequestV2DenialDoesNotExecute(t *testing.T) {
	var calls atomic.Int32
	notices := []Notification{}
	s, secret := approvalTestServer(t, func(message, scope, cwd string, timeout float64) map[string]any {
		calls.Add(1)
		return map[string]any{"ok": true}
	}, &notices)
	client, _ := NewClient(s.URL, t.TempDir())
	grant := pairForRequestV2(t, s, secret, client)
	errCh := make(chan error, 1)
	go func() { _, err := client.RequestV2(grant, "不允许", "chat", "", 30, 5); errCh <- err }()
	headers := map[string]string{"X-Lanbus-Approval": secret}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		inbox, _ := JSONRequest(s.URL+"/peer/inbox", nil, headers, 5)
		for _, raw := range inbox["requests"].([]any) {
			item, _ := raw.(map[string]any)
			if asString(item["kind"]) == "execution" {
				_, err := JSONRequest(s.URL+"/peer/deny", map[string]any{"request_id": asString(item["request_id"])}, headers, 5)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-errCh; err == nil || !strings.Contains(err.Error(), "拒绝") {
					t.Fatalf("denial error: %v", err)
				}
				if calls.Load() != 0 {
					t.Fatal("denied request executed")
				}
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("execution approval did not arrive"))
}

func TestRequestV2CachesResultAndBindsDigest(t *testing.T) {
	var calls atomic.Int32
	notices := []Notification{}
	s, secret := approvalTestServer(t, func(message, scope, cwd string, timeout float64) map[string]any {
		calls.Add(1)
		return map[string]any{"ok": true, "reply": "once"}
	}, &notices)
	client, _ := NewClient(s.URL, t.TempDir())
	grant := pairForRequestV2(t, s, secret, client)
	key, err := client.Identity.SessionKey(asString(grant["target_x_public"]),
		asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
	if err != nil {
		t.Fatal(err)
	}
	requestID := "cached-request"
	aad := asString(grant["grant_id"]) + ":" + requestID
	packet, err := EncryptPacket(key, map[string]any{
		"message": "run once", "scope": "chat", "cwd": "", "timeout": PyFloat(30),
	}, aad)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"grant_id": asString(grant["grant_id"]), "request_id": requestID,
		"request_version": PyFloat(2), "packet": packet}
	pending, err := JSONRequest(s.URL+"/peer/request", body, nil, 5)
	if err != nil || asString(pending["status"]) != "approval_required" {
		t.Fatalf("pending: %v %v", pending, err)
	}
	if calls.Load() != 0 {
		t.Fatal("executor ran before approval")
	}
	headers := map[string]string{"X-Lanbus-Approval": secret}
	if _, err := JSONRequest(s.URL+"/peer/approve", map[string]any{"request_id": requestID}, headers, 5); err != nil {
		t.Fatal(err)
	}
	first, err := JSONRequest(s.URL+"/peer/request", body, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := JSONRequest(s.URL+"/peer/request", body, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if asString(first["packet"].(map[string]any)["ciphertext"]) != asString(second["packet"].(map[string]any)["ciphertext"]) {
		t.Fatal("completed response was not cached")
	}
	if calls.Load() != 1 {
		t.Fatalf("executor calls=%d", calls.Load())
	}
	changed := copyGrant(body)
	changedPacket := copyGrant(packet)
	changedPacket["nonce"] = "AA=="
	changed["packet"] = changedPacket
	if _, err := JSONRequest(s.URL+"/peer/request", changed, nil, 5); err == nil || !strings.Contains(err.Error(), "内容已改变") {
		t.Fatalf("changed digest: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("changed request executed")
	}
}
