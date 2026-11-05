//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
	"time"
)

func startProcess(cmd *exec.Cmd) (*proc, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &proc{pid: cmd.Process.Pid}, nil
}

func (p *proc) kill() {
	if p == nil || p.pid <= 0 {
		return
	}
	_ = syscall.Kill(-p.pid, syscall.SIGTERM)
	go func(pid int) {
		time.Sleep(3 * time.Second)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}(p.pid)
}

func attachProcess(pid int) uintptr { return 0 }

func (p *proc) close() {}
