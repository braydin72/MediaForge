package version

import "fmt"

// Version is overridden at release build time via:
// -ldflags "-X github.com/braydin72/mediaforge/internal/version.Version=<X.Y.Z>"
// derived from the git tag. This fallback is used for non-tagged/dev builds.
var Version = "1.1.0"

// Build is overridden at compile time via:
// -ldflags "-X github.com/braydin72/mediaforge/internal/version.Build=<number>"
var Build = "dev"

// String returns the full version string, e.g. "v1.0.0+build.42" or "v1.0.0+build.dev".
func String() string {
	return fmt.Sprintf("v%s+build.%s", Version, Build)
}
