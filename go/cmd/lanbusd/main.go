// lanbusd is the native Go hub (Phase 2). It speaks the existing lanbus
// worker protocol and MCP subset, so the Python and Go hubs can be run in
// parallel for compatibility testing.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CozeraVan/lanbus/go/internal/hub"
	"github.com/CozeraVan/lanbus/go/internal/version"
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
	flag.StringVar(&cfg.Version, "version-string", version.String(), "version string")
	flag.Float64Var(&cfg.ReapS, "reap-s", 5, "lease reaper interval seconds")
	flag.IntVar(&cfg.LeaseS, "lease-s", 120, "default lease seconds")
	flag.StringVar(&cfg.DownloadDir, "download-dir", "bin", "directory served at /download/ for remote worker bootstrap")
	flag.StringVar(&cfg.TLSCert, "tls-cert", "", "PEM cert file; enables HTTPS with --tls-key")
	flag.StringVar(&cfg.TLSKey, "tls-key", "", "PEM key file for HTTPS")
	flag.StringVar(&cfg.PprofAddr, "pprof", "", "loopback addr for pprof (e.g. 127.0.0.1:6060)")
	var allowDevToken bool
	flag.BoolVar(&allowDevToken, "allow-dev-token", false, "permit the insecure 'devtoken' default")
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "print version")
	flag.Parse()
	if showVersion {
		fmt.Println("lanbusd " + version.String())
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
	if cfg.Token == "" && allowDevToken {
		cfg.Token = "devtoken"
	}
	if cfg.Token == "" {
		// Product default: generate a strong random token and persist it so
		// workers can read it via --token-file. Never ship the well-known
		// "devtoken" unless explicitly requested.
		dataDir := cfg.DataDir
		if dataDir == "" {
			home, _ := os.UserHomeDir()
			dataDir = filepath.Join(home, ".lanbus")
		}
		_ = os.MkdirAll(dataDir, 0o755)
		tokFile := filepath.Join(dataDir, "token.txt")
		if b, err := os.ReadFile(tokFile); err == nil && strings.TrimSpace(string(b)) != "" {
			cfg.Token = strings.TrimSpace(string(b))
		} else {
			buf := make([]byte, 24)
			_, _ = rand.Read(buf)
			cfg.Token = hex.EncodeToString(buf)
			_ = os.WriteFile(tokFile, []byte(cfg.Token+"\n"), 0o600)
			fmt.Println("[lanbusd] generated agent token ->", tokFile)
		}
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
