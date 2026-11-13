package peer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func companyServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	company, err := CreateCompany(filepath.Join(dir, "peer"), "测试公司")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(ServerConfig{Name: "company-node", Agent: "echo", CWD: dir,
		DataDir: filepath.Join(dir, "peer"), Host: "127.0.0.1", Port: 0,
		RequestPolicy: "company", Executor: func(message, scope, cwd string, timeout float64) map[string]any {
			return map[string]any{"ok": true, "reply": message}
		}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Company == nil || s.Company.ID != company.ID {
		t.Fatal("company not loaded")
	}
	s.Start()
	t.Cleanup(s.Stop)
	return s, secretForTest(t, filepath.Join(dir, "peer"))
}

func TestCompanyMemberAutoPairs(t *testing.T) {
	s, _ := companyServer(t)
	client, _ := NewClient(s.URL, t.TempDir())
	if err := SaveCompany(client.DataDir, s.Company); err != nil {
		t.Fatal(err)
	}
	response, err := client.RequestCompany(s.Company, "自由交流", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	grant, _ := response["grant"].(map[string]any)
	if asString(grant["mode"]) != "company" {
		t.Fatalf("grant: %v", grant)
	}
	result, err := client.Request(grant, "hello", "chat", "", 30)
	if err != nil || asString(result["reply"]) != "hello" {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestCompanyJoinNeedsApproval(t *testing.T) {
	s, secret := companyServer(t)
	client, _ := NewClient(s.URL, t.TempDir())
	type outcome struct {
		company *Company
		err     error
	}
	ch := make(chan outcome, 1)
	go func() { company, err := client.JoinCompany("申请加入", "chat", "", 5); ch <- outcome{company, err} }()
	headers := map[string]string{"X-Lanbus-Approval": secret}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inbox, err := JSONRequest(s.URL+"/peer/inbox", nil, headers, 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range inbox["requests"].([]any) {
			item, _ := raw.(map[string]any)
			if asString(item["kind"]) == "company" {
				if _, err := JSONRequest(s.URL+"/peer/approve", map[string]any{"request_id": asString(item["request_id"])}, headers, 5); err != nil {
					t.Fatal(err)
				}
				result := <-ch
				if result.err != nil || result.company.ID != s.Company.ID {
					t.Fatalf("join: %+v", result)
				}
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("company join approval did not arrive")
}

func TestWrongCompanyProofRejected(t *testing.T) {
	s, _ := companyServer(t)
	client, _ := NewClient(s.URL, t.TempDir())
	bad := &Company{ID: s.Company.ID, Name: s.Company.Name, Secret: strings.Repeat("00", 32)}
	if _, err := client.RequestCompany(bad, "冒充", "chat", ""); err == nil || !strings.Contains(err.Error(), "公司成员证明无效") {
		t.Fatalf("wrong proof: %v", err)
	}
}

func secretForTest(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "approval.json"))
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := ParseObject(raw)
	return asString(obj["secret"])
}
