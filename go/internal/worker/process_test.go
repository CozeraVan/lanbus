package worker

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRunStreamingDetectsExitWhenGrandchildHoldsPipe reproduces the silent-hang
// deadlock: the child prints an error (without a trailing newline) and exits,
// but a grandchild it spawned inherits and keeps the stdout pipe write end open.
// Waiting only for stdout EOF would block until the total timeout, surfacing as
// status=running with the error stuck in the scanner buffer (logs=None).
// runStreaming must detect the child's exit independently, release the pipe and
// report the real exit code plus the captured output promptly.
func TestRunStreamingDetectsExitWhenGrandchildHoldsPipe(t *testing.T) {
	t.Setenv("LANBUS_EXIT_HELPER", "child")

	start := time.Now()
	// timeout is 3s; a correct fix returns in well under that via exit detection,
	// while the broken EOF-only path would only return at the 3s timeout (rc=124).
	r := runStreaming(os.Args[0], []string{"-test.run=TestExitDetectHelperProcess"},
		"", "", "", make(chan struct{}), 3, nil)
	elapsed := time.Since(start)

	if r.code != 7 {
		t.Fatalf("exit code = %d, want 7 (child's real exit code; 124 means it waited for the timeout)", r.code)
	}
	if !strings.Contains(r.output, "lanbus-error-line") {
		t.Fatalf("output = %q, want it to contain the flushed error line", r.output)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("took %v, want prompt exit detection (<2s, not the 3s timeout)", elapsed)
	}
}

// TestRunStreamingCapturesNonZeroExit guards the normal path: a child that exits
// non-zero with output must report that code and output (no grandchild involved).
func TestRunStreamingCapturesNonZeroExit(t *testing.T) {
	t.Setenv("LANBUS_EXIT_HELPER", "plain")
	r := runStreaming(os.Args[0], []string{"-test.run=TestExitDetectHelperProcess"},
		"", "", "", make(chan struct{}), 5, nil)
	if r.code != 3 {
		t.Fatalf("exit code = %d, want 3", r.code)
	}
	if !strings.Contains(r.output, "plain-line") {
		t.Fatalf("output = %q, want it to contain plain-line", r.output)
	}
}

// TestExitDetectHelperProcess is re-executed as a child/grandchild by the tests
// above (via -test.run). It is a no-op during a normal test run.
func TestExitDetectHelperProcess(t *testing.T) {
	switch os.Getenv("LANBUS_EXIT_HELPER") {
	case "plain":
		fmt.Print("plain-line\n")
		os.Exit(3)
	case "child":
		// Error without a trailing newline: only flushed once the pipe reaches EOF.
		fmt.Print("lanbus-error-line")
		// Spawn a grandchild that inherits our stdout (the pipe write end) and
		// lingers, then exit without waiting for it.
		gc := exec.Command(os.Args[0], "-test.run=TestExitDetectHelperProcess")
		gc.Env = append(os.Environ(), "LANBUS_EXIT_HELPER=grandchild")
		gc.Stdout = os.Stdout
		gc.Stderr = os.Stderr
		if err := gc.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "grandchild start: %v\n", err)
			os.Exit(9)
		}
		os.Exit(7)
	case "grandchild":
		// Hold the inherited stdout pipe open while idle.
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}