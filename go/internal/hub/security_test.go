package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHostValidation(t *testing.T) {
	for _, host := range []string{"localhost", "localhost:8788", "127.0.0.1:8788", "192.168.1.8", "[::1]:8788"} {
		if !validHost(host) {
			t.Errorf("valid host rejected: %q", host)
		}
	}
	for _, host := range []string{"", "evil.example.com", "evil.example.com:8788"} {
		if validHost(host) {
			t.Errorf("invalid host accepted: %q", host)
		}
	}
}

func TestAuthenticatedRoutesRejectForeignHost(t *testing.T) {
	h := newRegressionHub(t)
	req := httptest.NewRequest(http.MethodPost, "http://evil.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer agent")
	res := httptest.NewRecorder()
	h.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestHealthKeepsPublicSemantics(t *testing.T) {
	h := newRegressionHub(t)
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/health", nil)
	req.Host = "evil.example.com"
	res := httptest.NewRecorder()
	h.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
}

func TestTruncPreservesUTF8(t *testing.T) {
	got := trunc("ab界面", 4)
	if !utf8.ValidString(got) || got != "ab" {
		t.Fatalf("trunc=%q valid=%v", got, utf8.ValidString(got))
	}
}
