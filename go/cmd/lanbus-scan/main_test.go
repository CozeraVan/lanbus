package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionUsesProductVersionStamp(t *testing.T) {
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goExe += ".exe"
	}
	cmd := exec.Command(goExe, "run", "-ldflags=-X github.com/CozeraVan/lanbus/go/internal/version.Version=9.8.7-test", ".", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run --version: %v\n%s", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "lanbus-scan 9.8.7-test"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}
