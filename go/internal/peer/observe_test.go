package peer

import "testing"

func TestStatusReportRegistrationAndState(t *testing.T) {
	root := t.TempDir()
	s := newConsolePeer(t, root, "host", "pair", nil)
	dir := s.cfg.DataDir

	r, err := StatusReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := r["online"].(bool); !b {
		t.Fatalf("expected online: %v", r)
	}
	if asString(r["state"]) != "idle" {
		t.Fatalf("state = %v, want idle", r["state"])
	}
	if b, _ := r["registered"].(bool); b {
		t.Fatalf("should not be registered before joining a company: %v", r)
	}

	if _, err := CreateCompany(dir, "测试公司"); err != nil {
		t.Fatal(err)
	}
	r, _ = StatusReport(dir)
	if b, _ := r["registered"].(bool); !b {
		t.Fatalf("expected registered after joining company: %v", r)
	}
	if asString(r["company_name"]) != "测试公司" {
		t.Fatalf("company_name = %v", r["company_name"])
	}
}

func TestHistoryAndReceiptsReports(t *testing.T) {
	root := t.TempDir()
	s := newConsolePeer(t, root, "host", "pair", nil)
	dir := s.cfg.DataDir

	recordExchange(dir, "http://target", "http://target", "chat", "hi", "reply", true, 16,
		map[string]any{"status": "completed", "request_id": "r1", "exit_code": 0})
	h, err := HistoryReport(dir, "http://target")
	if err != nil {
		t.Fatal(err)
	}
	turns, _ := h["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	a, _ := turns[1].(map[string]any)
	rc, _ := a["receipt"].(map[string]any)
	if asString(rc["status"]) != "completed" {
		t.Fatalf("history receipt = %v", rc)
	}

	s.recordStage("req-x", StageReceived, "")
	s.recordStage("req-x", StageCompleted, "")
	list := ReceiptsReport(dir)
	if len(list) != 1 || list[0].Status != "completed" {
		t.Fatalf("receipts = %+v", list)
	}
}