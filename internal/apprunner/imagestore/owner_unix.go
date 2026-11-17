//go:build unix

package imagestore

import (
	"os"
	"syscall"
)

func ownedByEffectiveUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
