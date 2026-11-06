// Package data contains native data-preparation primitives. It has no command,
// network or automatic startup entry point and grants no application access.
package data

import (
	"encoding/hex"
	"errors"
	"math"
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
	ErrDeleteUnsafe         = errors.New("data deletion binding is unsafe")
	ErrDeleteIncomplete     = errors.New("data deletion requires reconciliation")
	ErrImportUnsafe         = errors.New("data checkpoint import binding is unsafe")
	ErrImportIncomplete     = errors.New("data checkpoint import requires reconciliation")
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

// DeleteRequest binds one retained namespace and project quota to an exact
// reviewed deletion. It contains no host path or caller-selected traversal.
type DeleteRequest struct {
	NamespaceID, ReleaseSHA256             string
	ProjectID                              uint32
	DataFormat, QuotaBytes, QuotaInodes    int64
	ExpectedDevice, ExpectedNamespaceInode uint64
	WriterFence                            int64
}

func (r DeleteRequest) valid() bool {
	return namespaceID.MatchString(r.NamespaceID) && sha256Value(r.ReleaseSHA256) &&
		r.ProjectID > 0 && r.DataFormat > 0 && r.QuotaBytes > 0 &&
		r.QuotaBytes%512 == 0 && r.QuotaInodes > 0 &&
		r.ExpectedDevice > 0 && r.ExpectedNamespaceInode > 0 &&
		r.WriterFence > 0
}

// DeleteSnapshot is minted only after the complete namespace tree and project
// quota are absent. AlreadyAbsent supports exact receipt reconciliation.
type DeleteSnapshot struct {
	NamespaceID, ReleaseSHA256 string
	ProjectID                  uint32
	DataFormat                 int64
	Entries, UsedBytes         int64
	UsedInodes                 int64
	NamespaceDevice            uint64
	NamespaceInode             uint64
	WriterFence                int64
	AlreadyAbsent              bool
	ObservedMS                 int64
}

type DeleteObservation struct {
	snapshot DeleteSnapshot
	valid    bool
}

func (DeleteObservation) MarshalJSON() ([]byte, error) { return nil, ErrDeleteUnsafe }
func (o *DeleteObservation) UnmarshalJSON([]byte) error {
	*o = DeleteObservation{}
	return ErrDeleteUnsafe
}

func (o DeleteObservation) Snapshot() (DeleteSnapshot, error) {
	s := o.snapshot
	if !o.valid || !namespaceID.MatchString(s.NamespaceID) ||
		!sha256Value(s.ReleaseSHA256) || s.ProjectID == 0 ||
		s.DataFormat < 1 || s.WriterFence < 1 ||
		s.Entries < 0 || s.UsedBytes < 0 || s.UsedInodes < 0 ||
		(!s.AlreadyAbsent &&
			(s.NamespaceDevice == 0 || s.NamespaceInode == 0)) ||
		s.ObservedMS <= 0 {
		return DeleteSnapshot{}, ErrDeleteUnsafe
	}
	return s, nil
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

// ImportProvenance is Portal's record of the host that produced a checkpoint.
// It is compared with the archive manifest and never with the host receiving
// the import, which has a different filesystem, root inode and device.
type ImportProvenance struct {
	ProjectID              uint32
	NamespaceInode, Device uint64
	Source, Checkpoint     RootIdentity
}

// ImportRequest binds one retained checkpoint archive to a namespace that does
// not yet exist on the receiving root. It contains no host path. ProjectID and
// the quota are the receiving host's reservation, not the old host's.
type ImportRequest struct {
	CheckpointID, NamespaceID, ReleaseSHA256, ManifestSHA256 string
	ProjectID                                                uint32
	DataFormat, ArchiveBytes, MaxArchiveBytes, WriterFence   int64
	QuotaBytes, QuotaInodes                                  int64
	Provenance                                               ImportProvenance
}

func (r ImportRequest) valid() bool {
	p := r.Provenance
	return namespaceID.MatchString(r.CheckpointID) && namespaceID.MatchString(r.NamespaceID) &&
		sha256Value(r.ReleaseSHA256) && sha256Value(r.ManifestSHA256) &&
		r.ProjectID > 0 && r.DataFormat > 0 && r.ArchiveBytes > 0 &&
		r.MaxArchiveBytes > 0 && r.ArchiveBytes <= r.MaxArchiveBytes && r.WriterFence > 0 &&
		r.QuotaBytes > 0 && r.QuotaBytes%512 == 0 && r.QuotaInodes > 0 &&
		p.ProjectID > 0 && p.NamespaceInode > 0 && p.Device > 0 &&
		p.Source.valid() && p.Checkpoint.valid() && p.Source != p.Checkpoint
}

// importFootprint is the conservative quota charge of an archive on the
// receiving filesystem. XFS charges whole blocks, so every file rounds up to the
// block size and every directory is charged one block; the namespace root takes
// one block and one inode. Over-counting only refuses an import that would have
// fit; under-counting would leave a half-written namespace behind.
type importFootprint struct {
	block, bytes, inodes int64
	overflow             bool
}

func newImportFootprint(block int64) *importFootprint {
	f := &importFootprint{block: block}
	if block <= 0 || block&(block-1) != 0 {
		f.overflow = true
		return f
	}
	f.bytes, f.inodes = block, 1
	return f
}

func (f *importFootprint) charge(bytes int64) {
	if f.overflow || bytes < 0 || f.bytes > math.MaxInt64-bytes || f.inodes == math.MaxInt64 {
		f.overflow = true
		return
	}
	f.bytes += bytes
	f.inodes++
}

func (f *importFootprint) addDirectory() { f.charge(f.block) }

func (f *importFootprint) addFile(size int64) {
	if f.overflow || size < 0 || size > math.MaxInt64-f.block {
		f.overflow = true
		return
	}
	f.charge((size + f.block - 1) / f.block * f.block)
}

func (f *importFootprint) fits(quotaBytes, quotaInodes int64) error {
	if f.overflow || f.bytes > quotaBytes || f.inodes > quotaInodes {
		return ErrImportUnsafe
	}
	return nil
}

// ImportSnapshot is minted only after the namespace was created, extracted and
// read back entry by entry against the verified archive.
type ImportSnapshot struct {
	NamespaceID, ReleaseSHA256, ManifestSHA256 string
	ProjectID                                  uint32
	DataFormat, Entries, UsedBytes, UsedInodes int64
	ByteLimit, InodeLimit, ObservedMS          int64
	NamespaceDevice, NamespaceInode            uint64
}

// ImportObservation is not durable journal evidence or start authority.
type ImportObservation struct {
	snapshot ImportSnapshot
	valid    bool
}

func (ImportObservation) MarshalJSON() ([]byte, error) { return nil, ErrImportUnsafe }
func (o *ImportObservation) UnmarshalJSON([]byte) error {
	*o = ImportObservation{}
	return ErrImportUnsafe
}
func (o ImportObservation) Snapshot() (ImportSnapshot, error) {
	s := o.snapshot
	if !o.valid || !namespaceID.MatchString(s.NamespaceID) || !sha256Value(s.ReleaseSHA256) ||
		!sha256Value(s.ManifestSHA256) || s.ProjectID == 0 || s.DataFormat <= 0 ||
		s.NamespaceDevice == 0 || s.NamespaceInode == 0 || s.Entries < 0 ||
		s.UsedBytes < 0 || s.UsedInodes < 0 || s.ByteLimit <= 0 || s.InodeLimit <= 0 ||
		s.UsedBytes > s.ByteLimit || s.UsedInodes > s.InodeLimit || s.ObservedMS <= 0 {
		return ImportSnapshot{}, ErrImportUnsafe
	}
	return s, nil
}
