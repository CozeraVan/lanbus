//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUIEntries(t *testing.T) {
	home := t.TempDir()
	peersDir := filepath.Join(home, ".lanbus", "peers")

	// valid peer
	good := filepath.Join(peersDir, "go-agent")
	os.MkdirAll(good, 0o755)
	os.WriteFile(filepath.Join(good, "peer.json"), []byte(`{"name":"go-agent","url":"http://127.0.0.1:8791","port":8791}`), 0o644)
	os.WriteFile(filepath.Join(good, "approval.json"), []byte(`{"secret":"s3cr3t"}`), 0o600)

	// missing approval.json -> skipped
	noSecret := filepath.Join(peersDir, "no-secret")
	os.MkdirAll(noSecret, 0o755)
	os.WriteFile(filepath.Join(noSecret, "peer.json"), []byte(`{"url":"http://127.0.0.1:9999"}`), 0o644)

	// empty url -> skipped
	emptyURL := filepath.Join(peersDir, "empty-url")
	os.MkdirAll(emptyURL, 0o755)
	os.WriteFile(filepath.Join(emptyURL, "peer.json"), []byte(`{"url":""}`), 0o644)
	os.WriteFile(filepath.Join(emptyURL, "approval.json"), []byte(`{"secret":"x"}`), 0o600)

	entries := peerUIEntries(home)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].name != "go-agent" {
		t.Fatalf("name = %q", entries[0].name)
	}
	want := "http://127.0.0.1:8791/peer/ui#secret=s3cr3t"
	if entries[0].url != want {
		t.Fatalf("url = %q, want %q", entries[0].url, want)
	}
}

func TestPeerUIEntriesMissingDir(t *testing.T) {
	if entries := peerUIEntries(t.TempDir()); len(entries) != 0 {
		t.Fatalf("expected 0 entries for missing dir, got %d", len(entries))
	}
}