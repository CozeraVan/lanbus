package worker

import (
	"runtime"
	"strings"
	"testing"
)

func TestRunStreamingPTY(t *testing.T) {
	name, args, shell := "sh", []string{"-c", "printf PTY_OK"}, ""
	if runtime.GOOS == "windows" {
		name, args, shell = "", nil, "echo PTY_OK"
	}
	r := runStreamingPTY(name, args, shell, "", "", make(chan struct{}), 10, nil)
	if r.code != 0 || !strings.Contains(r.output, "PTY_OK") {
		t.Fatalf("pty result code=%d output=%q", r.code, r.output)
	}
}
