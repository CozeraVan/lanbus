//go:build windows

package worker

import (
	"syscall"

	"github.com/aymanbagabas/go-pty"
)

// setPTYShellCmdLine gives ConPTY's cmd.exe a raw command line, mirroring
// setShellCmdLine for the pipe path. go-pty honors SysProcAttr.CmdLine verbatim
// (cmd_windows.go), bypassing the MSVCRT-style escaping that mangles embedded
// double quotes. `cmd.exe /S /C "<cmd>"` makes cmd strip the outer quotes and run
// the command verbatim. No CREATE_NO_WINDOW here: a PTY needs its console.
func setPTYShellCmdLine(cmd *pty.Cmd, exe, shellCmd string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: exe + ` /S /C "` + shellCmd + `"`}
}