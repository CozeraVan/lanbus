// lanbus-worker-go: pull worker compatible with the Python lanbus hub.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/CozeraVan/lanbus/go/internal/version"
	"github.com/CozeraVan/lanbus/go/internal/worker"
)

func main() {
	var cfg worker.Config
	flag.StringVar(&cfg.Name, "name", "", "worker name")
	flag.StringVar(&cfg.Env, "env", "", "windows|wsl|macos|...")
	flag.StringVar(&cfg.Hub, "hub", "", "http://HUB:8788")
	flag.StringVar(&cfg.Token, "token", os.Getenv("LANBUS_WORKER_TOKEN"), "worker token")
	flag.StringVar(&cfg.CWD, "cwd", "", "default working directory")
	flag.StringVar(&cfg.Agent, "agent", "claude", "claude|codex|kimi|deepseek|shell|echo")
	flag.StringVar(&cfg.Cmd, "cmd", "", "custom command; {prompt_file} supported")
	flag.StringVar(&cfg.Caps, "caps", "", "extra comma-separated capabilities")
	flag.StringVar(&cfg.ArtifactGlob, "artifact-glob", "", "extra artifact glob (comma separated)")
	flag.StringVar(&cfg.Outbox, "outbox", "", "submission outbox dir")
	flag.StringVar(&cfg.LogDir, "log-dir", "", "local log dir")
	flag.Float64Var(&cfg.ProgressS, "progress-s", 3, "progress/cancel poll seconds")
	flag.IntVar(&cfg.MaxArtifactMB, "max-artifact-mb", 32, "artifact size cap")
	flag.BoolVar(&cfg.NoBrowser, "no-browser", false, "accepted for CLI compatibility")
	flag.BoolVar(&cfg.PTY, "pty", false, "run agent commands on a real PTY (ConPTY on Windows)")
	flag.BoolVar(&cfg.Insecure, "insecure", false, "skip TLS verification")
	flag.BoolVar(&cfg.Version, "version", false, "print version")
	flag.BoolVar(&cfg.Direct, "direct", false, "zero-config direct peering (no hub/token)")
	flag.IntVar(&cfg.DirectPort, "direct-port", 8790, "direct peer port")
	flag.StringVar(&cfg.DirectBind, "direct-bind", "0.0.0.0", "direct bind address")
	flag.StringVar(&cfg.DirectDataDir, "direct-data-dir", "", "direct identity/grant dir (default ~/.lanbus/peers/<name>)")
	flag.StringVar(&cfg.DisplayName, "display-name", "", "approval window target display name")
	flag.StringVar(&cfg.InstanceLabel, "instance-label", "", "label distinguishing multiple local agent windows")
	flag.StringVar(&cfg.RequestPolicy, "request-policy", "company", "company=free member chat; each=approve every request; pair=legacy")
	flag.BoolVar(&cfg.NoOpenApproval, "no-open-approval", false, "do not open the local approval page")
	flag.StringVar(&cfg.AgentPermissionMode, "agent-permission-mode", os.Getenv("LANBUS_AGENT_PERMISSION_MODE"),
		"override agent --permission-mode (e.g. bypassPermissions on trusted LAN)")
	flag.Parse()
	if cfg.Version {
		fmt.Println("lanbus-worker-go " + version.String())
		return
	}
	if cfg.Direct {
		if err := worker.RunDirect(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "lanbus-worker:", err)
			os.Exit(1)
		}
		return
	}
	if cfg.Name == "" || cfg.Env == "" || cfg.Hub == "" {
		fmt.Fprintln(os.Stderr, "usage: lanbus-worker --name NAME --env ENV --hub URL [options]")
		os.Exit(2)
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("LANBUS_TOKEN")
	}
	if cfg.Token == "" {
		cfg.Token = "devtoken"
	}
	if cfg.Caps == "" {
		cfg.Caps = runtime.GOOS
	}
	if strings.Contains(cfg.Caps, "browser") {
		// Browser tasks are intentionally left to the Python worker for now.
	}
	w, err := worker.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-worker:", err)
		os.Exit(1)
	}
	if err := w.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-worker:", err)
		os.Exit(1)
	}
}
