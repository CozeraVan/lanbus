package peer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiptStoreRecordsLifecycleAndPersists(t *testing.T) {
	dir := t.TempDir()
	rs := LoadReceipts(dir)
	rs.Update("req-1", func(r *Receipt) {
		r.PeerID = "peer-a"
		r.Scope = "chat"
		r.Message = "hello"
		r.addStage("received", "")
		r.addStage("approval_required", "")
	})
	rs.Update("req-1", func(r *Receipt) {
		r.addStage("approved", "")
		r.addStage("executing", "")
		r.addStage("completed", "")
		r.ExitCode = 0
	})

	list := rs.List()
	if len(list) != 1 {
		t.Fatalf("receipt count = %d, want 1", len(list))
	}
	got := list[0]
	if got.RequestID != "req-1" || got.Status != "completed" || got.PeerID != "peer-a" {
		t.Fatalf("receipt = %+v", got)
	}
	if len(got.Stages) != 5 {
		t.Fatalf("stages = %d, want 5 (%+v)", len(got.Stages), got.Stages)
	}
	if got.Stages[0].Stage != "received" || got.Stages[4].Stage != "completed" {
		t.Fatalf("stage order = %+v", got.Stages)
	}

	// A fresh store loaded from disk must see the persisted receipt.
	reloaded := LoadReceipts(dir)
	again := reloaded.List()
	if len(again) != 1 || again[0].Status != "completed" || len(again[0].Stages) != 5 {
		t.Fatalf("persisted receipt = %+v", again)
	}
	if _, err := filepath.Abs(filepath.Join(dir, "receipts.json")); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptStoreRecordsFailure(t *testing.T) {
	rs := LoadReceipts(t.TempDir())
	rs.Update("req-fail", func(r *Receipt) {
		r.addStage("received", "")
		r.addStage("executing", "")
		r.addStage("failed", "exit_code 1")
		r.ExitCode = 1
		r.Error = "agent crashed"
	})
	got := rs.List()[0]
	if got.Status != "failed" || got.ExitCode != 1 || got.Error != "agent crashed" {
		t.Fatalf("failure receipt = %+v", got)
	}
}

func TestReceiptStoreIsBoundedNewestFirst(t *testing.T) {
	rs := LoadReceipts(t.TempDir())
	for i := 0; i < receiptMax+25; i++ {
		id := "req-" + string(rune('A'+i%26)) + "-" + itoa(i)
		rs.Update(id, func(r *Receipt) { r.addStage("received", "") })
	}
	list := rs.List()
	if len(list) != receiptMax {
		t.Fatalf("bounded count = %d, want %d", len(list), receiptMax)
	}
	// Newest first: the last written id must be at the front.
	lastID := "req-" + string(rune('A'+(receiptMax+24)%26)) + "-" + itoa(receiptMax+24)
	if list[0].RequestID != lastID {
		t.Fatalf("newest first: got %q want %q", list[0].RequestID, lastID)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// hasStages reports whether every named stage appears in order in the receipt.
func hasStages(r Receipt, names ...string) bool {
	i := 0
	for _, s := range r.Stages {
		if i < len(names) && s.Stage == names[i] {
			i++
		}
	}
	return i == len(names)
}

func receiptWithStage(list []Receipt, stage string) *Receipt {
	for i := range list {
		for _, s := range list[i].Stages {
			if s.Stage == stage {
				return &list[i]
			}
		}
	}
	return nil
}

func TestServerReceiptsV2LifecycleAndEndpoint(t *testing.T) {
	notices := []Notification{}
	s, secret := approvalTestServer(t, func(message, scope, cwd string, timeout float64) map[string]any {
		return map[string]any{"ok": true, "exit_code": 0, "reply": "done"}
	}, &notices)
	client, _ := NewClient(s.URL, t.TempDir())
	grant := pairForRequestV2(t, s, secret, client)

	resultCh := make(chan map[string]any, 1)
	go func() { r, _ := client.RequestV2(grant, "做点事", "chat", "", 30, 5); resultCh <- r }()
	headers := map[string]string{"X-Lanbus-Approval": secret}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		inbox, _ := JSONRequest(s.URL+"/peer/inbox", nil, headers, 5)
		for _, raw := range inbox["requests"].([]any) {
			item, _ := raw.(map[string]any)
			if asString(item["kind"]) != "execution" {
				continue
			}
			if _, err := JSONRequest(s.URL+"/peer/approve", map[string]any{"request_id": asString(item["request_id"])}, headers, 5); err != nil {
				t.Fatal(err)
			}
			<-resultCh
			r := receiptWithStage(s.receipts.List(), "executing")
			if r == nil {
				t.Fatalf("no execution receipt in %+v", s.receipts.List())
			}
			if r.Status != "completed" || !hasStages(*r, "received", "approval_required", "approved", "executing", "completed") {
				t.Fatalf("lifecycle receipt = %+v", r)
			}
			resp, err := JSONRequest(s.URL+"/peer/receipts", nil, headers, 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp["receipts"].([]any)) == 0 {
				t.Fatal("/peer/receipts returned no receipts")
			}
			health, _ := JSONRequest(s.URL+"/peer/health", nil, nil, 5)
			if asString(health["state"]) != "idle" {
				t.Fatalf("health state = %q, want idle", health["state"])
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("execution approval did not arrive")
}

func TestServerReceiptsInlineCompletionAndFailure(t *testing.T) {
	notices := []Notification{}
	dir := t.TempDir()
	s, err := NewServer(ServerConfig{
		Name: "inline", Agent: "echo", CWD: dir, DataDir: filepath.Join(dir, "peer"),
		Host: "127.0.0.1", Port: 0, RequestPolicy: "pair",
		Executor: func(m, sc, cw string, to float64) map[string]any {
			if strings.Contains(m, "boom") {
				return map[string]any{"ok": false, "exit_code": 2, "reply": "exploded"}
			}
			return map[string]any{"ok": true, "exit_code": 0, "reply": "ok"}
		},
		Notifier: func(n Notification) { notices = append(notices, n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	secretRaw, _ := os.ReadFile(filepath.Join(dir, "peer", "approval.json"))
	secretObj, _ := ParseObject(secretRaw)
	secret := asString(secretObj["secret"])
	client, _ := NewClient(s.URL, t.TempDir())
	grant := pairForRequestV2(t, s, secret, client)

	if _, err := client.Request(grant, "你好", "chat", "", 30); err != nil {
		t.Fatal(err)
	}
	failRes, err := client.Request(grant, "boom", "chat", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if asBool(failRes["ok"], true) {
		t.Fatalf("expected ok=false result, got %v", failRes)
	}

	list := s.receipts.List()
	completed := receiptWithStage(list, "completed")
	failed := receiptWithStage(list, "failed")
	if completed == nil || failed == nil {
		t.Fatalf("want completed+failed receipts, got %+v", list)
	}
	if failed.ExitCode != 2 {
		t.Fatalf("failed exit_code = %d, want 2", failed.ExitCode)
	}
	if !hasStages(*completed, "received", "executing", "completed") {
		t.Fatalf("inline completed stages = %+v", completed.Stages)
	}
}