package consoleproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CozeraVan/lanbus/go/internal/peer"
)

// newPeer spins a real direct peer under <home>/.lanbus/peers/<name> so the
// proxy can resolve its loopback URL + approval secret from disk.
func newPeer(t *testing.T, home, name string) *peer.Server {
	t.Helper()
	s, err := peer.NewServer(peer.ServerConfig{
		Name: name, DataDir: filepath.Join(home, ".lanbus", "peers", name),
		Port: 0, RequestPolicy: "pair",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(s.Stop)
	return s
}

func do(t *testing.T, h *Handler, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(raw)
}

func TestLauncherListsPeers(t *testing.T) {
	home := t.TempDir()
	newPeer(t, home, "alpha")
	h := &Handler{Home: home}
	code, body := do(t, h, "GET", "/console", "")
	if code != 200 || !strings.Contains(body, "alpha") || !strings.Contains(body, "/console/alpha") {
		t.Fatalf("launcher code=%d body=%s", code, body)
	}
}

func TestConsolePageInjectsBase(t *testing.T) {
	home := t.TempDir()
	newPeer(t, home, "alpha")
	h := &Handler{Home: home}
	code, body := do(t, h, "GET", "/console/alpha", "")
	if code != 200 {
		t.Fatalf("console page code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `__LANBUS_BASE__="/console/alpha"`) {
		t.Fatalf("console page missing injected base: %s", body[:min(300, len(body))])
	}
	if !strings.Contains(body, "lanbus") {
		t.Fatalf("console page not served: %s", body[:min(300, len(body))])
	}
}

// TestAPIProxyInjectsSecret proves the proxy adds the approval secret: a direct
// loopback call without the secret is 403, but through the proxy it is 200.
func TestAPIProxyInjectsSecret(t *testing.T) {
	home := t.TempDir()
	s := newPeer(t, home, "alpha")
	h := &Handler{Home: home}

	// Control: direct call without secret is rejected.
	resp, err := http.Get(s.URL + "/peer/console/locals")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expected direct 403, got %d", resp.StatusCode)
	}

	// Through the proxy: secret injected → 200 with locals.
	code, body := do(t, h, "GET", "/console/alpha/peer/console/locals", "")
	if code != 200 || !strings.Contains(body, "alpha") {
		t.Fatalf("proxied locals code=%d body=%s", code, body)
	}
}

func TestAPIProxyPostBody(t *testing.T) {
	home := t.TempDir()
	s := newPeer(t, home, "alpha")
	h := &Handler{Home: home}
	code, body := do(t, h, "POST", "/console/alpha/peer/console/history",
		`{"controller":"alpha","target_url":"`+s.URL+`"}`)
	if code != 200 || !strings.Contains(body, "turns") {
		t.Fatalf("proxied POST history code=%d body=%s", code, body)
	}
}

func TestRejectsBadName(t *testing.T) {
	home := t.TempDir()
	newPeer(t, home, "alpha")
	h := &Handler{Home: home}
	for _, p := range []string{"/console/beta", "/console/..", "/console/alpha/../../etc"} {
		code, _ := do(t, h, "GET", p, "")
		if code != 404 {
			t.Fatalf("path %q expected 404, got %d", p, code)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
