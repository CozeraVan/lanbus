// Package version holds the single product version, stampable at build time
// via -ldflags "-X github.com/CozeraVan/lanbus/go/internal/version.Version=x.y.z".
// It mirrors src/lanbus/__version__ so Go and Python report the same release.
package version

// Version is the product version. Overridden by the build (Makefile/CI).
var Version = "1.9.0"

// String returns the product version.
func String() string { return Version }
