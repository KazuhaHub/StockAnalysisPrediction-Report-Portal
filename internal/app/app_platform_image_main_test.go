package app

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps the managed image stores out of the source tree: a test that lets the stores fall back to the
// package's own data directory leaves staged packages in the repository.
func TestMain(m *testing.M) {
	code := m.Run()
	for _, leftover := range []string{"data/app-staging", "data/app-blobs"} {
		if _, err := os.Stat(leftover); err == nil {
			fmt.Fprintf(os.Stderr, "FAIL: tests left %s in the source tree\n", leftover)
			os.RemoveAll("data")
			code = 1
		}
	}
	os.Exit(code)
}
