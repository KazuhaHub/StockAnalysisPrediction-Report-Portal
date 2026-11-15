//go:build !linux && !darwin

package app

import (
	"fmt"
	"runtime"
)

// Without a way to identify the filesystem there is no WAL and no read pool: the fail-safe.
func detectSQLiteFilesystem(string) (string, bool, error) {
	return "", false, fmt.Errorf("filesystem identification is not implemented on %s", runtime.GOOS)
}
