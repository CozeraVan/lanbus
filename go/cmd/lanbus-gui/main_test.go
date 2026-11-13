//go:build windows

package main

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CozeraVan/lanbus/go/internal/consoleproxy"
	"github.com/CozeraVan/lanbus/go/internal/peer"
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

// TestProxyHandlerDelegatesConsole verifies the desktop shell routes /console to
// the consoleproxy: launcher, injected-base console page, and secret-injected API
// proxying, all against a real local peer.
func TestProxyHandlerDelegatesConsole(t *testing.T) {
	home := t.TempDir()
	s, err := peer.NewServer(peer.ServerConfig{
		Name: "alpha", DataDir: filepath.Join(home, ".lanbus", "peers", "alpha"),
		Port: 0, RequestPolicy: "pair",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	h := &proxyHandler{console: &consoleproxy.Handler{Home: home}}

	req := httptest.NewRequest("GET", "/console", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/console/alpha") {
		t.Fatalf("launcher code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/console/alpha", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `__LANBUS_BASE__="/console/alpha"`) {
		t.Fatalf("console page code=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/console/alpha/peer/console/locals", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	if rec.Code != 200 || !strings.Contains(string(body), "alpha") {
		t.Fatalf("proxied api code=%d body=%s", rec.Code, string(body))
	}
}
