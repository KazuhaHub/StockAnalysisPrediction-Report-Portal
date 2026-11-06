//go:build linux

package driver

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func socketInfo(path string, owner uint32) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 107 || strings.ContainsAny(path, "\x00\r\n") {
		return nil, ErrUnsafeSocket
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, ErrUnsafeSocket
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0007 != 0 {
		return nil, ErrUnsafeSocket
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || stat.Nlink != 1 {
		return nil, ErrUnsafeSocket
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		dir, err := os.Lstat(parent)
		if err != nil || !dir.IsDir() {
			return nil, ErrUnsafeSocket
		}
		st, ok := dir.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return nil, ErrUnsafeSocket
		}
		if dir.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && dir.Mode()&os.ModeSticky != 0) {
			return nil, ErrUnsafeSocket
		}
		if parent == "/" {
			break
		}
	}
	return info, nil
}

func dockerDialer(path string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		before, err := socketInfo(path, 0)
		if err != nil {
			return nil, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return nil, ErrAccessDenied
			}
			return nil, ErrUnavailable
		}
		refuse := func() (net.Conn, error) { conn.Close(); return nil, ErrUnsafeSocket }
		if !rootPeer(conn) {
			return refuse()
		}
		after, err := socketInfo(path, 0)
		if err != nil || !os.SameFile(before, after) {
			return refuse()
		}
		return conn, nil
	}
}

func rootPeer(conn net.Conn) bool {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return false
	}
	var credential *syscall.Ucred
	var credentialErr error
	err = raw.Control(func(fd uintptr) {
		credential, credentialErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return err == nil && credentialErr == nil && credential != nil && credential.Uid == 0
}
