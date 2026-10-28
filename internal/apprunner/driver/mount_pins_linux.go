//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Native path observations are private infrastructure pins, never manifest
// values, journal receipts or execution permits. Held descriptors prevent inode
// reuse; validation compares both held identities and fresh no-symlink paths.
type mountDirectoryPin struct {
	path     string
	file     *os.File
	stat     unix.Stat_t
	uid, gid uint32
	mode     uint32
}
type candidateMountPins struct {
	mu      sync.Mutex
	closed  bool
	bodySHA string
	dirs    []mountDirectoryPin
}
type candidateDataPin struct{ Device, Inode uint64 }

func mountStatEqual(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Gid == b.Gid && a.Mode == b.Mode
}
func safeMountDirectory(st unix.Stat_t, leaf bool, uid, gid, mode uint32) bool {
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false
	}
	if leaf {
		return st.Uid == uid && st.Gid == gid && st.Mode&07777 == mode
	}
	return st.Uid == 0 && ((st.Mode&0022 == 0 && st.Mode&07000 == 0) || (st.Mode&unix.S_ISVTX != 0 && st.Mode&06000 == 0))
}
func openMountDirectory(path string, uid, gid, mode uint32) ([]mountDirectoryPin, error) {
	if !validContainerRoot(path) {
		return nil, ErrUnsafeSocket
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafeSocket
	}
	current := os.NewFile(uintptr(fd), "/")
	pins := []mountDirectoryPin{}
	fail := func() ([]mountDirectoryPin, error) {
		current.Close()
		for _, p := range pins {
			p.file.Close()
		}
		return nil, ErrUnsafeSocket
	}
	var root unix.Stat_t
	if unix.Fstat(fd, &root) != nil || !safeMountDirectory(root, false, 0, 0, 0) {
		return fail()
	}
	pins = append(pins, mountDirectoryPin{path: "/", file: current, stat: root})
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	prefix := ""
	for i, part := range parts {
		child, err := unix.Openat(int(current.Fd()), part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fail()
		}
		current = os.NewFile(uintptr(child), part)
		var st unix.Stat_t
		leaf := i == len(parts)-1
		if unix.Fstat(child, &st) != nil || !safeMountDirectory(st, leaf, uid, gid, mode) {
			return fail()
		}
		prefix += "/" + part
		p := mountDirectoryPin{path: prefix, file: current, stat: st}
		if leaf {
			p.uid, p.gid, p.mode = uid, gid, mode
		}
		pins = append(pins, p)
	}
	return pins, nil
}
func pinCandidateMounts(ctx context.Context, plan containerPlan, data candidateDataPin) (*candidateMountPins, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, ErrUnsupported
	}
	if !validContainerPlan(plan) || data.Inode == 0 {
		return nil, ErrInvalidResponse
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || len(request.HostConfig.Mounts) != 3 {
		return nil, ErrInvalidResponse
	}
	pins := &candidateMountPins{bodySHA: plan.bodySHA}
	fail := func(err error) (*candidateMountPins, error) { pins.Close(); return nil, err }
	for _, mount := range request.HostConfig.Mounts {
		var uid, gid, mode uint32
		switch mount.Target {
		case "/data":
			mode = 0700
			if plan.writable != nil {
				gid, mode = 65532, 02770
			}
		case "/run/portal-app/http":
			uid, gid, mode = 65532, 65532, 0700
		case "/run/portal-app/broker":
			mode = 0755
		default:
			return fail(ErrInvalidResponse)
		}
		dirs, err := openMountDirectory(mount.Source, uid, gid, mode)
		if err != nil {
			return fail(err)
		}
		pins.dirs = append(pins.dirs, dirs...)
		if mount.Target == "/run/portal-app/http" {
			if err := validateCandidateHTTPStorage(int(dirs[len(dirs)-1].file.Fd()), CandidateIdentity(plan.owner), true); err != nil {
				return fail(err)
			}
		}
		if mount.Target == "/data" {
			st := dirs[len(dirs)-1].stat
			if st.Ino != data.Inode || uint64(st.Dev) != data.Device {
				return fail(ErrUnsafeSocket)
			}
		}
	}
	if err := pins.validate(ctx, plan); err != nil {
		return fail(err)
	}
	return pins, nil
}
func (p *candidateMountPins) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	for _, d := range p.dirs {
		if e := d.file.Close(); e != nil && err == nil {
			err = e
		}
	}
	p.dirs = nil
	return err
}
func (p *candidateMountPins) validate(ctx context.Context, plan containerPlan) error {
	if p == nil {
		return ErrUnsafeSocket
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateLocked(ctx, plan)
}

func (p *candidateMountPins) validateLocked(ctx context.Context, plan containerPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.closed || !validContainerPlan(plan) || plan.bodySHA != p.bodySHA || len(p.dirs) == 0 {
		return ErrUnsafeSocket
	}
	for _, pin := range p.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		var held unix.Stat_t
		if unix.Fstat(int(pin.file.Fd()), &held) != nil || !mountStatEqual(held, pin.stat) {
			return ErrUnsafeSocket
		}
		if pin.uid == 65532 && pin.gid == 65532 && pin.mode == 0700 {
			if err := validateCandidateHTTPStorage(int(pin.file.Fd()), CandidateIdentity(plan.owner), true); err != nil {
				return err
			}
		}
		if pin.path == "/" {
			continue
		}
		// Reopen every component from root. EvalSymlinks or lstat of only the leaf
		// would fail to detect a replaced parent or a newly introduced symlink.
		mode := pin.mode
		uid, gid := pin.uid, pin.gid
		if mode == 0 {
			mode = held.Mode & 07777
			uid, gid = held.Uid, held.Gid
		}
		fresh, err := openMountDirectory(pin.path, uid, gid, mode)
		if err != nil {
			return err
		}
		st := fresh[len(fresh)-1].stat
		for _, d := range fresh {
			d.file.Close()
		}
		if !mountStatEqual(st, pin.stat) {
			return ErrUnsafeSocket
		}
	}
	return ctx.Err()
}

// Pin lifetime spans the synchronous create request. A post-effect path change
// is uncertain and never authorizes retry or adoption. Root/daemon compromise
// remains outside this profile; Docker still resolves trusted-host bind paths.
func createPinnedCandidateHTTP(ctx context.Context, client *http.Client, plan containerPlan, pins *candidateMountPins) (string, error) {
	if pins == nil {
		return "", ErrUnsafeSocket
	}
	pins.mu.Lock()
	defer pins.mu.Unlock()
	if err := pins.validateLocked(ctx, plan); err != nil {
		return "", err
	}
	id, err := createCandidateContainerHTTP(ctx, client, plan)
	if err != nil {
		return "", err
	}
	if err := pins.validateLocked(ctx, plan); err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	return id, nil
}

// A live native pin set is required for initial start. The lock spans the
// synchronous action and its observations; no root/path authority is invented.
func startPinnedCandidateHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan, pins *candidateMountPins) (string, error) {
	if pins == nil {
		return "", ErrUnsafeSocket
	}
	pins.mu.Lock()
	defer pins.mu.Unlock()
	if err := pins.validateLocked(ctx, plan); err != nil {
		return "", err
	}
	state, err := startCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return "", err
	}
	if err := pins.validateLocked(ctx, plan); err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	return state, nil
}
