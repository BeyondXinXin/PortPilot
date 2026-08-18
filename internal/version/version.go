// Package version provides the version string injected at build time.
package version

import "strings"

// Version is set by build.cmd with:
//
//   -ldflags "-X github.com/BeyondXinXin/portpilot/internal/version.Version=..."
//
// It remains "dev" for local builds that do not have Git metadata.
var Version = "dev"

// Display returns a human-friendly version without duplicating the v prefix.
func Display() string {
	if Version == "" || Version == "dev" {
		return "dev"
	}
	if strings.HasPrefix(Version, "dev-") {
		return Version
	}
	if strings.HasPrefix(Version, "v") {
		return Version
	}
	return "v" + Version
}
