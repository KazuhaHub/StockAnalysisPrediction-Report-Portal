package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Managed image storage (doc 170, "Portal storage"). The filesystem is the index and no table exists for it:
//
//	<data>/app-staging/  packages in flight and finished packages, a bounded least-recently-used cache
//	<data>/app-blobs/    the image blobs of the releases that still need them, named by their sha256
//
// Every name in both directories is a hex digest, or ends in ".partial". Anything else is removed on start.
const (
	platformStagingCapBytes int64 = 2 << 30
	platformBlobCapBytes    int64 = 8 << 30
	// platformPartialMaxAge is how long an interrupted download may wait for its resume.
	platformPartialMaxAge = 24 * time.Hour
	// platformBlobGrace keeps a freshly written blob that no release pins yet. A package is uploaded before the
	// review plan that will pin it exists, so a sweep must not take it from under the operator.
	platformBlobGrace = 24 * time.Hour
)

var (
	errPlatformImageStoreFull   = errors.New("application image storage is full")
	errPlatformImageBlobCorrupt = errors.New("a stored application image blob failed its hash check")
	errPlatformImageBlobMissing = errors.New("a stored application image blob is missing")
	// errPlatformImageBlobRead is a read that failed without proving the blob wrong; nothing was deleted.
	errPlatformImageBlobRead = errors.New("a stored application image blob could not be read")
)

var platformStoreName = regexp.MustCompile(`^[0-9a-f]{64}(\.partial|\.validator\.partial)?$`)

type platformImageStore struct {
	staging, blobs      string
	stagingCap, blobCap int64
	grace               time.Duration
	now                 func() time.Time
	mu                  sync.Mutex
	held                map[string]int
	// reservedBlobs and reservedStaging are bytes promised to writes in flight, so two concurrent checks cannot
	// both pass the cap on the same free space.
	reservedBlobs, reservedStaging int64
	digestLocks                    map[string]*platformDigestLock
	// putFault, when set, is consulted before each blob write; a test uses it to fail a write part-way.
	putFault func(appplatform.ImageBlob) error
}

type platformDigestLock struct {
	mu   sync.Mutex
	refs int
}

func newPlatformImageStore(root string) (*platformImageStore, error) {
	p := &platformImageStore{
		staging: filepath.Join(root, "app-staging"), blobs: filepath.Join(root, "app-blobs"),
		stagingCap: platformStagingCapBytes, blobCap: platformBlobCapBytes, grace: platformBlobGrace,
		now: time.Now, held: map[string]int{}, digestLocks: map[string]*platformDigestLock{},
	}
	for _, dir := range []string{p.staging, p.blobs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := platformCheckStoreDir(dir); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// platformImageRoot is the data directory the stores live under, next to the database like the other artefacts.
func (s *Server) platformImageRoot() string {
	if s.platformImageDir != "" {
		return s.platformImageDir
	}
	if s.cfg != nil && s.cfg.DBPath != "" {
		return config.DirOf(s.cfg.DBPath)
	}
	// Only a server without a configuration (a test) gets here; it must not write into the working directory.
	platformImageFallback.Do(func() {
		platformImageFallbackDir, _ = os.MkdirTemp("", "portal-app-images-")
	})
	return platformImageFallbackDir
}

var (
	platformImageFallback    sync.Once
	platformImageFallbackDir string
)

// platformImages returns the image stores, created on first use.
func (s *Server) platformImages() (*platformImageStore, error) {
	s.platformImageOnce.Lock()
	defer s.platformImageOnce.Unlock()
	if s.platformImageStore != nil {
		return s.platformImageStore, nil
	}
	store, err := newPlatformImageStore(s.platformImageRoot())
	if err != nil {
		return nil, err
	}
	s.platformImageStore = store
	return store, nil
}

// recover removes what a crash left behind. A finished package or blob is trusted for nothing: bytes are hashed
// again every time they are used.
func (p *platformImageStore) recover() {
	now := p.now()
	for _, dir := range []string{p.staging, p.blobs} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			if !platformStoreName.MatchString(entry.Name()) || !info.Mode().IsRegular() {
				_ = os.RemoveAll(path)
				continue
			}
			partial := strings.HasSuffix(entry.Name(), ".partial")
			if partial && (dir == p.blobs || now.Sub(info.ModTime()) > platformPartialMaxAge) {
				_ = os.Remove(path)
			}
		}
	}
}

func platformHexOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// newUploadKey names an upload no earlier attempt can resume.
func newPlatformUploadKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "upload:" + hex.EncodeToString(b[:])
}

func (p *platformImageStore) hold(names ...string) (release func()) {
	p.mu.Lock()
	for _, n := range names {
		p.held[n]++
	}
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			for _, n := range names {
				if p.held[n]--; p.held[n] <= 0 {
					delete(p.held, n)
				}
			}
			p.mu.Unlock()
		})
	}
}

func (p *platformImageStore) isHeld(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[name] > 0
}

type platformStoreEntry struct {
	name string
	size int64
	mod  time.Time
	part bool
}

func (p *platformImageStore) list(dir string) []platformStoreEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []platformStoreEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !platformStoreName.MatchString(e.Name()) {
			continue
		}
		out = append(out, platformStoreEntry{name: e.Name(), size: info.Size(), mod: info.ModTime(), part: strings.HasSuffix(e.Name(), ".partial")})
	}
	return out
}

func platformEntriesBytes(entries []platformStoreEntry) int64 {
	var n int64
	for _, e := range entries {
		n += e.size
	}
	return n
}

// reserveStaging makes room for a package of at most extra bytes, evicting the least recently used finished
// packages that nothing holds. It refuses when the cap still would not hold the package.
func (p *platformImageStore) reserveStaging(extra int64, keep string) (release func(), err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := p.list(p.staging)
	used := platformEntriesBytes(entries) + p.reservedStaging
	if used+extra > p.stagingCap {
		sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
		for _, e := range entries {
			if used+extra <= p.stagingCap {
				break
			}
			if e.part || e.name == keep || p.held[e.name] > 0 {
				continue
			}
			if os.Remove(filepath.Join(p.staging, e.name)) == nil {
				used -= e.size
			}
		}
	}
	if used+extra > p.stagingCap {
		return nil, errPlatformImageStoreFull
	}
	p.reservedStaging += extra
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.reservedStaging -= extra
			p.mu.Unlock()
		})
	}, nil
}

// reserveBlobs promises room under the blob cap for the blobs of one release that are not stored yet. The check
// and the promise happen under one lock, so two staging calls cannot both fit in the same free space.
func (p *platformImageStore) reserveBlobs(blobs []appplatform.ImageBlob) (release func(), err error) {
	missing := p.missingBlobs(blobs)
	var need int64
	for _, b := range missing {
		need += b.Size
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blobBytes()+p.reservedBlobs+need > p.blobCap {
		return nil, errPlatformImageStoreFull
	}
	p.reservedBlobs += need
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.reservedBlobs -= need
			p.mu.Unlock()
		})
	}, nil
}

// lockDigest serializes writers of one blob name, so two puts of the same digest never share a partial file.
func (p *platformImageStore) lockDigest(digest string) (unlock func()) {
	p.mu.Lock()
	l := p.digestLocks[digest]
	if l == nil {
		l = &platformDigestLock{}
		p.digestLocks[digest] = l
	}
	l.refs++
	p.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		p.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(p.digestLocks, digest)
		}
		p.mu.Unlock()
	}
}

// openPartial opens the in-flight file of key for appending and returns what it already holds. The name is the
// hash of the key, so a restart finds the same file.
func (p *platformImageStore) openPartial(key string) (*os.File, int64, error) {
	path := filepath.Join(p.staging, platformHexOf(key)+".partial")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, 0, errPlatformImageStoreFull
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|platformNoFollow, 0o600)
	if err != nil {
		return nil, 0, err
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, size, nil
}

func (p *platformImageStore) partialPath(key string) string {
	return filepath.Join(p.staging, platformHexOf(key)+".partial")
}

// partialName is the file name of the in-flight download of key.
func (p *platformImageStore) partialName(key string) string { return platformHexOf(key) + ".partial" }

func (p *platformImageStore) validatorPath(key string) string {
	return filepath.Join(p.staging, platformHexOf(key)+".validator.partial")
}

// discardPartial removes an in-flight file and its validator.
func (p *platformImageStore) discardPartial(key string) {
	_ = os.Remove(p.partialPath(key))
	_ = os.Remove(p.validatorPath(key))
}

// finishPartial hashes the finished file, names it by its digest and drops its validator.
func (p *platformImageStore) finishPartial(key string) (string, int64, error) {
	path := p.partialPath(key)
	file, err := os.OpenFile(path, os.O_RDONLY|platformNoFollow, 0)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	file.Close()
	if err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	final := filepath.Join(p.staging, digest)
	if err := os.Rename(path, final); err != nil {
		return "", 0, err
	}
	_ = os.Remove(p.validatorPath(key))
	now := p.now()
	_ = os.Chtimes(final, now, now)
	return digest, size, nil
}

// ---------- blobs ----------

func (p *platformImageStore) blobPath(digest string) string { return filepath.Join(p.blobs, digest) }

// blobBytes is the size of every finished blob.
func (p *platformImageStore) blobBytes() int64 {
	var n int64
	for _, e := range p.list(p.blobs) {
		if !e.part {
			n += e.size
		}
	}
	return n
}

// missingBlobs lists the blobs of an image set that are not stored with the declared size. It does not hash:
// hashing happens on use.
func (p *platformImageStore) missingBlobs(blobs []appplatform.ImageBlob) []appplatform.ImageBlob {
	var missing []appplatform.ImageBlob
	seen := map[string]bool{}
	for _, blob := range blobs {
		if seen[blob.Digest] {
			continue
		}
		seen[blob.Digest] = true
		if info, err := os.Lstat(p.blobPath(blob.Digest)); err != nil || !info.Mode().IsRegular() || info.Size() != blob.Size {
			missing = append(missing, blob)
		}
	}
	return missing
}

// fitsBlobs reports whether the blobs that are not stored yet would fit under the cap.
func (p *platformImageStore) fitsBlobs(blobs []appplatform.ImageBlob) bool {
	var need int64
	for _, blob := range p.missingBlobs(blobs) {
		need += blob.Size
	}
	return p.blobBytes()+need <= p.blobCap
}

// putBlob stores one blob, hashing the exact bytes it writes. A blob that does not hash to its name is never
// given that name.
func (p *platformImageStore) putBlob(blob appplatform.ImageBlob, source io.Reader) error {
	release := p.hold(blob.Digest)
	defer release()
	defer p.lockDigest(blob.Digest)()
	if p.putFault != nil {
		if err := p.putFault(blob); err != nil {
			return err
		}
	}
	if blob.Size < 1 || blob.Size > appplatform.MaxImageBlobBytes || !platformValidSHA(blob.Digest) {
		return errPlatformImageBlobCorrupt
	}
	temp := p.blobPath(blob.Digest) + ".partial"
	// Under the digest lock nothing else owns this name; a stale file (or a link planted in its place) is removed
	// and the file is created new, never followed.
	_ = os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|platformNoFollow, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, blob.Size+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err != nil || syncErr != nil || closeErr != nil || n != blob.Size || hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		_ = os.Remove(temp)
		return errPlatformImageBlobCorrupt
	}
	if err := os.Rename(temp, p.blobPath(blob.Digest)); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// openBlob hashes a stored blob against its name and size and returns it open at offset zero. A blob that is
// proven wrong (a different size, or a complete read that does not hash to its name) is deleted, so the release
// needs its package again. A read that merely fails (EIO, EMFILE) deletes nothing: the attempt fails and the blob
// stays for the next one.
func (p *platformImageStore) openBlob(blob appplatform.ImageBlob) (*os.File, error) {
	path := p.blobPath(blob.Digest)
	file, err := os.OpenFile(path, os.O_RDONLY|platformNoFollow, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errPlatformImageBlobMissing
		}
		if platformIsLinkRefusal(err) {
			_ = os.Remove(path)
			return nil, errPlatformImageBlobCorrupt
		}
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	if !info.Mode().IsRegular() || info.Size() != blob.Size {
		file.Close()
		_ = os.Remove(path)
		return nil, errPlatformImageBlobCorrupt
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	if n != blob.Size || hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		file.Close()
		_ = os.Remove(path)
		return nil, errPlatformImageBlobCorrupt
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	return file, nil
}

// platformIsLinkRefusal reports the error an O_NOFOLLOW open returns for a symbolic link.
func platformIsLinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

// sweep removes the blobs nothing pins. A blob held by an upload or a load, and a blob younger than the grace
// period, stay. It returns the digests it removed.
func (p *platformImageStore) sweep(pinned map[string]bool) []string {
	var removed []string
	now := p.now()
	for _, e := range p.list(p.blobs) {
		if e.part || pinned[e.name] || p.isHeld(e.name) || now.Sub(e.mod) < p.grace {
			continue
		}
		if os.Remove(filepath.Join(p.blobs, e.name)) == nil {
			removed = append(removed, e.name)
		}
	}
	return removed
}

// removeUnpinned removes named blobs the pinned set does not hold, whatever their age. It is for blobs a failed
// staging wrote itself.
func (p *platformImageStore) removeUnpinned(digests []string, pinned map[string]bool) {
	for _, d := range digests {
		if !pinned[d] && !p.isHeld(d) {
			_ = os.Remove(p.blobPath(d))
		}
	}
}
