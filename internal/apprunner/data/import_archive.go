package data

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// The import archive is the administrator data import of doc 185 section 5. An operator packs a prepared tree on
// the runner host with `app-runner pack-import`; the archive waits in the import inbox until a reviewed
// import_data operation replaces a namespace with it.
//
// Its layout is the checkpoint layout, so the restore reader and extractor are reused unchanged: one tar whose
// first entry is a manifest, then directories (names ending in "/") and regular files in ascending name order. The
// first entry keeps the checkpoint manifest's entry name; its content tells the two kinds of archive apart.
const (
	// ImportInboxDirectory is the directory under the checkpoint root that holds packed archives. It is
	// root-owned and mode 0700.
	ImportInboxDirectory = ".portal-imports"
	// ImportManifestFormat is the format of the manifest in the first entry.
	ImportManifestFormat = "portal.app/data-import/1"
	// ArchiveManifestEntry is the name of the first entry of every checkpoint and import archive.
	ArchiveManifestEntry = ".portal-checkpoint-manifest.json"
	// ImportManifestBlockBytes is the whole first entry on disk: one 512-byte header and one 512-byte data
	// block. The manifest is small enough to always fit one block, so the packer can write the body first
	// and the manifest last without moving a byte.
	ImportManifestBlockBytes = 1024
	// ImportOwner is the owner every imported entry takes (doc 185 section 5.1).
	ImportOwner = 65532
	// ImportDirectoryMode and ImportFileMode are the normalised permissions of imported entries.
	ImportDirectoryMode = 0750
	ImportFileMode      = 0640
	// MaxImportManifestBytes bounds the manifest JSON to one data block.
	MaxImportManifestBytes = 512
)

var ErrImportArchiveInvalid = errors.New("import archive manifest or layout is invalid")

// ImportManifest is the content of the first entry of an import archive.
//
// BodySHA256 is the SHA-256 of every byte that follows the first entry, up to the end of the file. It is the
// archive's identity: the packer prints it, the administrator copies it into the review, and the runner finds
// the archive by it without reading the whole file. Entries counts directories and files; Bytes is the total
// size of file content.
type ImportManifest struct {
	Format     string `json:"format"`
	Entries    int64  `json:"entries"`
	Bytes      int64  `json:"bytes"`
	BodySHA256 string `json:"body_sha256"`
	Packer     string `json:"packer"`
}

// Valid reports whether the manifest is well formed.
func (m ImportManifest) Valid() bool {
	return m.Format == ImportManifestFormat && m.Entries >= 0 && m.Bytes >= 0 && sha256Value(m.BodySHA256) &&
		m.Packer != "" && len(m.Packer) <= 64 && !strings.ContainsAny(m.Packer, "\x00\r\n")
}

// MarshalImportManifest returns the canonical manifest bytes: what ParseImportManifest accepts.
func MarshalImportManifest(m ImportManifest) ([]byte, error) {
	if !m.Valid() {
		return nil, ErrImportArchiveInvalid
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxImportManifestBytes {
		return nil, ErrImportArchiveInvalid
	}
	return raw, nil
}

// ParseImportManifest decodes a manifest and refuses anything but the canonical encoding of a valid one.
func ParseImportManifest(raw []byte) (ImportManifest, error) {
	var m ImportManifest
	if len(raw) == 0 || len(raw) > MaxImportManifestBytes {
		return m, ErrImportArchiveInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || !m.Valid() {
		return ImportManifest{}, ErrImportArchiveInvalid
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ImportManifest{}, ErrImportArchiveInvalid
	}
	return m, nil
}

// ImportInboxArchive is what the runner reports about one archive in the inbox.
type ImportInboxArchive struct {
	// Found is false when no archive carries the digest.
	Found bool
	// Bytes is the file size.
	Bytes    int64
	Manifest ImportManifest
	// SchemaMarker is the value of the archive's .portal-data-schema entry, zero when it has none.
	SchemaMarker int
}

// ImportDataRequest binds one inbox archive to the existing namespace whose contents it replaces. It carries no
// host path: the archive is found by the digest the administrator reviewed.
type ImportDataRequest struct {
	NamespaceID string
	ProjectID   uint32
	// ArchiveSHA256 is the manifest's body digest; ArchiveBytes the file size; Entries and Bytes the manifest's
	// counts. All four were confirmed at review and are checked again before anything is cleared.
	ArchiveSHA256           string
	ArchiveBytes            int64
	Entries, Bytes          int64
	QuotaBytes, QuotaInodes int64
	// DataFormat is the data schema of the release the namespace belongs to. The archive's marker must equal it.
	DataFormat                             int64
	MaxArchiveBytes                        int64
	ExpectedDevice, ExpectedNamespaceInode uint64
	WriterFence                            int64
}

func (r ImportDataRequest) valid() bool {
	return namespaceID.MatchString(r.NamespaceID) && r.ProjectID > 0 && sha256Value(r.ArchiveSHA256) &&
		r.ArchiveBytes > 0 && r.MaxArchiveBytes > 0 && r.ArchiveBytes <= r.MaxArchiveBytes &&
		r.Entries >= 0 && r.Bytes >= 0 && r.QuotaBytes > 0 && r.QuotaBytes%512 == 0 && r.QuotaInodes > 0 &&
		r.DataFormat >= 0 && r.ExpectedDevice > 0 && r.ExpectedNamespaceInode > 0 && r.WriterFence > 0
}

// ImportDataSnapshot is minted only after the namespace was cleared, extracted and read back entry by entry
// against the verified archive. UsedBytes and UsedInodes are what the project quota reports afterwards.
type ImportDataSnapshot struct {
	NamespaceID, ArchiveSHA256                 string
	ProjectID                                  uint32
	DataFormat, Entries, UsedBytes, UsedInodes int64
	ByteLimit, InodeLimit, ObservedMS          int64
	NamespaceDevice, NamespaceInode            uint64
}

// ImportDataObservation is not durable journal evidence or start authority.
type ImportDataObservation struct {
	snapshot ImportDataSnapshot
	valid    bool
}

func (ImportDataObservation) MarshalJSON() ([]byte, error) { return nil, ErrImportUnsafe }
func (o *ImportDataObservation) UnmarshalJSON([]byte) error {
	*o = ImportDataObservation{}
	return ErrImportUnsafe
}

// Snapshot returns the observation, or ErrImportUnsafe when it was not minted by a completed import.
func (o ImportDataObservation) Snapshot() (ImportDataSnapshot, error) {
	s := o.snapshot
	if !o.valid || !namespaceID.MatchString(s.NamespaceID) || !sha256Value(s.ArchiveSHA256) || s.ProjectID == 0 ||
		s.DataFormat < 0 || s.NamespaceDevice == 0 || s.NamespaceInode == 0 || s.Entries < 0 ||
		s.UsedBytes < 0 || s.UsedInodes < 0 || s.ByteLimit <= 0 || s.InodeLimit <= 0 ||
		s.UsedBytes > s.ByteLimit || s.UsedInodes > s.InodeLimit || s.ObservedMS <= 0 {
		return ImportDataSnapshot{}, ErrImportUnsafe
	}
	return s, nil
}

var (
	// ErrQuotaRaiseUnsafe is a raise whose binding, direction or live state does not hold. Nothing was changed.
	ErrQuotaRaiseUnsafe = errors.New("data quota raise binding is unsafe")
	// ErrQuotaRaiseIncomplete is a raise whose native limit change may have applied; it needs reconciliation.
	ErrQuotaRaiseIncomplete = errors.New("data quota raise requires reconciliation")
	// ErrQuotaLower is a request below the current limit. Version 1 only raises (doc 185 question 2).
	ErrQuotaLower = errors.New("data quota may only be raised")
	// ErrImportArchiveMissing is no inbox archive with the reviewed digest.
	ErrImportArchiveMissing = errors.New("no import archive has the reviewed digest")
	// ErrImportArchiveAmbiguous is more than one inbox archive with the reviewed digest.
	ErrImportArchiveAmbiguous = errors.New("more than one import archive has the reviewed digest")
	// ErrImportTooLarge is an archive whose footprint exceeds the namespace quota.
	ErrImportTooLarge = errors.New("import archive exceeds the namespace quota")
	// ErrImportFormatMismatch is an archive whose data schema marker is not the release's data format.
	ErrImportFormatMismatch = errors.New("import archive data format does not match the release")
)

// QuotaRaiseRequest binds one raise to an existing namespace. The old limits are what Portal and the journal
// recorded; the live limits must still equal them, so a hand-edited limit is never raised over.
type QuotaRaiseRequest struct {
	NamespaceID                            string
	ProjectID                              uint32
	OldBytes, OldInodes                    int64
	NewBytes, NewInodes                    int64
	ExpectedDevice, ExpectedNamespaceInode uint64
}

func (r QuotaRaiseRequest) valid() bool {
	return namespaceID.MatchString(r.NamespaceID) && r.ProjectID > 0 &&
		r.OldBytes > 0 && r.OldBytes%512 == 0 && r.OldInodes > 0 &&
		r.NewBytes > 0 && r.NewBytes%512 == 0 && r.NewInodes > 0 &&
		r.ExpectedDevice > 0 && r.ExpectedNamespaceInode > 0
}

// QuotaRaiseSnapshot is minted only after the larger limits were read back from the project quota.
type QuotaRaiseSnapshot struct {
	NamespaceID                     string
	ProjectID                       uint32
	ByteLimit, InodeLimit           int64
	UsedBytes, UsedInodes           int64
	AlreadyApplied                  bool
	ObservedMS                      int64
	NamespaceDevice, NamespaceInode uint64
}

// QuotaRaiseObservation is not durable journal evidence.
type QuotaRaiseObservation struct {
	snapshot QuotaRaiseSnapshot
	valid    bool
}

func (QuotaRaiseObservation) MarshalJSON() ([]byte, error) { return nil, ErrQuotaRaiseUnsafe }
func (o *QuotaRaiseObservation) UnmarshalJSON([]byte) error {
	*o = QuotaRaiseObservation{}
	return ErrQuotaRaiseUnsafe
}

// Snapshot returns the observation, or ErrQuotaRaiseUnsafe when it was not minted by a completed raise.
func (o QuotaRaiseObservation) Snapshot() (QuotaRaiseSnapshot, error) {
	s := o.snapshot
	if !o.valid || !namespaceID.MatchString(s.NamespaceID) || s.ProjectID == 0 || s.ByteLimit <= 0 ||
		s.InodeLimit <= 0 || s.UsedBytes < 0 || s.UsedInodes < 0 || s.NamespaceDevice == 0 ||
		s.NamespaceInode == 0 || s.ObservedMS <= 0 {
		return QuotaRaiseSnapshot{}, ErrQuotaRaiseUnsafe
	}
	return s, nil
}
