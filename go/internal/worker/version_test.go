package worker

import (
	"testing"

	"github.com/CozeraVan/lanbus/go/internal/version"
)

// TestWorkerVersionTracksBuildVersion guards against the registration version
// being hardcoded: the worker must report the build-stamped product version
// (internal/version.Version, set via -ldflags) with the "-go" suffix, so lan_envs
// shows the real release instead of a stale literal.
func TestWorkerVersionTracksBuildVersion(t *testing.T) {
	old := version.Version
	version.Version = "9.8.7-test"
	defer func() { version.Version = old }()

	w, err := New(Config{Name: "vtest"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "9.8.7-test-go"; w.version != want {
		t.Fatalf("worker version = %q, want %q", w.version, want)
	}
}