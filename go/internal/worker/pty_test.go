package worker

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/aymanbagabas/go-pty"
)

func TestRunStreamingPTY(t *testing.T) {
	r := runStreamingPTY("", nil, "echo PTY_OK", "", "", make(chan struct{}), 10, nil)
	if r.code != 0 || !strings.Contains(r.output, "PTY_OK") {
		t.Fatalf("pty result code=%d output=%q", r.code, r.output)
	}
}

// TestRunPTYPreservesDoubleQuotes is the PTY-path regression for the same
// Windows cmd.exe quoting bug fixed in runStreaming: terminal.Command("cmd.exe",
// "/C", cmd) lets MSVCRT-style escaping mangle embedded double quotes. The ConPTY
// shell path must preserve them via a raw SysProcAttr.CmdLine (`cmd.exe /S /C`).
func TestRunPTYPreservesDoubleQuotes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: cmd.exe quoting")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("python not available")
	}
	if term, err := pty.New(); err != nil {
		t.Skip("pty not available: " + err.Error())
	} else {
		term.Close()
	}
	cwd := t.TempDir()
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"ascii", `python -u -c "print('hello' * 3)"`, "hellohellohello"},
		{"unicode", `python -u -c "print('界面' * 3)"`, "界面界面界面"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runStreamingPTY("", nil, tc.cmd, cwd, "", make(chan struct{}), 30, nil)
			if r.code != 0 {
				t.Fatalf("exit code = %d, want 0; output=%q", r.code, r.output)
			}
			if got := strings.TrimSpace(r.output); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
