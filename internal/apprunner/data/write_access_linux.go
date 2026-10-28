//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const writeAccessGID = 65532
const writeAccessMode = 02770

func sameNamespaceAllocation(a, b Observation) bool {
	return a.NamespaceID == b.NamespaceID && a.ProjectID == b.ProjectID && a.Device == b.Device && a.Inode == b.Inode && a.ByteLimit == b.ByteLimit && a.InodeLimit == b.InodeLimit && a.Inherited && b.Inherited && a.UsedBytes >= 0 && a.UsedBytes <= a.ByteLimit && a.UsedInodes >= 0 && a.UsedInodes <= a.InodeLimit
}

// EnableWriteAccess changes one sealed namespace to root:65532 mode 02770.
// Root retains inode ownership; group access and setgid support the fixed app
// identity without letting it chmod/chown the namespace root. Project identity,
// inheritance and hard limits must already match the exact admitted allocation.
//
// This low-level primitive has no production dispatcher. Its trusted caller must
// durably claim the effect and exclude other writers/containers before calling.
// It does not inspect Docker, grant a logical writer, permit a writable mount or
// revoke existing open descriptors. Errors after the first mutation are uncertain;
// retries never repair or adopt a partial/already writable namespace.
func (r *Root) EnableWriteAccess(ctx context.Context, expected RootIdentity, allocation Observation) (WriteAccessObservation, error) {
	var zero WriteAccessObservation
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !namespaceID.MatchString(allocation.NamespaceID) || allocation.ProjectID == 0 || allocation.Inode == 0 || allocation.ByteLimit <= 0 || allocation.InodeLimit <= 0 || !allocation.Inherited {
		return zero, ErrUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, err
	}
	before, err := r.inspect(ctx, allocation.NamespaceID, allocation.ProjectID)
	if err != nil || !sameNamespaceAllocation(before, allocation) {
		return zero, errors.Join(ErrUnsafe, err)
	}
	folder, err := r.openNamespace(allocation.NamespaceID)
	if err != nil {
		return zero, err
	}
	defer folder.Close()
	var st unix.Stat_t
	if err := native(folder, func(fd uintptr) error { return unix.Fstat(int(fd), &st) }); err != nil {
		return zero, err
	}
	if uint64(st.Dev) != before.Device || st.Ino != before.Inode || st.Uid != 0 || st.Gid != 0 || st.Mode&07777 != 0700 {
		return zero, ErrUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := native(folder, func(fd uintptr) error { return unix.Fchown(int(fd), 0, writeAccessGID) }); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	// Cancellation here leaves an owned but incomplete sealed transition. Never
	// roll back permissions: a later caller could have observed open descriptors.
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	if err := native(folder, func(fd uintptr) error { return unix.Fchmod(int(fd), writeAccessMode) }); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	if err := folder.Sync(); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	if err := r.file.Sync(); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	after, err := r.inspectNamespaceAccess(ctx, allocation.NamespaceID, allocation.ProjectID, writeAccessGID, writeAccessMode)
	if err != nil || !sameNamespaceAllocation(after, before) {
		return zero, errors.Join(ErrIncomplete, err)
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrIncomplete, err)
	}
	return WriteAccessObservation{Namespace: after}, nil
}

// InspectWriteAccess observes the fixed writable DAC profile without mutating,
// adopting or repairing it. The result is not current controller/writer authority.
func (r *Root) InspectWriteAccess(ctx context.Context, expected RootIdentity, id string, project uint32) (WriteAccessObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !namespaceID.MatchString(id) || project == 0 {
		return WriteAccessObservation{}, ErrUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return WriteAccessObservation{}, err
	}
	observed, err := r.inspectNamespaceAccess(ctx, id, project, writeAccessGID, writeAccessMode)
	if err != nil {
		return WriteAccessObservation{}, err
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return WriteAccessObservation{}, err
	}
	return WriteAccessObservation{Namespace: observed}, nil
}
