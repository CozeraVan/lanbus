// lanbusd is the native Go hub (Phase 2). It speaks the existing lanbus
// worker protocol and MCP subset, so the Python and Go hubs can be run in
// parallel for compatibility testing.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CozeraVan/lanbus/go/internal/hub"
)

func main() {
	var cfg hub.Config
	flag.StringVar(&cfg.Bind, "bind", "0.0.0.0", "bind address")
	flag.IntVar(&cfg.Port, "port", 8788, "listen port")
	flag.StringVar(&cfg.DataDir, "data-dir", "", "data directory (default ~/.lanbus)")
	flag.StringVar(&cfg.Token, "token", "", "agent token (default LANBUS_TOKEN or devtoken)")
	flag.StringVar(&cfg.WorkerToken, "worker-token", "", "worker token")
	flag.StringVar(&cfg.OperatorToken, "operator-token", "", "human/operator token for approvals (generated if absent)")
	var tokenFile, workerTokenFile string
	flag.StringVar(&tokenFile, "token-file", "", "read agent token from file")
	flag.StringVar(&workerTokenFile, "worker-token-file", "", "read worker token from file")
	flag.StringVar(&cfg.Version, "version-string", "1.9.0-go", "version string")
	flag.Float64Var(&cfg.ReapS, "reap-s", 5, "lease reaper interval seconds")
	flag.IntVar(&cfg.LeaseS, "lease-s", 120, "default lease seconds")
	flag.StringVar(&cfg.DownloadDir, "download-dir", "bin", "directory served at /download/ for remote worker bootstrap")
	var version bool
	flag.BoolVar(&version, "version", false, "print version")
	flag.Parse()
	if version {
		fmt.Println("lanbusd 1.9.0-go")
		return
	}
	if cfg.Token == "" && tokenFile != "" {
		if b, err := os.ReadFile(tokenFile); err == nil {
			cfg.Token = strings.TrimSpace(string(b))
		}
	}
	if cfg.WorkerToken == "" && workerTokenFile != "" {
		if b, err := os.ReadFile(workerTokenFile); err == nil {
			cfg.WorkerToken = strings.TrimSpace(string(b))
		}
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("LANBUS_TOKEN")
	}
	if cfg.Token == "" {
		cfg.Token = "devtoken"
	}
	if cfg.WorkerToken == "" {
		cfg.WorkerToken = os.Getenv("LANBUS_WORKER_TOKEN")
	}
	h, err := hub.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanbusd:", err)
		os.Exit(1)
	}
	defer h.Close()
	if err := h.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "lanbusd:", err)
		os.Exit(1)
	}
}
