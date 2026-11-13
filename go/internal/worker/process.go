package worker

import (
	"bufio"
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
			cmd = exec.Command("cmd.exe")
			setShellCmdLine(cmd, shellCmd)
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
	start := time.Now()
	rc := 0
	cancelled := false
	timedOut := false
	for {
		select {
		case <-done:
			if err := cmd.Wait(); err == nil {
				rc = 0
			} else if ee, ok := err.(*exec.ExitError); ok {
				rc = ee.ExitCode()
			} else {
				rc = 1
			}
			goto finished
		case <-cancel:
			cancelled = true
			p.kill()
			time.Sleep(300 * time.Millisecond)
			p.kill()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			rc = 130
			goto finished
		case <-time.After(200 * time.Millisecond):
			if timeout > 0 && time.Since(start) > time.Duration(timeout)*time.Second {
				timedOut = true
				p.kill()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				rc = 124
				goto finished
			}
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				rc = cmd.ProcessState.ExitCode()
				goto finished
			}
		}
	}
finished:
	p.close()
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

func envList() []string {
	return osEnviron()
}

func isWindows() bool {
	return runtimeGOOS() == "windows"
}
