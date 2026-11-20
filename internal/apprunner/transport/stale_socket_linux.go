//go:build linux

package transport

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// RemoveStaleControlSocket removes the control socket a runner that was killed left behind, so that the next
// listener can bind (doc 187 section 7). The listener itself still refuses any pre-existing path.
//
// It acts only when both hold:
//   - the caller holds the journal's exclusive owner lock, which `held` stands for: a *journal.Store exists
//     only while its directory lock is held, so no live runner can own the socket;
//   - lstat shows a socket (not a link, file or directory) owned 0:<controllerGID> with mode 0660 and one
//     link, under a control root that has the shape the listener requires.
//
// Anything else at the path is left in place and reported as ErrUnsafeEndpoint. A missing path is not an error.
func RemoveStaleControlSocket(path string, controllerGID uint32, held *journal.Store) error {
	if held == nil {
		return ErrUnsafeEndpoint
	}
	return removeStaleControlSocket(path, 0, controllerGID)
}

func removeStaleControlSocket(path string, uid, gid uint32) error {
	profile := endpointProfile{owner: uid, group: gid, parentMode: 0750, socketMode: 0660, shared: true}
	if !safeParent(path, profile) {
		return ErrUnsafeEndpoint
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrUnsafeEndpoint
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != profile.socketMode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		st.Uid != uid || st.Gid != gid || st.Nlink != 1 {
		return ErrUnsafeEndpoint
	}
	// The identity is re-read right before the unlink, so a path replaced in between is never removed.
	again, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, again) {
		return ErrUnsafeEndpoint
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrUnsafeEndpoint
	}
	return nil
}
