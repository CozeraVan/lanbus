package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestAskOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "fake-model"}}})
		case "/v1/chat/completions":
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": "hello from fake"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	res := askEndpoint(u.Hostname(), port, "hi", "", "", "", false, 5*time.Second, 16)
	if !res.OK || res.API != "openai" || res.Model != "fake-model" || res.Reply != "hello from fake" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestAskOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "ollama-model"}}})
		case "/api/chat":
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "ollama reply"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	res := askEndpoint(u.Hostname(), port, "hi", "", "", "", false, 5*time.Second, 16)
	if !res.OK || res.API != "ollama" || res.Reply != "ollama reply" {
		t.Fatalf("unexpected result: %+v", res)
	}
}
