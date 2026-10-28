// Package buildinfo holds version metadata injected at build time.
package buildinfo

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// UserAgent identifies the updater to servers.
func UserAgent() string {
	return fmt.Sprintf("wml-update/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
}

// Full returns a human-readable build description.
func Full() string {
	return fmt.Sprintf("wml-update %s (commit %s, built %s, %s/%s)", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
