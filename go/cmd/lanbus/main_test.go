package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		status    int
		wantError bool
	}{
		{name: "healthy", body: `{"ok":true,"version":"1.9.0"}`, status: 200},
		{name: "unhealthy", body: `{"ok":false}`, status: 200, wantError: true},
		{name: "missing ok", body: `{}`, status: 200, wantError: true},
		{name: "plain text", body: `not a lanbus hub`, status: 200, wantError: true},
		{name: "invalid json", body: `{`, status: 200, wantError: true},
		{name: "json array", body: `[]`, status: 200, wantError: true},
		{name: "http error", body: `{"ok":true}`, status: 500, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			err := healthcheck([]string{"--url", server.URL})
			if (err != nil) != test.wantError {
				t.Fatalf("healthcheck() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestPeerUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "peer.json"), []byte(`{"url":"http://127.0.0.1:8791"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approval.json"), []byte(`{"secret":"s3cr3t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var opened string
	err := peerUI([]string{"--name", "go-agent", "--data-dir", dir}, func(rawURL string) error {
		opened = rawURL
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8791/peer/ui#secret=s3cr3t"
	if opened != want {
		t.Fatalf("opened URL = %q, want %q", opened, want)
	}
}

func TestPeerUIRejectsUnsafeNameWithoutOpeningBrowser(t *testing.T) {
	called := false
	err := peerUI([]string{"--name", "../escape"}, func(string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("peerUI() error = %v, opener called = %v", err, called)
	}
}
