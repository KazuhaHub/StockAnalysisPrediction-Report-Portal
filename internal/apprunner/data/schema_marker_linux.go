//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// readSchemaMarkerIn reads the marker from an open namespace directory without following links. It
// reports absence only for a missing entry; anything that is not one regular, singly linked file of at
// most MaxSchemaMarkerBytes with a canonical value is unsafe.
func readSchemaMarkerIn(folder *os.File) (schema int, present bool, err error) {
	fd := -1
	err = native(folder, func(parent uintptr) error {
		var openErr error
		fd, openErr = unix.Openat(int(parent), SchemaMarkerName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		return openErr
	})
	if errors.Is(err, unix.ENOENT) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errors.Join(ErrMigrationUnsafe, err)
	}
	file := os.NewFile(uintptr(fd), "runner-schema-marker")
	defer file.Close()
	var stat unix.Stat_t
	if err := native(file, func(fd uintptr) error { return unix.Fstat(int(fd), &stat) }); err != nil {
		return 0, false, errors.Join(ErrMigrationUnsafe, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 1 || stat.Size > MaxSchemaMarkerBytes {
		return 0, false, ErrMigrationUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxSchemaMarkerBytes+1))
	if err != nil || int64(len(raw)) != stat.Size {
		return 0, false, errors.Join(ErrMigrationUnsafe, err)
	}
	value, err := parseSchemaMarker(raw)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

// ReadSchemaMarker observes the schema marker and the usage of one namespace that holds the fixed
// writable profile. It mutates nothing. The result is not a migration receipt.
func (r *Root) ReadSchemaMarker(ctx context.Context, expected RootIdentity, id string, project uint32) (SchemaMarkerObservation, error) {
	var zero SchemaMarkerObservation
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !namespaceID.MatchString(id) || project == 0 {
		return zero, ErrMigrationUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, err
	}
	before, err := r.inspectNamespaceAccess(ctx, id, project, writeAccessGID, writeAccessMode)
	if err != nil {
		return zero, err
	}
	folder, err := r.openNamespace(id)
	if err != nil {
		return zero, err
	}
	defer folder.Close()
	schema, present, err := readSchemaMarkerIn(folder)
	if err != nil {
		return zero, err
	}
	after, err := r.inspectNamespaceAccess(ctx, id, project, writeAccessGID, writeAccessMode)
	if err != nil || after.Inode != before.Inode || after.Device != before.Device {
		return zero, errors.Join(ErrMigrationUnsafe, err)
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, err
	}
	return SchemaMarkerObservation{valid: true, snapshot: SchemaMarkerSnapshot{
		NamespaceID: id, ProjectID: project, Present: present, Schema: schema,
		UsedBytes: after.UsedBytes, UsedInodes: after.UsedInodes, ByteLimit: after.ByteLimit, InodeLimit: after.InodeLimit,
		NamespaceDevice: after.Device, NamespaceInode: after.Inode, ObservedMS: time.Now().UnixMilli(),
	}}, nil
}
