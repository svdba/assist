// version/version.go
package version

import (
	"fmt"
)

var (
	// These are set during build via -ldflags
	Version   = "0.0.0"
	AppName   = "assist"
	BuildDate = "2026-04-18" // Format: YYYY-MM-DD
	BuildSHA  = "0000000"
)

// Prints the app version
func String() string {
	return fmt.Sprintf("%s %s (build: %s-%s)", AppName, Version, BuildDate, BuildSHA)
}
