package worker

import (
	"os/exec"
	"time"
)

func execLookPath(name string) (string, error) { return exec.LookPath(name) }
func sleepSec(n int)                           { time.Sleep(time.Duration(n) * time.Second) }
