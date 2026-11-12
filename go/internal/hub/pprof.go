package hub

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// servePprof runs net/http/pprof on a dedicated loopback listener so profiling
// is never exposed on the public hub port. PprofAddr must be a loopback address.
func (h *Hub) servePprof() {
	addr := h.cfg.PprofAddr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Println("[lanbusd] pprof disabled, bad addr:", err)
		return
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fmt.Println("[lanbusd] pprof disabled, addr must be loopback:", addr)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	fmt.Println("[lanbusd] pprof listening", addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println("[lanbusd] pprof:", err)
	}
}
