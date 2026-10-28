// Package data contains native data-preparation primitives. It has no command,
// network or automatic startup entry point and grants no application access.
package data

import (
	"errors"
	"os"
	"regexp"
)

var (
	ErrUnsupported = errors.New("native data profile is unsupported")
	ErrUnsafe      = errors.New("data root or project reservation is unsafe")
	ErrConflict    = errors.New("data namespace or project already exists")
	ErrIncomplete  = errors.New("data preparation requires reconciliation")
)
var namespaceID = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)

type Request struct {
	NamespaceID   string
	ProjectID     uint32
	Bytes, Inodes int64
}

func (r Request) valid() bool {
	return namespaceID.MatchString(r.NamespaceID) && r.ProjectID > 0 && r.Bytes > 0 && r.Bytes%512 == 0 && r.Inodes > 0
}

// RootIdentity pins infrastructure configuration to one physical data root.
// Device numbers are deliberately excluded because they can change on remount.
// This is an observation, not installation authority or a durable binding.
type RootIdentity struct {
	FilesystemUUID string
	Inode          uint64
}

func (r RootIdentity) valid() bool {
	return r.Inode > 0 && namespaceID.MatchString(r.FilesystemUUID) && r.FilesystemUUID != "00000000-0000-0000-0000-000000000000"
}

// Observation is a current native snapshot, not a durable command receipt or
// proof of journal admission, writer exclusion or container sandboxing.
type Observation struct {
	NamespaceID                                  string
	ProjectID                                    uint32
	Device, Inode                                uint64
	ByteLimit, InodeLimit, UsedBytes, UsedInodes int64
	Inherited                                    bool
}

// WriteAccessObservation is a physical permission/quota observation, not a
// writer lease or an admission receipt. A caller must separately journal and
// fence authority and exclude old containers/open descriptors before mutation.
type WriteAccessObservation struct{ Namespace Observation }

// Open requires an explicitly configured root-owned private existing data root.
// Implementations own a new pinned descriptor; callers retain their input file.
func Open(root *os.File) (*Root, error) { return openRoot(root) }
