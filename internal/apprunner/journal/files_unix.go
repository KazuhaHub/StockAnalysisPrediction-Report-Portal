//go:build linux || darwin

package journal

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type directoryGuard struct {
	directory        string
	root, lock       *os.File
	rootInfo, dbInfo os.FileInfo
	closeOnce        sync.Once
	closeErr         error
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func privateRegular(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && owned(info) && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1
}

func lockDirectory(directory string) (*directoryGuard, error) {
	if !filepath.IsAbs(directory) || directory != filepath.Clean(directory) {
		return nil, ErrUnsafePath
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	if canonical != directory {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return nil, ErrUnsafePath
	}
	// A different unprivileged owner must not be able to rename the private
	// directory through a writable/owned ancestor. Trusted sticky temporary
	// roots are allowed; the journal directory itself remains private.
	for parent := filepath.Dir(directory); ; parent = filepath.Dir(parent) {
		ancestor, err := os.Lstat(parent)
		if err != nil {
			return nil, err
		}
		stat, ok := ancestor.Sys().(*syscall.Stat_t)
		if !ok || !ancestor.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
			return nil, ErrUnsafePath
		}
		if ancestor.Mode().Perm()&0022 != 0 && ancestor.Mode()&os.ModeSticky == 0 {
			return nil, ErrUnsafePath
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	root, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	guard := &directoryGuard{directory: directory, root: root, rootInfo: info}
	ok := false
	defer func() {
		if !ok {
			guard.close()
		}
	}()
	opened, err := root.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, ErrUnsafePath
	}
	if err := guard.checkFiles(); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, lockName)
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	created := err == nil
	if errors.Is(err, syscall.EEXIST) {
		fd, err = syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	guard.lock = os.NewFile(uintptr(fd), path)
	if created {
		if err := guard.lock.Chmod(0600); err != nil {
			return nil, err
		}
	}
	lockInfo, err := guard.lock.Stat()
	if err != nil {
		return nil, err
	}
	if !privateRegular(lockInfo) {
		return nil, ErrUnsafePath
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrInUse
		}
		return nil, err
	}
	onDisk, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(onDisk, lockInfo) {
		return nil, ErrUnsafePath
	}
	if err := guard.checkFiles(); err != nil {
		return nil, err
	}
	ok = true
	return guard, nil
}

func (g *directoryGuard) checkFiles() error {
	root, err := os.Lstat(g.directory)
	if err != nil {
		return err
	}
	if !os.SameFile(root, g.rootInfo) || !owned(root) || !root.IsDir() || root.Mode().Perm() != 0700 {
		return ErrUnsafePath
	}
	entries, err := os.ReadDir(g.directory)
	if err != nil {
		return err
	}
	database, sidecar := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case journalName:
			database = true
		case journalName + "-wal", journalName + "-shm", journalName + "-journal":
			sidecar = true
		case lockName:
		default:
			return ErrUnsafePath
		}
		info, err := os.Lstat(filepath.Join(g.directory, entry.Name()))
		if err != nil {
			return err
		}
		if !privateRegular(info) {
			return ErrUnsafePath
		}
		if entry.Name() == journalName && g.dbInfo != nil && !os.SameFile(info, g.dbInfo) {
			return ErrUnsafePath
		}
	}
	if sidecar && !database {
		return ErrUnsafePath
	}
	return nil
}

func (g *directoryGuard) prepareDatabase() error {
	path := filepath.Join(g.directory, journalName)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), path)
		defer file.Close()
		if err := file.Chmod(0600); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		info, err = file.Stat()
		if err != nil {
			return err
		}
		if err := g.sync(); err != nil {
			return err
		}
	}
	if !privateRegular(info) {
		return ErrUnsafePath
	}
	g.dbInfo = info
	return g.checkFiles()
}

func (g *directoryGuard) sync() error { return g.root.Sync() }

func (g *directoryGuard) close() error {
	g.closeOnce.Do(func() {
		var unlock, closeLock, flush, closeRoot error
		if g.root != nil {
			flush = g.root.Sync()
		}
		if g.lock != nil {
			unlock = syscall.Flock(int(g.lock.Fd()), syscall.LOCK_UN)
			closeLock = g.lock.Close()
		}
		if g.root != nil {
			closeRoot = g.root.Close()
		}
		g.closeErr = errors.Join(flush, unlock, closeLock, closeRoot)
	})
	return g.closeErr
}
