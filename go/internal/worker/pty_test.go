package worker

import (
	"strings"
	"testing"
)

func TestRunStreamingPTY(t *testing.T) {
	r := runStreamingPTY("", nil, "echo PTY_OK", "", "", make(chan struct{}), 10, nil)
	if r.code != 0 || !strings.Contains(r.output, "PTY_OK") {
		t.Fatalf("pty result code=%d output=%q", r.code, r.output)
	}
}
