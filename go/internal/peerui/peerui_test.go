package peerui

import (
	"os"
	"path/filepath"
	"testing"
)

func writePeer(t *testing.T, dir, rawURL, secret string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	peerJSON := `{"name":"go-agent","url":"` + rawURL + `","port":8791}`
	if err := os.WriteFile(filepath.Join(dir, "peer.json"), []byte(peerJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	approvalJSON := `{"secret":"` + secret + `"}`
	if err := os.WriteFile(filepath.Join(dir, "approval.json"), []byte(approvalJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalURL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-agent")
	writePeer(t, dir, "http://127.0.0.1:8791", "s3cr3t")
	got, err := ApprovalURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8791/peer/ui#secret=s3cr3t"
	if got != want {
		t.Fatalf("ApprovalURL() = %q, want %q", got, want)
	}
}

func TestApprovalURLNormalizesWhitespaceAndTrailingSlash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-agent")
	writePeer(t, dir, "  http://127.0.0.1:8791/  ", "s3cr3t")
	got, err := ApprovalURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8791/peer/ui#secret=s3cr3t"
	if got != want {
		t.Fatalf("ApprovalURL() = %q, want %q", got, want)
	}
}

func TestApprovalURLRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		secret string
	}{
		{name: "empty url", rawURL: "", secret: "s3cr3t"},
		{name: "empty secret", rawURL: "http://127.0.0.1:8791", secret: ""},
		{name: "https", rawURL: "https://127.0.0.1:8791", secret: "s3cr3t"},
		{name: "remote host", rawURL: "http://192.168.1.2:8791", secret: "s3cr3t"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "go-agent")
			writePeer(t, dir, test.rawURL, test.secret)
			if _, err := ApprovalURL(dir); err == nil {
				t.Fatal("ApprovalURL() expected error")
			}
		})
	}
	if _, err := ApprovalURL(t.TempDir()); err == nil {
		t.Fatal("ApprovalURL() expected missing files to fail")
	}
}

func TestEntriesSkipsInvalidPeers(t *testing.T) {
	home := t.TempDir()
	peers := filepath.Join(home, ".lanbus", "peers")
	writePeer(t, filepath.Join(peers, "good"), "http://127.0.0.1:8791", "s3cr3t")
	writePeer(t, filepath.Join(peers, "remote"), "http://192.168.1.2:8791", "secret")
	entries := Entries(home)
	if len(entries) != 1 || entries[0].Name != "good" {
		t.Fatalf("Entries() = %+v, want only good", entries)
	}
}
