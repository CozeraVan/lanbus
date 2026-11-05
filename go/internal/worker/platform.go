package worker

import (
	"os"
	"runtime"
)

func osEnviron() []string { return os.Environ() }
func runtimeGOOS() string { return runtime.GOOS }
