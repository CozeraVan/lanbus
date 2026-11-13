package peer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalMatchesPython(t *testing.T) {
	cases := []struct {
		value    map[string]any
		expected string
	}{
		{
			map[string]any{
				"b": json.Number("1"), "a": "中文<>&/\"\\\n\t\x01\u2028", "n": nil, "t": true,
				"f": json.Number("1762940000.1234567"),
				"arr": []any{json.Number("3"), json.Number("1"),
					map[string]any{"z": json.Number("0"), "a": json.Number("1")}},
			},
			"{\"a\":\"中文<>&/\\\"\\\\\\n\\t\\u0001\u2028\",\"arr\":[3,1,{\"a\":1,\"z\":0}],\"b\":1,\"f\":1762940000.1234567,\"n\":null,\"t\":true}",
		},
		{
			map[string]any{
				"created_at": json.Number("1762940000.0"), "timeout": json.Number("300.0"),
				"x": json.Number("1e-05"), "y": json.Number("1e+16"),
				"z": json.Number("0.0001"), "w": json.Number("1.5e+16"),
			},
			"{\"created_at\":1762940000.0,\"timeout\":300.0,\"w\":1.5e+16,\"x\":1e-05,\"y\":1e+16,\"z\":0.0001}",
		},
		{
			map[string]any{"empty": map[string]any{}, "list": []any{}, "s": "plain"},
			"{\"empty\":{},\"list\":[],\"s\":\"plain\"}",
		},
	}
	for i, c := range cases {
		got, err := Canonical(c.value)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if string(got) != c.expected {
			t.Fatalf("case %d:\n got %s\nwant %s", i, got, c.expected)
		}
	}
}

func TestPyFloatFormat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1762940000.1234567, "1762940000.1234567"},
		{300.0, "300"},
		{1e-05, "1e-05"},
		{1e16, "1e+16"},
		{1.5e16, "1.5e+16"},
		{0.0001, "0.0001"},
		{123.456, "123.456"},
	}
	for _, c := range cases {
		if got := PyFloat(c.in).String(); got != c.want {
			t.Errorf("PyFloat(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// pythonScript drives the cross-language interop checks. Phase 1 emits
// vectors; phase 2 (after Go writes its signature) verifies it.
const pythonScript = `
import json, os, sys
sys.path.insert(0, os.path.join(sys.argv[2], "src"))
from lanbus import peer
data_dir = sys.argv[1]
idA = peer.Identity.load(os.path.join(data_dir, "initiator"))
idB = peer.Identity.load(os.path.join(data_dir, "target"))
payload = {"message": "中文<>&\"\\\n\t\x01\u2028", "scope": "chat",
           "created_at": 1762940000.1234567, "timeout": 300.0,
           "tiny": 1e-05, "big": 1e16}
sig = idA.sign(payload)
n1 = peer._b64(os.urandom(24)); n2 = peer._b64(os.urandom(24))
keyA = idA.session_key(idB.x_public_b64, n1, n2)
out = {"peer_id": idA.peer_id, "sign_public": idA.sign_public_b64,
       "x_public": idA.x_public_b64, "signature": sig, "payload": payload,
       "target_peer_id": idB.peer_id, "target_x_public": idB.x_public_b64,
       "n1": n1, "n2": n2, "keyA": keyA.hex()}
with open(os.path.join(data_dir, "vectors.json"), "w", encoding="utf-8") as fh:
    json.dump(out, fh, ensure_ascii=False)
go_sig_path = os.path.join(data_dir, "go_signature.txt")
if os.path.exists(go_sig_path):
    go_sig = open(go_sig_path).read().strip()
    ok = peer.Identity.verify(idA.sign_public_b64, payload, go_sig)
    results = []
    rt_path = os.path.join(data_dir, "pyfloat_roundtrip.json")
    if os.path.exists(rt_path):
        for lit in json.load(open(rt_path)):
            results.append(peer.canonical({"v": json.loads(lit)}).decode("utf-8"))
    with open(os.path.join(data_dir, "verify_result.json"), "w") as fh:
        json.dump({"go_signature_ok": ok, "roundtrip": results}, fh)
`

func TestPythonInterop(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not available")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "src", "lanbus", "peer.py")); err != nil {
		t.Skip("python source tree not found")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "interop.py")
	if err := os.WriteFile(script, []byte(pythonScript), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() {
		cmd := exec.Command(python, script, dir, repoRoot)
		cmd.Env = append(os.Environ(), "PYTHONUTF8=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("python interop: %v\n%s", err, out)
		}
	}
	run() // phase 1
	raw, err := os.ReadFile(filepath.Join(dir, "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := ParseObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Go loads the Python-generated target identity (PKCS8 PEM compatibility).
	idB, err := LoadIdentity(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatalf("load python identity: %v", err)
	}
	if idB.PeerID != asString(vectors["target_peer_id"]) {
		t.Fatalf("peer_id mismatch: %s vs %s", idB.PeerID, vectors["target_peer_id"])
	}
	// Go verifies the Python signature over the tricky payload.
	payload, _ := vectors["payload"].(map[string]any)
	if !VerifySignature(asString(vectors["sign_public"]), payload, asString(vectors["signature"])) {
		t.Fatal("Go failed to verify Python signature")
	}
	// ECDH + HKDF key agreement must match byte for byte.
	keyB, err := idB.SessionKey(asString(vectors["x_public"]), asString(vectors["n1"]), asString(vectors["n2"]))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", keyB) != asString(vectors["keyA"]) {
		t.Fatalf("session key mismatch:\n go %x\n py %s", keyB, vectors["keyA"])
	}
	// Go signs the same payload; Python must verify it.
	goSig, err := idB.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	// Note: signature is by idB but Python verifies with idA's public key only
	// when the signer matches; sign with a Go-loaded idA instead.
	idA, err := LoadIdentity(filepath.Join(dir, "initiator"))
	if err != nil {
		t.Fatal(err)
	}
	goSig, err = idA.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go_signature.txt"), []byte(goSig), 0o644); err != nil {
		t.Fatal(err)
	}
	// PyFloat literals must round-trip through Python's canonical unchanged.
	floats := []float64{1762940000.1234567, 300.0, 1e-05, 1e16, 0.0001, 1.5e16, 123.456}
	literals := []string{}
	expected := []string{}
	for _, f := range floats {
		lit := PyFloat(f).String()
		literals = append(literals, lit)
		raw, err := Canonical(map[string]any{"v": json.Number(lit)})
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, string(raw))
	}
	litRaw, _ := json.Marshal(literals)
	if err := os.WriteFile(filepath.Join(dir, "pyfloat_roundtrip.json"), litRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	run() // phase 2
	raw, err = os.ReadFile(filepath.Join(dir, "verify_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ParseObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !asBool(result["go_signature_ok"], false) {
		t.Fatal("Python failed to verify Go signature")
	}
	roundtrip, _ := result["roundtrip"].([]any)
	if len(roundtrip) != len(expected) {
		t.Fatalf("roundtrip length %d != %d", len(roundtrip), len(expected))
	}
	for i, item := range roundtrip {
		if asString(item) != expected[i] {
			t.Fatalf("float roundtrip %d:\n py %s\n go %s", i, asString(item), expected[i])
		}
	}
}

func testServer(t *testing.T, executor ExecutorFunc) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewServer(ServerConfig{
		Name: "go-test", Agent: "echo", CWD: dir, DataDir: filepath.Join(dir, "peer"),
		Host: "127.0.0.1", Port: 0, Executor: executor, RequestPolicy: "pair",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	secretRaw, err := os.ReadFile(filepath.Join(dir, "peer", "approval.json"))
	if err != nil {
		t.Fatal(err)
	}
	secretObj, _ := ParseObject(secretRaw)
	return s, asString(secretObj["secret"])
}

func TestDirectFlow(t *testing.T) {
	s, secret := testServer(t, func(message, scope, cwd string, timeout float64) map[string]any {
		return map[string]any{"ok": true, "exit_code": 0, "reply": "echo:" + message, "peer": "go-test"}
	})
	clientDir := t.TempDir()
	client, err := NewClient(s.URL, clientDir)
	if err != nil {
		t.Fatal(err)
	}
	// health
	health, err := JSONRequest(s.URL+"/peer/health", nil, nil, 5)
	if err != nil || !asBool(health["direct_pairing"], false) || asString(health["peer_id"]) != s.Identity.PeerID {
		t.Fatalf("health: %v %v", health, err)
	}
	// pair
	pending, err := RequestPairing(s.URL, client.Identity, "跨语言测试", "chat", "")
	if err != nil || asString(pending["status"]) != "pending" {
		t.Fatalf("pair: %v %v", pending, err)
	}
	requestID := asString(pending["request_id"])
	// inbox requires loopback + secret
	if _, err := JSONRequest(s.URL+"/peer/inbox", nil, nil, 5); err == nil || !strings.Contains(err.Error(), "只能在目标机器本机审批") {
		t.Fatalf("inbox without secret: %v", err)
	}
	headers := map[string]string{"X-Lanbus-Approval": secret}
	inbox, err := JSONRequest(s.URL+"/peer/inbox", nil, headers, 5)
	if err != nil || len(inbox["requests"].([]any)) != 1 {
		t.Fatalf("inbox: %v %v", inbox, err)
	}
	// approve
	approved, err := JSONRequest(s.URL+"/peer/approve", map[string]any{
		"request_id": requestID, "mode": "pair", "scope": "chat",
	}, headers, 5)
	if err != nil || asString(approved["status"]) != "approved" {
		t.Fatalf("approve: %v %v", approved, err)
	}
	grant, _ := approved["grant"].(map[string]any)
	if err := client.SaveGrant(grant); err != nil {
		t.Fatal(err)
	}
	// status reflects approval
	status, err := PairingStatus(s.URL, requestID, client.Identity.PeerID)
	if err != nil || asString(status["status"]) != "approved" {
		t.Fatalf("status: %v %v", status, err)
	}
	// encrypted request round trip (unicode message)
	result, err := client.Request(grant, "你好，世界", "chat", "", 30)
	if err != nil || asString(result["reply"]) != "echo:你好，世界" {
		t.Fatalf("request: %v %v", result, err)
	}
	// scope escalation denied
	if _, err := client.Request(grant, "x", "project", "", 30); err == nil || !strings.Contains(err.Error(), "请求 scope 超出授权范围") {
		t.Fatalf("scope escalation: %v", err)
	}
	// replay denied: reuse a request_id with a fresh packet
	key, err := client.Identity.SessionKey(asString(grant["target_x_public"]),
		asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
	if err != nil {
		t.Fatal(err)
	}
	replayID := randomHex(16)
	aad := asString(grant["grant_id"]) + ":" + replayID
	packet, err := EncryptPacket(key, map[string]any{"message": "one", "scope": "chat", "cwd": "", "timeout": PyFloat(30)}, aad)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"grant_id": asString(grant["grant_id"]), "request_id": replayID, "packet": packet}
	if _, err := JSONRequest(s.URL+"/peer/request", body, nil, 30); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, err := JSONRequest(s.URL+"/peer/request", body, nil, 30); err == nil || !strings.Contains(err.Error(), "重复请求已拒绝") {
		t.Fatalf("replay: %v", err)
	}
	// revoke kills the grant
	revoked, err := JSONRequest(s.URL+"/peer/revoke", map[string]any{"peer_id": client.Identity.PeerID}, headers, 5)
	if err != nil || !asBool(revoked["ok"], false) {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	if _, err := client.Request(grant, "x", "chat", "", 30); err == nil || !strings.Contains(err.Error(), "授权不存在、已过期或已撤销") {
		t.Fatalf("after revoke: %v", err)
	}
	// session grant stays in memory only
	pending2, err := RequestPairing(s.URL, client.Identity, "会话测试", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	approved2, err := JSONRequest(s.URL+"/peer/approve", map[string]any{
		"request_id": asString(pending2["request_id"]), "mode": "session", "scope": "chat",
	}, headers, 5)
	if err != nil {
		t.Fatal(err)
	}
	grant2, _ := approved2["grant"].(map[string]any)
	if _, err := client.Request(grant2, "会话内", "chat", "", 30); err != nil {
		t.Fatalf("session request: %v", err)
	}
	savedRaw, _ := os.ReadFile(filepath.Join(s.cfg.DataDir, "grants.json"))
	if strings.Contains(string(savedRaw), asString(grant2["grant_id"])) {
		t.Fatal("session grant leaked to disk")
	}
	// deny flow
	pending3, err := RequestPairing(s.URL, client.Identity, "拒绝测试", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := JSONRequest(s.URL+"/peer/deny", map[string]any{"request_id": asString(pending3["request_id"])}, headers, 5); err != nil {
		t.Fatal(err)
	}
	status3, err := PairingStatus(s.URL, asString(pending3["request_id"]), client.Identity.PeerID)
	if err != nil || asString(status3["status"]) != "denied" {
		t.Fatalf("deny status: %v %v", status3, err)
	}
}

func TestPairRateLimit(t *testing.T) {
	s, _ := testServer(t, nil)
	dir := t.TempDir()
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for i := 0; i < 6; i++ {
		_, lastErr = RequestPairing(s.URL, id, fmt.Sprintf("申请%d", i), "chat", "")
		if i < 5 && lastErr != nil {
			t.Fatalf("pair %d: %v", i, lastErr)
		}
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "配对申请频率过高") {
		t.Fatalf("expected rate limit, got %v", lastErr)
	}
}

func TestHealthAdvertisesCallerEndpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := NewServer(ServerConfig{
		Name: "advertise", Agent: "echo", CWD: dir, DataDir: filepath.Join(dir, "peer"),
		Host: "0.0.0.0", Port: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	defer s.Stop()
	req, _ := http.NewRequest("GET", s.URL+"/peer/health", nil)
	req.Host = "192.168.1.236:8790"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health map[string]any
	json.NewDecoder(resp.Body).Decode(&health)
	if asString(health["endpoint"]) != "http://192.168.1.236:8790" {
		t.Fatalf("endpoint = %v", health["endpoint"])
	}
}
