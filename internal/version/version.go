package version

import "fmt"

// Value is set via ldflags at build time (e.g. -X main.version=...).
// main.go copies the value here at startup so internal packages can use it.
var Value = "dev"

func UserAgent() string {
	return fmt.Sprintf("Chunk-CLI (%s)", Value)
}
