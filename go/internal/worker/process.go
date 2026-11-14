package worker

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type runResult struct {
	code   int
	output string
}

// proc is a started child with a platform-specific supervision handle.
// On Windows job is a Job Object handle; on Unix job is always zero and the
// process group id is used for cancellation.
type proc struct {
	pid int
	job uintptr
}

func runStreaming(name string, args []string, shellCmd, cwd, stdin string, cancel <-chan struct{}, timeout int, logf func(string)) runResult {
	var cmd *exec.Cmd
	if shellCmd != "" {
		if isWindows() {
			cmd = exec.Command("cmd.exe", "/C", shellCmd)
		} else {
			cmd = exec.Command("sh", "-c", shellCmd)
		}
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.Dir = cwd
	env := append([]string{}, envList()...)
	env = append(env, "LANBUS_WORKER=1", "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8")
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runResult{125, "(pipe: " + err.Error() + ")"}
	}
	cmd.Stderr = cmd.Stdout
	p, err := startProcess(cmd)
	if err != nil {
		return runResult{125, "(exec: " + err.Error() + ")"}
	}
	var lines []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			lines = append(lines, line)
			if len(lines) > 8000 {
				lines = lines[len(lines)-8000:]
			}
			mu.Unlock()
			if logf != nil {
				logf(line)
			}
		}
		close(done)
	}()
	// Detect process exit independently of stdout EOF. A child can exit while a
	// grandchild it spawned keeps the inherited stdout pipe write end open (e.g. a
	// launcher CLI that leaves a lingering SDK/node process behind). Waiting only
	// for EOF would hang until the total timeout, surfacing as status=running with
	// any error text stuck in the scanner buffer. os.Process.Wait reports the exit
	// without closing the pipe, so remaining output can still be drained.
	waitCh := make(chan *os.ProcessState, 1)
	go func() {
		ps, _ := cmd.Process.Wait()
		waitCh <- ps
	}()

	start := time.Now()
	rc := 0
	cancelled := false
	timedOut := false
	for {
		select {
		case <-done:
			// stdout drained (normal completion); reap the exit code.
			rc = exitCodeFromState(<-waitCh)
			goto finished
		case ps := <-waitCh:
			// The process exited but a grandchild may still hold the pipe open.
			// Kill the tree to release it, then drain whatever output remains.
			p.kill()
			select {
			case <-done:
			case <-time.After(1500 * time.Millisecond):
			}
			rc = exitCodeFromState(ps)
			goto finished
		case <-cancel:
			cancelled = true
			p.kill()
			_ = cmd.Process.Kill()
			<-waitCh
			rc = 130
			goto finished
		case <-time.After(200 * time.Millisecond):
			if timeout > 0 && time.Since(start) > time.Duration(timeout)*time.Second {
				timedOut = true
				p.kill()
				_ = cmd.Process.Kill()
				<-waitCh
				rc = 124
				goto finished
			}
		}
	}
finished:
	p.close()
	_ = stdout.Close()
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	if cancelled {
		return runResult{130, out + "\n(cancelled)"}
	}
	if timedOut {
		return runResult{124, out + "\n(timeout)"}
	}
	return runResult{rc, out}
}

// exitCodeFromState maps a reaped process state to an exit code, defaulting to 1
// when the state is unavailable (e.g. the process could not be waited on).
func exitCodeFromState(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	return ps.ExitCode()
}

func envList() []string {
	return osEnviron()
}

func isWindows() bool {
	return runtimeGOOS() == "windows"
}
