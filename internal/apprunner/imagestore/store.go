// Package imagestore is the runner-private scratch where image blobs wait between the blob channel and a load.
// Blobs are verified as they arrive, held only for one operation, and never handed to the Docker daemon as a
// path: the driver reads them back and re-hashes every byte it writes into the load archive.
package imagestore

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Refusal is the typed reason an upload is refused. The strings are the wire names of the blob channel.
type Refusal string

const (
	RefusalNone              Refusal = ""
	RefusalDigestMismatch    Refusal = "digest_mismatch"
	RefusalDiffIDMismatch    Refusal = "diff_id_mismatch"
	RefusalUncompressedBound Refusal = "uncompressed_bound"
	RefusalSizeBound         Refusal = "size_bound"
	RefusalMediaType         Refusal = "media_type"
	RefusalScratchFull       Refusal = "scratch_full"
	RefusalBusy              Refusal = "busy"
)

var (
	ErrUnsafeRoot  = errors.New("image scratch root is not a private owned directory")
	ErrInvalidBlob = errors.New("invalid image blob request")
	ErrScratchBusy = errors.New("image blob is already being uploaded")
	uuidPattern    = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// MaxScratchBytes bounds everything the scratch holds at once. One operation carries at most a package's worth of
// blobs, so two concurrent operations fit with room for partial uploads.
const MaxScratchBytes = 1 << 30

// MaxConcurrentUploads is the bound on simultaneous blob sessions.
const MaxConcurrentUploads = 2

type Store struct {
	root string

	limit int64

	mu       sync.Mutex
	used     int64
	active   map[string]bool // operation/digest pairs with an upload in flight
	sessions int
	// opBytes is the single owner of the byte accounting: used is always the sum of it. Every charge and refund
	// goes through charge and refund, keyed by operation.
	opBytes    map[string]int64
	opSessions map[string]int
	pins       map[string]int
	// lastUse is the last time an operation had an upload session, a load hold or a read: eviction takes only
	// operations idle for at least idle.
	lastUse map[string]time.Time
	idle    time.Duration
	trashN  int
}

// EvictIdle is how long an operation must have had no upload session and no load hold before its scratch may be
// evicted to make room. A reservation that cannot be satisfied otherwise is answered scratch_full, which is
// retryable: the Portal serializes its uploads (PR 3b), so a later attempt finds the room.
const EvictIdle = 10 * time.Minute

const refusalsDir = ".refusals"

// MaxRefusalRecords bounds the durable refusal records. They are tiny and exempt from eviction.
const MaxRefusalRecords = 1024

// Open validates the scratch root and removes everything left in it: scratch never outlives the runner process,
// and a load that was interrupted learns of the loss through its read-back and re-uploads.
func Open(root string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrUnsafeRoot
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !ownedByEffectiveUser(info) {
		return nil, ErrUnsafeRoot
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, ErrUnsafeRoot
	}
	for _, entry := range entries {
		// Refusal records outlive eviction and a restart: they name a blob that can never pass, which is a fact
		// about the bytes the sender holds, not about scratch.
		if entry.Name() == refusalsDir {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return nil, ErrUnsafeRoot
		}
	}
	if err := os.Mkdir(filepath.Join(root, refusalsDir), 0700); err != nil && !os.IsExist(err) {
		return nil, ErrUnsafeRoot
	}
	return &Store{root: root, limit: MaxScratchBytes, active: map[string]bool{}, opBytes: map[string]int64{},
		opSessions: map[string]int{}, pins: map[string]int{}, lastUse: map[string]time.Time{}, idle: EvictIdle}, nil
}

// charge and refund are the only places the accounting changes.
func (s *Store) charge(operation string, n int64) {
	s.mu.Lock()
	s.opBytes[operation] += n
	s.used += n
	s.mu.Unlock()
}

func (s *Store) refund(operation string, n int64) {
	s.mu.Lock()
	s.refundLocked(operation, n)
	s.mu.Unlock()
}

func (s *Store) refundLocked(operation string, n int64) {
	if n > s.opBytes[operation] {
		n = s.opBytes[operation]
	}
	s.opBytes[operation] -= n
	s.used -= n
}

func (s *Store) touch(operation string) {
	s.mu.Lock()
	s.lastUse[operation] = time.Now()
	s.mu.Unlock()
}

// Hold keeps an operation's scratch from being evicted while a load reads it.
func (s *Store) Hold(operation string) (release func()) {
	s.mu.Lock()
	s.pins[operation]++
	s.lastUse[operation] = time.Now()
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.pins[operation]--; s.pins[operation] <= 0 {
				delete(s.pins, operation)
			}
			s.lastUse[operation] = time.Now()
			s.mu.Unlock()
		})
	}
}

// reserve charges n bytes to an operation. When the cap would be exceeded it first evicts whole operation
// directories, least recently active first, that have had no upload session and no load hold for at least s.idle.
// Eviction is safe because an unknown load can always re-upload what it lost through blobs_missing. Under the lock
// a victim is only renamed to a trash name; the removal runs after the lock is released. When nothing may be
// evicted the answer is the retryable scratch_full.
func (s *Store) reserve(operation string, n int64) error {
	var trash []string
	defer func() {
		for _, dir := range trash {
			_ = os.RemoveAll(dir)
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.used+n > s.limit {
		var victims []string
		now := time.Now()
		for op := range s.opBytes {
			if op != operation && s.opSessions[op] == 0 && s.pins[op] == 0 && now.Sub(s.lastUse[op]) >= s.idle {
				victims = append(victims, op)
			}
		}
		if len(victims) == 0 {
			return errors.New("scratch full")
		}
		sort.Slice(victims, func(i, j int) bool { return s.lastUse[victims[i]].Before(s.lastUse[victims[j]]) })
		victim := victims[0]
		s.trashN++
		dest := filepath.Join(s.root, ".trash-"+strconv.Itoa(s.trashN))
		if err := os.Rename(filepath.Join(s.root, victim), dest); err != nil {
			return errors.New("scratch full")
		}
		trash = append(trash, dest)
		s.used -= s.opBytes[victim]
		delete(s.opBytes, victim)
		delete(s.lastUse, victim)
	}
	s.opBytes[operation] += n
	s.used += n
	return nil
}

func (s *Store) operationDir(operation string) (string, error) {
	if !uuidPattern.MatchString(operation) {
		return "", ErrInvalidBlob
	}
	return filepath.Join(s.root, operation), nil
}

func (s *Store) ensureOperationDir(operation string) (string, error) {
	dir, err := s.operationDir(operation)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !ownedByEffectiveUser(info) {
		return "", ErrUnsafeRoot
	}
	return dir, nil
}

// Used is the number of scratch bytes accounted.
func (s *Store) Used() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// record is the verification record written next to a blob after it passed its checks. A blob counts for a role
// only when its record equals that role.
type record struct {
	Kind              string `json:"kind"`
	MediaType         string `json:"media_type"`
	DiffID            string `json:"diff_id"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
	SHA256            string `json:"sha256"`
}

func recordFor(blob appplatform.ImageBlob) record {
	return record{Kind: string(blob.Kind), MediaType: blob.MediaType, DiffID: blob.DiffID, UncompressedBytes: blob.UncompressedBytes, SHA256: blob.Digest}
}

func (s *Store) readRecord(operation, digest string) (record, bool) {
	dir, err := s.operationDir(operation)
	if err != nil || !digestPattern.MatchString(digest) {
		return record{}, false
	}
	file, err := os.OpenFile(filepath.Join(dir, digest+".rec"), os.O_RDONLY|noFollow, 0)
	if err != nil {
		return record{}, false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	var r record
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &r) != nil {
		return record{}, false
	}
	return r, true
}

// Verified reports whether the operation holds this blob in exactly this role: the file is there and its
// verification record equals the expected kind, media type, diff_id, uncompressed size and digest.
func (s *Store) Verified(operation string, expected appplatform.ImageBlob) bool {
	if !s.Has(operation, expected.Digest) {
		return false
	}
	r, ok := s.readRecord(operation, expected.Digest)
	return ok && r == recordFor(expected)
}

// hasKind is Verified for a role whose media type is not yet known (the manifest and the config).
func (s *Store) hasKind(operation, digest string, kind appplatform.ImageBlobKind) bool {
	if !s.Has(operation, digest) {
		return false
	}
	r, ok := s.readRecord(operation, digest)
	return ok && r.Kind == string(kind) && r.SHA256 == digest
}

func writeRecord(dir string, blob appplatform.ImageBlob) error {
	raw, err := json.Marshal(recordFor(blob))
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, blob.Digest+".rec.tmp")
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, blob.Digest+".rec"))
}

// Has reports whether a complete blob file is stored for the operation. It says nothing about its role.
func (s *Store) Has(operation, digest string) bool {
	dir, err := s.operationDir(operation)
	if err != nil || !digestPattern.MatchString(digest) {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, digest))
	return err == nil && info.Mode().IsRegular()
}

// Open returns a stored blob for reading. It never follows a link.
func (s *Store) Open(operation, digest string) (io.ReadCloser, error) {
	dir, err := s.operationDir(operation)
	if err != nil || !digestPattern.MatchString(digest) {
		return nil, ErrInvalidBlob
	}
	s.touch(operation)
	file, err := os.OpenFile(filepath.Join(dir, digest), os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrInvalidBlob
	}
	return file, nil
}

// Source adapts one operation's scratch to the driver's blob source.
func (s *Store) Source(operation string) OperationSource {
	return OperationSource{store: s, operation: operation}
}

// OperationSource is one operation's scratch, read by the driver while it builds the load archive.
type OperationSource struct {
	store     *Store
	operation string
}

func (o OperationSource) Open(digest string) (io.ReadCloser, error) {
	return o.store.Open(o.operation, digest)
}

// Verified tells the driver whether a blob is held in exactly the role the plan gives it.
func (o OperationSource) Verified(expected appplatform.ImageBlob) bool {
	return o.store.Verified(o.operation, expected)
}

// Missing lists the blobs of an image entry the operation's scratch does not hold in their role: the manifest and
// config first, then, once the manifest is readable, the layers it names. A blob stored under another kind, or whose
// record differs from what the plan says, is missing for that role and must be uploaded again under the right kind.
func (s *Store) Missing(operation string, entry appplatform.BackendImage) []string {
	s.touch(operation)
	var missing []string
	if !s.hasKind(operation, entry.ManifestDigest, appplatform.ImageBlobManifest) {
		missing = append(missing, entry.ManifestDigest)
	}
	if !s.hasKind(operation, entry.ConfigDigest, appplatform.ImageBlobConfig) {
		missing = append(missing, entry.ConfigDigest)
	}
	if len(missing) != 0 {
		return missing
	}
	read := func(digest string) []byte {
		file, err := s.Open(operation, digest)
		if err != nil {
			return nil
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, appplatform.MaxImageMetadataBytes+1))
		if err != nil {
			return nil
		}
		return raw
	}
	plan, err := appplatform.PlanImage(entry, read(entry.ManifestDigest), read(entry.ConfigDigest))
	if err != nil {
		// A stored manifest or config that no longer matches the entry is as good as absent.
		return []string{entry.ManifestDigest, entry.ConfigDigest}
	}
	if !s.Verified(operation, plan.Manifest) {
		missing = append(missing, entry.ManifestDigest)
	}
	if !s.Verified(operation, plan.Config) {
		missing = append(missing, entry.ConfigDigest)
	}
	for _, layer := range plan.Layers {
		if !s.Verified(operation, layer) {
			missing = append(missing, layer.Digest)
		}
	}
	return missing
}

// Refused reports whether the runner already refused, for good, a blob this image needs in its role: an upload
// that failed verification on a complete stream. Such a blob can never pass, so a load that waits for it would
// wait for ever.
func (s *Store) Refused(operation string, entry appplatform.BackendImage) bool {
	if s.refusedAs(operation, appplatform.ImageBlob{Digest: entry.ManifestDigest, Kind: appplatform.ImageBlobManifest}, false) ||
		s.refusedAs(operation, appplatform.ImageBlob{Digest: entry.ConfigDigest, Kind: appplatform.ImageBlobConfig}, false) {
		return true
	}
	if !s.hasKind(operation, entry.ManifestDigest, appplatform.ImageBlobManifest) || !s.hasKind(operation, entry.ConfigDigest, appplatform.ImageBlobConfig) {
		return false
	}
	read := func(digest string) []byte {
		file, err := s.Open(operation, digest)
		if err != nil {
			return nil
		}
		defer file.Close()
		raw, _ := io.ReadAll(io.LimitReader(file, appplatform.MaxImageMetadataBytes+1))
		return raw
	}
	plan, err := appplatform.PlanImage(entry, read(entry.ManifestDigest), read(entry.ConfigDigest))
	if err != nil {
		return false
	}
	for _, layer := range plan.Layers {
		if s.refusedAs(operation, layer, true) {
			return true
		}
	}
	return false
}

// refusalPath names a refusal by operation, digest, kind and a hash of the declaration, so a refusal made under
// one declaration is never overwritten by (or mistaken for) one made under another.
func (s *Store) refusalPath(operation string, blob appplatform.ImageBlob) string {
	decl := sha256.Sum256([]byte(blob.MediaType + "|" + blob.DiffID + "|" + strconv.FormatInt(blob.UncompressedBytes, 10) + "|" + strconv.FormatInt(blob.Size, 10)))
	return filepath.Join(s.root, refusalsDir, operation+"."+blob.Digest+"."+string(blob.Kind)+"."+hex.EncodeToString(decl[:6]))
}

func (s *Store) refusalGlob(operation, digest string, kind appplatform.ImageBlobKind) string {
	return filepath.Join(s.root, refusalsDir, operation+"."+digest+"."+string(kind)+".*")
}

// refusalRecord is a definite refusal together with the whole declaration it was made under. A refusal counts for
// a role only when that declaration equals the role exactly: an upload that declared the wrong diff_id or size says
// nothing about bytes that are valid under the right declaration.
type refusalRecord struct {
	Reason            string `json:"reason"`
	Kind              string `json:"kind"`
	MediaType         string `json:"media_type"`
	DiffID            string `json:"diff_id"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
	Size              int64  `json:"size"`
	Digest            string `json:"digest"`
}

func readRefusalFile(path string) (refusalRecord, bool) {
	raw, err := os.ReadFile(path)
	var r refusalRecord
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &r) != nil {
		return refusalRecord{}, false
	}
	return r, true
}

// refusedAs reports a refusal whose declaration equals the expected role. For the manifest and config the media
// type and size are not known before the manifest is read, so any refusal of that digest and kind counts there.
func (s *Store) refusedAs(operation string, expected appplatform.ImageBlob, exact bool) bool {
	if !uuidPattern.MatchString(operation) || !digestPattern.MatchString(expected.Digest) {
		return false
	}
	if exact {
		r, ok := readRefusalFile(s.refusalPath(operation, expected))
		return ok && r.Digest == expected.Digest && r.Kind == string(expected.Kind) && r.MediaType == expected.MediaType &&
			r.DiffID == expected.DiffID && r.UncompressedBytes == expected.UncompressedBytes && r.Size == expected.Size
	}
	matches, _ := filepath.Glob(s.refusalGlob(operation, expected.Digest, expected.Kind))
	for _, m := range matches {
		if r, ok := readRefusalFile(m); ok && r.Digest == expected.Digest && r.Kind == string(expected.Kind) {
			return true
		}
	}
	return false
}

// recordRefusal stores the typed reason a blob can never pass, outside the evictable scratch.
func (s *Store) recordRefusal(operation string, blob appplatform.ImageBlob, reason Refusal) {
	switch reason {
	case RefusalDigestMismatch, RefusalDiffIDMismatch, RefusalUncompressedBound, RefusalMediaType:
	default:
		return
	}
	dir := filepath.Join(s.root, refusalsDir)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) >= MaxRefusalRecords {
		sort.Slice(entries, func(i, j int) bool {
			a, _ := entries[i].Info()
			b, _ := entries[j].Info()
			return a != nil && b != nil && a.ModTime().Before(b.ModTime())
		})
		_ = os.Remove(filepath.Join(dir, entries[0].Name()))
	}
	raw, err := json.Marshal(refusalRecord{Reason: string(reason), Kind: string(blob.Kind), MediaType: blob.MediaType, DiffID: blob.DiffID,
		UncompressedBytes: blob.UncompressedBytes, Size: blob.Size, Digest: blob.Digest})
	if err == nil {
		_ = os.WriteFile(s.refusalPath(operation, blob), raw, 0600)
	}
}

func (s *Store) clearRefusals(operation string) {
	matches, _ := filepath.Glob(filepath.Join(s.root, refusalsDir, operation+".*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// RemoveOperation deletes an operation's scratch once its load has settled. Removing nothing is not an error. An
// operation with an upload in flight is left alone: the upload owns its bytes until it ends.
func (s *Store) RemoveOperation(operation string) error {
	dir, err := s.operationDir(operation)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opSessions[operation] > 0 {
		return ErrScratchBusy
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	s.refundLocked(operation, s.opBytes[operation])
	delete(s.opBytes, operation)
	delete(s.lastUse, operation)
	s.clearRefusals(operation)
	return nil
}

// Upload is one blob being received. It is not safe for concurrent use.
type Upload struct {
	store     *Store
	key       string
	blob      appplatform.ImageBlob
	final     string
	partial   string
	file      *os.File
	written   int64
	whole     hash.Hash
	layer     *layerSink
	operation string
	released  bool
	closed    bool
	// reservedLeft is the part of the reservation not yet written: the only thing an abort or a discard returns
	// beyond the bytes the partial holds, so a refund can never exceed what was charged.
	reservedLeft int64
}

// Begin starts or resumes an upload. A blob already stored is reported as complete. A resume offset must equal the
// length of the partial the scratch holds; an offset of zero restarts the blob. The returned offset is where the
// sender must continue: it differs from the request only when the sender asked to resume at the wrong place.
func (s *Store) Begin(operation string, blob appplatform.ImageBlob, resume int64) (*Upload, int64, bool, Refusal, error) {
	if !digestPattern.MatchString(blob.Digest) || blob.Size < 1 || resume < 0 || resume > blob.Size {
		return nil, 0, false, RefusalNone, ErrInvalidBlob
	}
	if refusal := boundsRefusal(blob); refusal != RefusalNone {
		if uuidPattern.MatchString(operation) {
			s.recordRefusal(operation, blob, refusal)
		}
		return nil, 0, false, refusal, nil
	}
	dir, err := s.ensureOperationDir(operation)
	if err != nil {
		return nil, 0, false, RefusalNone, err
	}
	key := operation + "/" + blob.Digest
	s.mu.Lock()
	if s.active[key] || s.sessions >= MaxConcurrentUploads {
		s.mu.Unlock()
		return nil, 0, false, RefusalBusy, nil
	}
	s.active[key] = true
	s.sessions++
	s.opSessions[operation]++
	s.lastUse[operation] = time.Now()
	s.mu.Unlock()
	u := &Upload{store: s, key: key, operation: operation, blob: blob, final: filepath.Join(dir, blob.Digest), partial: filepath.Join(dir, blob.Digest+".partial")}
	fail := func(err error) (*Upload, int64, bool, Refusal, error) {
		u.release()
		return nil, 0, false, RefusalNone, err
	}
	if info, err := os.Lstat(u.final); err == nil && info.Mode().IsRegular() {
		if info.Size() == blob.Size && s.Verified(operation, blob) {
			u.release()
			return nil, blob.Size, true, RefusalNone, nil
		}
		// Stored under another role (or damaged): it does not count for this one. Replace it.
		_ = os.Remove(u.final)
		_ = os.Remove(filepath.Join(dir, blob.Digest+".rec"))
		s.refund(operation, info.Size())
	}
	length := int64(0)
	if info, err := os.Lstat(u.partial); err == nil {
		if !info.Mode().IsRegular() {
			return fail(ErrUnsafeRoot)
		}
		length = info.Size()
	}
	if length > blob.Size || resume == 0 && length != 0 {
		// A restart (or an oversized leftover) discards the partial.
		s.discardPartial(operation, u.partial, length)
		length = 0
	}
	if resume != 0 && resume != length {
		u.release()
		return nil, length, false, RefusalNone, nil
	}
	if err := s.reserve(operation, blob.Size-length); err != nil {
		u.release()
		return nil, 0, false, RefusalScratchFull, nil
	}
	u.reservedLeft = blob.Size - length
	// From here the partial's existing bytes are this upload's to refund too.
	u.written = length
	file, err := os.OpenFile(u.partial, os.O_RDWR|os.O_CREATE|noFollow, 0600)
	if err != nil {
		u.refundAll()
		return fail(err)
	}
	u.file = file
	u.whole = sha256.New()
	if blob.Kind == appplatform.ImageBlobLayer {
		compressed, _ := appplatform.ImageBlobCompressed(blob.MediaType)
		u.layer = newLayerSink(compressed, blob.DiffID, blob.UncompressedBytes)
	}
	if length > 0 {
		// The hashers are rebuilt from the bytes already stored: nothing about a prefix is trusted.
		if _, err := io.Copy(io.MultiWriter(u.whole, sinkWriter{u.layer}), io.LimitReader(file, length)); err != nil {
			// A stored prefix that already breaks a bound is discarded, or the same refusal would repeat forever.
			refusal := Refusal(err.Error())
			if refusal != RefusalDiffIDMismatch && refusal != RefusalUncompressedBound {
				u.Abort()
				return nil, 0, false, RefusalNone, err
			}
			u.refuse(refusal)
			u.discard()
			return nil, 0, false, refusal, nil
		}
	}
	if _, err := file.Seek(length, io.SeekStart); err != nil {
		u.Abort()
		return nil, 0, false, RefusalNone, err
	}
	return u, length, false, RefusalNone, nil
}

func boundsRefusal(blob appplatform.ImageBlob) Refusal {
	switch blob.Kind {
	case appplatform.ImageBlobManifest, appplatform.ImageBlobConfig:
		if blob.Size > appplatform.MaxImageMetadataBytes {
			return RefusalSizeBound
		}
	case appplatform.ImageBlobLayer:
		if _, ok := appplatform.ImageBlobCompressed(blob.MediaType); !ok {
			return RefusalMediaType
		}
		if blob.Size > appplatform.MaxImageBlobBytes {
			return RefusalSizeBound
		}
		if blob.UncompressedBytes < 1 || blob.UncompressedBytes > appplatform.MaxImageLayerUncompressedBytes || !digestPattern.MatchString(blob.DiffID) {
			return RefusalUncompressedBound
		}
	default:
		return RefusalMediaType
	}
	return RefusalNone
}

func (s *Store) discardPartial(operation, path string, length int64) {
	_ = os.Remove(path)
	s.refund(operation, length)
}

// refundAll returns everything this upload accounts: the partial's bytes and the unwritten reservation.
func (u *Upload) refundAll() { u.store.refund(u.operation, u.written+u.reservedLeft) }

// clearRefusal drops every refusal of this blob in this role: a correct upload supersedes them.
func (u *Upload) clearRefusal() {
	matches, _ := filepath.Glob(u.store.refusalGlob(u.operation, u.blob.Digest, u.blob.Kind))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// refuse records a definite verification refusal for this blob in its role.
func (u *Upload) refuse(reason Refusal) { u.store.recordRefusal(u.operation, u.blob, reason) }

func (u *Upload) release() {
	if u.released {
		return
	}
	u.released = true
	u.store.mu.Lock()
	delete(u.store.active, u.key)
	u.store.sessions--
	if u.store.opSessions[u.operation]--; u.store.opSessions[u.operation] <= 0 {
		delete(u.store.opSessions, u.operation)
	}
	u.store.lastUse[u.operation] = time.Now()
	u.store.mu.Unlock()
}

// Write appends one chunk. A refusal is final for this upload: the partial is discarded and the upload is closed.
func (u *Upload) Write(chunk []byte) Refusal {
	if u.written+int64(len(chunk)) > u.blob.Size {
		u.discard()
		return RefusalSizeBound
	}
	if _, err := u.file.Write(chunk); err != nil {
		u.discard()
		return RefusalScratchFull
	}
	_, _ = u.whole.Write(chunk)
	if u.layer != nil {
		if refusal := u.layer.write(chunk); refusal != RefusalNone {
			u.refuse(refusal)
			u.discard()
			return refusal
		}
	}
	u.written += int64(len(chunk))
	u.reservedLeft -= int64(len(chunk))
	return RefusalNone
}

// Written is the number of bytes the partial holds.
func (u *Upload) Written() int64 { return u.written }

// Commit verifies the whole blob and moves it to its final name. A mismatch is a refusal and discards the partial.
func (u *Upload) Commit(wholeSHA256 string, total int64) Refusal {
	if total != u.blob.Size || u.written != u.blob.Size {
		u.discard()
		return RefusalSizeBound
	}
	sum := hex.EncodeToString(u.whole.Sum(nil))
	if sum != u.blob.Digest || sum != wholeSHA256 {
		u.refuse(RefusalDigestMismatch)
		u.discard()
		return RefusalDigestMismatch
	}
	if u.layer != nil {
		if refusal := u.layer.finish(); refusal != RefusalNone {
			u.refuse(refusal)
			u.discard()
			return refusal
		}
	}
	if err := u.file.Sync(); err != nil {
		u.discard()
		return RefusalScratchFull
	}
	if err := u.file.Close(); err != nil {
		u.discard()
		return RefusalScratchFull
	}
	u.file = nil
	// The record is written atomically before the blob takes its final name, so a blob without a record never
	// counts for any role.
	if err := writeRecord(filepath.Dir(u.final), u.blob); err != nil {
		u.discard()
		return RefusalScratchFull
	}
	if err := os.Rename(u.partial, u.final); err != nil {
		u.discard()
		return RefusalScratchFull
	}
	u.clearRefusal()
	u.closed = true
	u.release()
	return RefusalNone
}

// Abort keeps a partial for a later resume: a lost connection is not a verdict on the bytes already stored.
func (u *Upload) Abort() {
	if u.closed {
		return
	}
	u.closed = true
	if u.layer != nil {
		u.layer.abort()
	}
	if u.file != nil {
		u.file.Close()
		u.file = nil
	}
	// Bytes the partial already holds stay accounted; the unwritten remainder of the reservation is returned.
	u.store.refund(u.operation, u.reservedLeft)
	u.release()
}

// Discard drops the partial and closes the upload: the sender broke a declared bound, which no resume can repair.
func (u *Upload) Discard() { u.discard() }

func (u *Upload) discard() {
	if u.closed {
		return
	}
	u.closed = true
	if u.layer != nil {
		u.layer.abort()
	}
	if u.file != nil {
		u.file.Close()
		u.file = nil
	}
	_ = os.Remove(u.partial)
	u.refundAll()
	u.release()
}

type sinkWriter struct{ sink *layerSink }

func (w sinkWriter) Write(p []byte) (int, error) {
	if w.sink != nil {
		if refusal := w.sink.write(p); refusal != RefusalNone {
			return 0, errors.New(string(refusal))
		}
	}
	return len(p), nil
}

// layerSink decompresses a layer as it arrives and checks the diff_id, stopping at the declared uncompressed size,
// so a layer can never expand beyond its declaration. The tar is never written anywhere.
type layerSink struct {
	compressed bool
	diffID     string
	bound      int64
	writer     *io.PipeWriter
	done       chan layerVerdict
	plain      hash.Hash
	plainBytes int64
	failed     Refusal
	// collected records that the goroutine's verdict was received, so abort never waits for it twice.
	collected bool
}

type layerVerdict struct {
	sum     string
	bytes   int64
	refusal Refusal
}

func newLayerSink(compressed bool, diffID string, bound int64) *layerSink {
	s := &layerSink{compressed: compressed, diffID: diffID, bound: bound}
	if !compressed {
		s.plain = sha256.New()
		return s
	}
	reader, writer := io.Pipe()
	s.writer, s.done = writer, make(chan layerVerdict, 1)
	go func() {
		verdict := layerVerdict{}
		zr, err := gzip.NewReader(reader)
		if err != nil {
			reader.CloseWithError(err)
			verdict.refusal = RefusalDiffIDMismatch
			s.done <- verdict
			return
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(zr, bound+1))
		switch {
		case n > bound:
			verdict.refusal = RefusalUncompressedBound
			// Closing the read side makes any later Write fail instead of blocking on a reader that stopped.
			reader.CloseWithError(errors.New(string(RefusalUncompressedBound)))
		case err != nil:
			verdict.refusal = RefusalDiffIDMismatch
			reader.CloseWithError(err)
		default:
			// Anything after the compressed stream's end is not part of the layer.
			if rest, _ := io.Copy(io.Discard, reader); rest > 0 {
				verdict.refusal = RefusalDiffIDMismatch
			}
		}
		verdict.sum, verdict.bytes = hex.EncodeToString(h.Sum(nil)), n
		s.done <- verdict
	}()
	return s
}

func (s *layerSink) write(chunk []byte) Refusal {
	if s.failed != RefusalNone {
		return s.failed
	}
	if !s.compressed {
		s.plainBytes += int64(len(chunk))
		if s.plainBytes > s.bound {
			s.failed = RefusalUncompressedBound
			return s.failed
		}
		_, _ = s.plain.Write(chunk)
		return RefusalNone
	}
	if _, err := s.writer.Write(chunk); err != nil {
		// The reader stopped: it knows why. Collect its verdict instead of guessing from the pipe error.
		s.writer.Close()
		verdict := <-s.done
		s.collected = true
		s.failed = verdict.refusal
		if s.failed == RefusalNone {
			s.failed = RefusalDiffIDMismatch
		}
		return s.failed
	}
	return RefusalNone
}

func (s *layerSink) finish() Refusal {
	if s.failed != RefusalNone {
		return s.failed
	}
	if !s.compressed {
		if hex.EncodeToString(s.plain.Sum(nil)) != s.diffID || s.plainBytes != s.bound {
			return RefusalDiffIDMismatch
		}
		return RefusalNone
	}
	s.writer.Close()
	verdict := <-s.done
	s.collected = true
	switch {
	case verdict.refusal != RefusalNone:
		return verdict.refusal
	case verdict.sum != s.diffID || verdict.bytes != s.bound:
		return RefusalDiffIDMismatch
	}
	return RefusalNone
}

func (s *layerSink) abort() {
	if s.compressed && s.writer != nil && !s.collected {
		s.collected = true
		s.writer.CloseWithError(io.ErrClosedPipe)
		<-s.done
	}
}
