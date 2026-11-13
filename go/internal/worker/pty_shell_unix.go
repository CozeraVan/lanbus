//go:build !windows

package worker

import "github.com/aymanbagabas/go-pty"

// setPTYShellCmdLine is a no-op on Unix: `sh -c` receives the shell string as a
// single argument with correct escaping, so no raw command line is needed.
func setPTYShellCmdLine(cmd *pty.Cmd, exe, shellCmd string) {}