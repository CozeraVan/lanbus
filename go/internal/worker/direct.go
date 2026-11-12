package worker

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/CozeraVan/lanbus/go/internal/peer"
)

var permissionModeRe = regexp.MustCompile(`--permission-mode(?:\s+|=)\S+`)

// ApplyPermissionMode mirrors Python worker.apply_permission_mode: override an
// existing --permission-mode flag only; other adapters are left untouched.
func ApplyPermissionMode(command, mode string) string {
	if mode == "" || !strings.Contains(command, "--permission-mode") {
		return command
	}
	return permissionModeRe.ReplaceAllString(command, "--permission-mode "+mode)
}

// DefaultCommand mirrors Python worker.DEFAULTS (shell form).
func DefaultCommand(agent string) string {
	switch agent {
	case "claude":
		return "claude -p --permission-mode acceptEdits"
	case "codex":
		return "codex exec --skip-git-repo-check -"
	case "deepseek":
		return "dsh headless -"
	default:
		return ""
	}
}

func expandPromptFile(command, promptFile string, windows bool) string {
	quoted := `"` + promptFile + `"`
	if !windows {
		quoted = `'` + strings.ReplaceAll(promptFile, `'`, `'"'"'`) + `'`
	}
	command = strings.ReplaceAll(command, `"{prompt_file}"`, quoted)
	command = strings.ReplaceAll(command, `'{prompt_file}'`, quoted)
	return strings.ReplaceAll(command, "{prompt_file}", quoted)
}

func windowsTempCommand(path string, read bool) (string, []string) {
	args := []string{"/C"}
	if read {
		args = append(args, "type")
	}
	return "cmd.exe", append(args, path)
}

// RunDirect starts the zero-config direct peer server: no hub, no token,
// target-side human approval via `lanbus inbox/allow` (same wire protocol
// as the Python worker, so Python/Go initiators interoperate).
func RunDirect(cfg Config) error {
	if cfg.Name == "" {
		return fmt.Errorf("--name is required")
	}
	dataDir := cfg.DirectDataDir
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataDir = filepath.Join(home, ".lanbus", "peers", cfg.Name)
	}
	command := cfg.Cmd
	if command == "" {
		command = DefaultCommand(cfg.Agent)
	}
	command = ApplyPermissionMode(command, cfg.AgentPermissionMode)
	permMode := cfg.AgentPermissionMode
	if permMode == "" {
		permMode = "acceptEdits"
	}
	executor := func(message, scope, cwd string, timeout float64) map[string]any {
		cancel := make(chan struct{})
		var r runResult
		if command != "" {
			tmp, err := os.CreateTemp("", "lanbus-peer-*.md")
			if err != nil {
				return map[string]any{"ok": false, "exit_code": 1, "reply": err.Error(), "peer": cfg.Name}
			}
			promptFile := tmp.Name()
			tmp.WriteString(message)
			tmp.Close()
			defer os.Remove(promptFile)
			cmd := expandPromptFile(command, promptFile, runtime.GOOS == "windows")
			stdin := message
			if strings.Contains(command, "{prompt_file}") {
				stdin = ""
			}
			r = runStreaming("", nil, cmd, cwd, stdin, cancel, int(timeout), nil)
		} else {
			switch cfg.Agent {
			case "claude":
				r = runStreaming("claude", []string{"-p", "--permission-mode", permMode}, "", cwd, message, cancel, int(timeout), nil)
			case "codex":
				r = runStreaming("codex", []string{"exec", "--skip-git-repo-check", "-"}, "", cwd, message, cancel, int(timeout), nil)
			case "kimi":
				r = runStreaming("kimi", []string{"-p", message}, "", cwd, "", cancel, int(timeout), nil)
			case "deepseek":
				r = runStreaming("dsh", []string{"headless", "-"}, "", cwd, message, cancel, int(timeout), nil)
			case "shell":
				if runtime.GOOS == "windows" {
					tmp, err := os.CreateTemp("", "lanbus-shell-*.bat")
					if err != nil {
						r = runResult{code: 125, output: err.Error()}
					} else {
						tmp.WriteString("@echo off\r\nchcp 65001 >nul\r\n" + message + "\r\n")
						tmp.Close()
						name, args := windowsTempCommand(tmp.Name(), false)
						r = runStreaming(name, args, "", cwd, "", cancel, int(timeout), nil)
						os.Remove(tmp.Name())
					}
				} else {
					r = runStreaming("", nil, message, cwd, "", cancel, int(timeout), nil)
				}
			case "echo":
				if runtime.GOOS == "windows" {
					tmp, err := os.CreateTemp("", "lanbus-echo-*.txt")
					if err != nil {
						r = runResult{code: 125, output: err.Error()}
					} else {
						tmp.WriteString(message + "\n")
						tmp.Close()
						name, args := windowsTempCommand(tmp.Name(), true)
						r = runStreaming(name, args, "", cwd, "", cancel, int(timeout), nil)
						os.Remove(tmp.Name())
					}
				} else {
					r = runStreaming("echo", []string{message}, "", cwd, "", cancel, int(timeout), nil)
				}
			default:
				r = runResult{code: 127, output: "(unknown agent " + cfg.Agent + ")"}
			}
		}
		reply := r.output
		if runes := []rune(reply); len(runes) > 20000 {
			reply = string(runes[len(runes)-20000:])
		}
		return map[string]any{"ok": r.code == 0, "exit_code": r.code, "reply": reply, "peer": cfg.Name}
	}
	server, err := peer.NewServer(peer.ServerConfig{
		Name: cfg.Name, Agent: cfg.Agent, CWD: cfg.CWD, Command: command,
		DataDir: dataDir, Host: cfg.DirectBind, Port: cfg.DirectPort, Executor: executor,
	})
	if err != nil {
		return err
	}
	server.Start()
	fmt.Printf("[worker:%s] direct=%s peer_id=%s\n", cfg.Name, server.URL, server.Identity.PeerID)
	fmt.Printf("[worker:%s] 本机审批: lanbus inbox --name %s\n", cfg.Name, cfg.Name)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	server.Stop()
	return nil
}
