//go:build windows

package service

import "os"

func ownedByEffectiveUser(os.FileInfo) bool { return false }
