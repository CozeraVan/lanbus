//go:build !windows

package scanner

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
