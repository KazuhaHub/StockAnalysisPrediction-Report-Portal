// Package data contains native data-preparation primitives. It has no command,
// network or automatic startup entry point and grants no application access.
package data

import (
	"encoding/hex"
	"errors"
	"os"
	"regexp"
)

var (
	ErrUnsupported          = errors.New("native data profile is unsupported")
	ErrUnsafe               = errors.New("data root or project reservation is unsafe")
	ErrConflict             = errors.New("data namespace or project already exists")
	ErrIncomplete           = errors.New("data preparation requires reconciliation")
	ErrCheckpointUnsafe     = errors.New("data checkpoint contains unsafe or changed content")
	ErrCheckpointIncomplete = errors.New("data checkpoint requires reconciliation")
	ErrRestoreUnsafe        = errors.New("data restore binding is unsafe")
	ErrRestoreIncomplete    = errors.New("data restore requires reconciliation")
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

// CheckpointRequest is trusted infrastructure input derived from a stopped
// generation. It never contains a host path or caller-selected command.
type CheckpointRequest struct {
	CheckpointID, NamespaceID, ReleaseSHA256 string
	ProjectID                                uint32
	DataFormat                               int64
	MaxArchiveBytes                          int64
	ExpectedDevice, ExpectedNamespaceInode   uint64
	WriterFence                              int64
}

func (r CheckpointRequest) valid() bool {
	return namespaceID.MatchString(r.CheckpointID) && namespaceID.MatchString(r.NamespaceID) && r.ProjectID > 0 && r.DataFormat > 0 && r.MaxArchiveBytes > 0 && r.ExpectedDevice > 0 && r.ExpectedNamespaceInode > 0 && r.WriterFence > 0 && sha256Value(r.ReleaseSHA256)
}

// CheckpointSnapshot describes a complete archive that was read back and
// hashed. It is not a journal receipt, restore proof or activation authority.
type CheckpointSnapshot struct {
	NamespaceID, ReleaseSHA256 string
	ProjectID                  uint32
	DataFormat                 int64
	ArchiveBytes, Entries      int64
	ManifestSHA256             string
	ObservedMS                 int64
}

// CheckpointObservation is minted only after checkpoint read-back succeeds.
type CheckpointObservation struct {
	snapshot CheckpointSnapshot
	valid    bool
}

func (CheckpointObservation) MarshalJSON() ([]byte, error) { return nil, ErrCheckpointUnsafe }
func (o *CheckpointObservation) UnmarshalJSON([]byte) error {
	*o = CheckpointObservation{}
	return ErrCheckpointUnsafe
}
func (o CheckpointObservation) Snapshot() (CheckpointSnapshot, error) {
	if !o.valid || o.snapshot.ArchiveBytes <= 0 || o.snapshot.Entries < 0 || o.snapshot.ObservedMS <= 0 ||
		!namespaceID.MatchString(o.snapshot.NamespaceID) || !sha256Value(o.snapshot.ReleaseSHA256) || !sha256Value(o.snapshot.ManifestSHA256) {
		return CheckpointSnapshot{}, ErrCheckpointUnsafe
	}
	return o.snapshot, nil
}

// RestoreRequest binds one immutable checkpoint to the existing namespace it
// may replace. It contains no host path or caller-selected archive entry.
type RestoreRequest struct {
	CheckpointID, NamespaceID, ReleaseSHA256, ManifestSHA256 string
	ProjectID                                                uint32
	DataFormat, ArchiveBytes, MaxArchiveBytes                int64
	ExpectedDevice, ExpectedNamespaceInode                   uint64
	WriterFence                                              int64
}

func (r RestoreRequest) valid() bool {
	return namespaceID.MatchString(r.CheckpointID) && namespaceID.MatchString(r.NamespaceID) &&
		r.ProjectID > 0 && r.DataFormat > 0 && r.ArchiveBytes > 0 &&
		r.MaxArchiveBytes > 0 && r.ArchiveBytes <= r.MaxArchiveBytes &&
		r.ExpectedDevice > 0 && r.ExpectedNamespaceInode > 0 && r.WriterFence > 0 &&
		sha256Value(r.ReleaseSHA256) && sha256Value(r.ManifestSHA256)
}

// RestoreSnapshot is minted only after archive verification, safe replacement,
// and a second full namespace read-back matched the checkpoint.
type RestoreSnapshot struct {
	NamespaceID, ReleaseSHA256, ManifestSHA256 string
	ProjectID                                  uint32
	DataFormat, Entries, UsedBytes, ObservedMS int64
	NamespaceDevice, NamespaceInode            uint64
}

// RestoreObservation is not durable journal evidence or restore authority.
type RestoreObservation struct {
	snapshot RestoreSnapshot
	valid    bool
}

func (RestoreObservation) MarshalJSON() ([]byte, error) { return nil, ErrCheckpointUnsafe }
func (o *RestoreObservation) UnmarshalJSON([]byte) error {
	*o = RestoreObservation{}
	return ErrCheckpointUnsafe
}
func (o RestoreObservation) Snapshot() (RestoreSnapshot, error) {
	if !o.valid || !namespaceID.MatchString(o.snapshot.NamespaceID) ||
		!sha256Value(o.snapshot.ReleaseSHA256) || !sha256Value(o.snapshot.ManifestSHA256) ||
		o.snapshot.ProjectID == 0 || o.snapshot.DataFormat <= 0 ||
		o.snapshot.NamespaceDevice == 0 || o.snapshot.NamespaceInode == 0 ||
		o.snapshot.Entries < 0 || o.snapshot.UsedBytes < 0 || o.snapshot.ObservedMS <= 0 {
		return RestoreSnapshot{}, ErrCheckpointUnsafe
	}
	return o.snapshot, nil
}

func sha256Value(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}

// Open requires an explicitly configured root-owned private existing data root.
// Implementations own a new pinned descriptor; callers retain their input file.
func Open(root *os.File) (*Root, error) { return openRoot(root) }
