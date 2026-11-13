package worker

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestRunShellPreservesDoubleQuotes is a regression test for the Windows
// cmd.exe quoting bug: exec.Command("cmd.exe", "/C", cmd) applies MSVCRT-style
// escaping that cmd.exe does not understand, so any shell command containing
// double quotes was truncated/mangled (e.g. `python -c "print('x')"` reached the
// child as `"print('x'`). The shell path must preserve embedded double quotes.
func TestRunShellPreservesDoubleQuotes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: cmd.exe quoting")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("python not available")
	}
	cwd := t.TempDir()
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"ascii", `python -u -c "print('hello' * 3)"`, "hellohellohello"},
		{"unicode", `python -u -c "print('界面🙂' * 2)"`, "界面🙂界面🙂"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cancel := make(chan struct{})
			r := runStreaming("", nil, tc.cmd, cwd, "", cancel, 30, nil)
			if r.code != 0 {
				t.Fatalf("exit code = %d, want 0; output=%q", r.code, r.output)
			}
			if got := strings.TrimSpace(r.output); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
