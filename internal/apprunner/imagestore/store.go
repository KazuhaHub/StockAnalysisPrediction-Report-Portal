// Package imagestore is the runner-private scratch where image blobs wait between the blob channel and a load.
// Blobs are verified as they arrive, held only for one operation, and never handed to the Docker daemon as a
// path: the driver reads them back and re-hashes every byte it writes into the load archive.
package imagestore

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

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
}

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
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return nil, ErrUnsafeRoot
		}
	}
	return &Store{root: root, limit: MaxScratchBytes, active: map[string]bool{}}, nil
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

// Has reports whether a complete, verified blob is stored for the operation.
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

// Missing lists the blobs of an image entry the operation's scratch does not yet hold: the manifest and config
// first, then, once the manifest is readable, the layers it names.
func (s *Store) Missing(operation string, entry appplatform.BackendImage) []string {
	var missing []string
	for _, digest := range []string{entry.ManifestDigest, entry.ConfigDigest} {
		if !s.Has(operation, digest) {
			missing = append(missing, digest)
		}
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
	for _, layer := range plan.Layers {
		if !s.Has(operation, layer.Digest) {
			missing = append(missing, layer.Digest)
		}
	}
	return missing
}

// RemoveOperation deletes an operation's scratch once its load has settled. Removing nothing is not an error.
func (s *Store) RemoveOperation(operation string) error {
	dir, err := s.operationDir(operation)
	if err != nil {
		return err
	}
	size := dirSize(dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	s.mu.Lock()
	s.used -= size
	if s.used < 0 {
		s.used = 0
	}
	s.mu.Unlock()
	return nil
}

func dirSize(dir string) int64 {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// Upload is one blob being received. It is not safe for concurrent use.
type Upload struct {
	store      *Store
	key        string
	blob       appplatform.ImageBlob
	final      string
	partial    string
	file       *os.File
	written    int64
	whole      hash.Hash
	layer      *layerSink
	released   bool
	closed     bool
	chargedAdd int64
}

// Begin starts or resumes an upload. A blob already stored is reported as complete. A resume offset must equal the
// length of the partial the scratch holds; an offset of zero restarts the blob. The returned offset is where the
// sender must continue: it differs from the request only when the sender asked to resume at the wrong place.
func (s *Store) Begin(operation string, blob appplatform.ImageBlob, resume int64) (*Upload, int64, bool, Refusal, error) {
	if !digestPattern.MatchString(blob.Digest) || blob.Size < 1 || resume < 0 || resume > blob.Size {
		return nil, 0, false, RefusalNone, ErrInvalidBlob
	}
	if refusal := boundsRefusal(blob); refusal != RefusalNone {
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
	s.mu.Unlock()
	u := &Upload{store: s, key: key, blob: blob, final: filepath.Join(dir, blob.Digest), partial: filepath.Join(dir, blob.Digest+".partial")}
	fail := func(err error) (*Upload, int64, bool, Refusal, error) {
		u.release()
		return nil, 0, false, RefusalNone, err
	}
	if info, err := os.Lstat(u.final); err == nil && info.Mode().IsRegular() && info.Size() == blob.Size {
		u.release()
		return nil, blob.Size, true, RefusalNone, nil
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
		s.discardPartial(u.partial, length)
		length = 0
	}
	if resume != 0 && resume != length {
		u.release()
		return nil, length, false, RefusalNone, nil
	}
	if err := s.reserve(blob.Size - length); err != nil {
		u.release()
		return nil, 0, false, RefusalScratchFull, nil
	}
	u.chargedAdd = blob.Size - length
	file, err := os.OpenFile(u.partial, os.O_RDWR|os.O_CREATE|noFollow, 0600)
	if err != nil {
		s.unreserve(u.chargedAdd)
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
			u.discard()
			return nil, 0, false, refusal, nil
		}
		u.written = length
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

func (s *Store) reserve(n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used+n > s.limit {
		return errors.New("scratch full")
	}
	s.used += n
	return nil
}

func (s *Store) unreserve(n int64) {
	s.mu.Lock()
	s.used -= n
	if s.used < 0 {
		s.used = 0
	}
	s.mu.Unlock()
}

func (s *Store) discardPartial(path string, length int64) {
	_ = os.Remove(path)
	s.unreserve(length)
}

func (u *Upload) release() {
	if u.released {
		return
	}
	u.released = true
	u.store.mu.Lock()
	delete(u.store.active, u.key)
	u.store.sessions--
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
			u.discard()
			return refusal
		}
	}
	u.written += int64(len(chunk))
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
		u.discard()
		return RefusalDigestMismatch
	}
	if u.layer != nil {
		if refusal := u.layer.finish(); refusal != RefusalNone {
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
	if err := os.Rename(u.partial, u.final); err != nil {
		u.discard()
		return RefusalScratchFull
	}
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
	u.store.unreserve(u.blob.Size - u.written)
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
	u.store.unreserve(u.written)
	u.store.unreserve(u.blob.Size - u.written)
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
