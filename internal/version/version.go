// Package version carries the build stamp injected by the Makefile.
package version

import (
	"fmt"
	"runtime"
)

// Overridden with -ldflags -X at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Short renders "version@commit", which is what the log line and /healthz use.
func Short() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return Version + "@" + Commit
}

// Full adds the platform, for `mcpx version`.
func Full() string {
	return fmt.Sprintf("mcpx %s (%s) %s %s/%s", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
