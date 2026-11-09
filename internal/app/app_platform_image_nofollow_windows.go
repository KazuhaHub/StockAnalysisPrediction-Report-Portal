//go:build windows

package app

import (
	"errors"
	"os"
)

const platformNoFollow = 0

func platformCheckStoreDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("image store directory is not a real directory")
	}
	return nil
}
