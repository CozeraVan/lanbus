package worker

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// runStreamingPTY runs a command attached to a real PTY (ConPTY on Windows,
// Unix PTY elsewhere). PTY mode is opt-in: it is needed by TUI-oriented CLIs,
// while the regular pipe mode is more deterministic for `-p`/stdin agents.
func runStreamingPTY(name string, args []string, shellCmd, cwd, stdin string, cancel <-chan struct{}, timeout int, logf func(string)) runResult {
	terminal, err := pty.New()
	if err != nil {
		return runResult{125, "(pty: " + err.Error() + ")"}
	}
	defer terminal.Close()

	var cmd *pty.Cmd
	if shellCmd != "" {
		if isWindows() {
			cmd = terminal.Command(resolvePTYCommand("cmd.exe"), "/C", shellCmd)
		} else {
			cmd = terminal.Command(resolvePTYCommand("sh"), "-c", shellCmd)
		}
	} else {
		path := resolvePTYCommand(name)
		if isWindows() {
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".cmd" || ext == ".bat" {
				cmd = terminal.Command(resolvePTYCommand("cmd.exe"),
					append([]string{"/C", path}, args...)...)
			} else {
				cmd = terminal.Command(path, args...)
			}
		} else {
			cmd = terminal.Command(path, args...)
		}
	}
	cmd.Dir = cwd
	cmd.Env = append(append([]string{}, envList()...),
		"LANBUS_WORKER=1", "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8")
	if err := cmd.Start(); err != nil {
		return runResult{125, "(pty exec: " + err.Error() + ")"}
	}
	p := &proc{pid: cmd.Process.Pid, job: attachProcess(cmd.Process.Pid)}

	if stdin != "" {
		go func(data []byte) {
			_, _ = terminal.Write(data)
			// EOT makes line-oriented CLIs using stdin finish even in PTY mode.
			_, _ = terminal.Write([]byte{4})
		}([]byte(stdin))
	}

	var lines []string
	var mu sync.Mutex
	readDone := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(terminal)
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
		close(readDone)
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	start := time.Now()
	rc := 0
	cancelled := false
	timedOut := false
	select {
	case err := <-waitCh:
		rc = exitCode(err)
	case <-cancel:
		cancelled = true
		p.kill()
		_ = terminal.Close()
		<-waitCh
		rc = 130
	case <-time.After(200 * time.Millisecond):
		for {
			select {
			case err := <-waitCh:
				rc = exitCode(err)
				goto finished
			case <-cancel:
				cancelled = true
				p.kill()
				_ = terminal.Close()
				<-waitCh
				rc = 130
				goto finished
			case <-time.After(200 * time.Millisecond):
				if timeout > 0 && time.Since(start) > time.Duration(timeout)*time.Second {
					timedOut = true
					p.kill()
					_ = terminal.Close()
					<-waitCh
					rc = 124
					goto finished
				}
			}
		}
	}
finished:
	p.close()
	select {
	case <-readDone:
	case <-time.After(1500 * time.Millisecond):
	}
	mu.Lock()
	out := cleanPTYOutput(joinLines(lines))
	mu.Unlock()
	if cancelled {
		return runResult{130, out + "\n(cancelled)"}
	}
	if timedOut {
		return runResult{124, out + "\n(timeout)"}
	}
	return runResult{rc, out}
}

func resolvePTYCommand(name string) string {
	if name == "" {
		return name
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\].*?\x07`)
)

func cleanPTYOutput(s string) string {
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, line := range lines[1:] {
		out += "\n" + line
	}
	return out
}
