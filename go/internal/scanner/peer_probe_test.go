package scanner

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestProbeLanbusPeer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"win-agent","agent":"claude","hostname":"WIN","peer_id":"97908059f8584d102bfc0a1e4d4533e3","direct_pairing":true,"endpoint":"http://192.168.1.236:8790","scopes":["chat","project"]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	services := probeHTTP(host, port, false, 2)
	found := false
	for _, s := range services {
		if s["kind"] == "lanbus-peer" {
			found = true
			if s["peer_id"] != "97908059f8584d102bfc0a1e4d4533e3" || s["name"] != "win-agent" || s["ai"] != true {
				t.Fatalf("bad lanbus-peer service: %v", s)
			}
		}
	}
	if !found {
		t.Fatalf("lanbus-peer not detected: %v", services)
	}
}