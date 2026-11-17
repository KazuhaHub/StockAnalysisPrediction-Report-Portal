//go:build !unix

package imagestore

import "os"

func ownedByEffectiveUser(os.FileInfo) bool { return true }
