// Package version holds build metadata injected with -ldflags -X.
package version

import "fmt"

// Set at build time; the defaults describe an un-stamped local build.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String renders the build metadata on one line.
func String() string {
	return fmt.Sprintf("ponnokhoj %s (commit %s, built %s)", Version, Commit, Date)
}
