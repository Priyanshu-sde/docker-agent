package selfupdate

import "github.com/docker/docker-agent/pkg/version"

// currentVersion returns the compiled-in release version. Wrapped so the rest
// of the package depends only on a string and stays trivially testable.
func currentVersion() string {
	return version.Version
}
