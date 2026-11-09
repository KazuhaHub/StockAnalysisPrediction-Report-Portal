package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// readMetadata returns a manifest or config blob after hashing it against its name. It deletes the blob only
// when it is proven wrong; a failed read leaves it.
func (p *platformImageStore) readMetadata(digest string) ([]byte, error) {
	if !platformValidSHA(digest) {
		return nil, errPlatformImageBlobMissing
	}
	path := p.blobPath(digest)
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
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > appplatform.MaxImageMetadataBytes {
		_ = os.Remove(path)
		return nil, errPlatformImageBlobCorrupt
	}
	raw, err := io.ReadAll(io.LimitReader(file, appplatform.MaxImageMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobRead, err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		_ = os.Remove(path)
		return nil, errPlatformImageBlobCorrupt
	}
	return raw, nil
}

// planFor rebuilds the verified plan of a descriptor's image entry from the stored manifest and config.
func (p *platformImageStore) planFor(entry appplatform.BackendImage) (appplatform.ImagePlan, error) {
	manifest, err := p.readMetadata(entry.ManifestDigest)
	if err != nil {
		return appplatform.ImagePlan{}, err
	}
	config, err := p.readMetadata(entry.ConfigDigest)
	if err != nil {
		return appplatform.ImagePlan{}, err
	}
	plan, err := appplatform.PlanImage(entry, manifest, config)
	if err != nil {
		_ = os.Remove(p.blobPath(entry.ManifestDigest))
		_ = os.Remove(p.blobPath(entry.ConfigDigest))
		return appplatform.ImagePlan{}, errPlatformImageBlobCorrupt
	}
	return plan, nil
}

// releaseBlobs lists the blobs a stored release's image entries name. A release whose descriptor does not parse,
// or whose manifest cannot be read to name its layers, is an error: the caller cannot know what to keep.
func (p *platformImageStore) releaseBlobs(descriptor []byte) ([]string, error) {
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	if parsed.Manifest.Backend == nil {
		return nil, nil
	}
	var digests []string
	for _, entry := range parsed.Manifest.Backend.Images {
		digests = append(digests, entry.ManifestDigest, entry.ConfigDigest)
		plan, err := p.planFor(entry)
		if err != nil {
			return nil, err
		}
		for _, layer := range plan.Layers {
			digests = append(digests, layer.Digest)
		}
	}
	return digests, nil
}

// platformPinnedBlobSet expands the shared release pins (loadPlatformReleasePins) to blob digests through the
// stored descriptors and manifests. Any failure, a cancelled context included, returns an error and no set: a
// sweep must never run on a partial set.
func (s *Server) platformPinnedBlobSet(ctx context.Context, p *platformImageStore) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	pins, err := loadPlatformReleasePins(pool, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	pinned := map[string]bool{}
	for sha := range pins {
		var descriptor []byte
		if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), sha).Scan(&descriptor); err != nil {
			return nil, fmt.Errorf("pinned release %s: %w", sha, err)
		}
		digests, err := p.releaseBlobs(descriptor)
		if err != nil {
			return nil, fmt.Errorf("pinned release %s (installations %s): %w", sha, s.platformReleaseInstallations(ctx, sha), err)
		}
		for _, d := range digests {
			pinned[d] = true
		}
	}
	return pinned, ctx.Err()
}

// platformReleaseInstallations names the installations a release belongs to, for a log line.
func (s *Server) platformReleaseInstallations(ctx context.Context, sha string) string {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT DISTINCT installation_id FROM app_platform_activations WHERE release_sha256=?
		UNION SELECT DISTINCT o.installation_id FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE p.source_release_sha256=? OR p.target_release_sha256=?`), sha, sha, sha)
	if err != nil {
		return "unknown"
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			if out != "" {
				out += ","
			}
			out += id
		}
	}
	if out == "" {
		return "none"
	}
	return out
}

// sweepPlatformImages removes the blobs nothing pins. It runs on start and after an operation ends, and a
// failure only leaves blobs for the next sweep.
func (s *Server) sweepPlatformImages(ctx context.Context) {
	store, err := s.platformImages()
	if err != nil {
		return
	}
	pinned, err := s.platformPinnedBlobSet(ctx, store)
	if err != nil {
		log.Printf("app-platform image sweep stopped, nothing removed; upload the package of the named release again: %v", err)
		return
	}
	store.sweep(pinned)
}

// platformImageRecover runs once at start, before any operation can use the stores.
func (s *Server) platformImageRecover() {
	store, err := s.platformImages()
	if err != nil {
		log.Printf("app-platform image store: %v", err)
		return
	}
	store.recover()
	s.sweepPlatformImages(context.Background())
}

// platformPackageStaged is what a staged package reports.
type platformPackageStaged struct {
	release    platformStagedRelease
	inspection appplatform.OfflineEnvelopeInspection
}

// stagePlatformPackageFile stages a package that already sits on disk. A static package stages as before. A
// managed one first proves its blobs fit under the cap and stores them, and only then writes the release row, so
// a refusal for space leaves no release behind and a release row always finds its blobs.
func (s *Server) stagePlatformPackageFile(ctx context.Context, actor platformActorSession, file *os.File, size int64, expect func(appplatform.OfflineEnvelope) error) (platformPackageStaged, error) {
	var zero platformPackageStaged
	envelope, err := appplatform.InspectOfflineEnvelopeAt(file, size)
	if err != nil {
		return zero, err
	}
	if expect != nil {
		if err := expect(envelope); err != nil {
			return zero, err
		}
	}
	var added []string
	var store *platformImageStore
	if envelope.Inspection.Execution == "managed" {
		if store, err = s.platformImages(); err != nil {
			return zero, err
		}
		if added, err = s.storePlatformImageBlobs(store, envelope); err != nil {
			// A write that failed part-way leaves the blobs this call already wrote: remove the unpinned ones.
			if pinned, pinErr := s.platformPinnedBlobSet(ctx, store); pinErr == nil {
				store.removeUnpinned(added, pinned)
			}
			return zero, err
		}
	}
	staged, err := s.stagePlatformReleaseContext(ctx, actor, envelope.Descriptor, envelope.Signature, envelope.Frontend)
	if err != nil {
		if store != nil {
			if pinned, pinErr := s.platformPinnedBlobSet(ctx, store); pinErr == nil {
				store.removeUnpinned(added, pinned)
			}
		}
		return zero, err
	}
	return platformPackageStaged{release: staged, inspection: envelope.Inspection}, nil
}

// storePlatformImageBlobs puts the blobs of a verified package into the blob store. It returns the digests it
// wrote itself.
func (s *Server) storePlatformImageBlobs(store *platformImageStore, envelope appplatform.OfflineEnvelope) ([]string, error) {
	var blobs []appplatform.ImageBlob
	for _, plan := range envelope.Images {
		blobs = append(blobs, plan.Blobs()...)
	}
	// A blob that is present but fails its hash is replaced from this package, so only a missing one is new space.
	// The room is promised under the store's lock before any byte is written, and given back when staging ends.
	unreserve, err := store.reserveBlobs(blobs)
	if err != nil {
		return nil, err
	}
	defer unreserve()
	names := make([]string, 0, len(blobs))
	for _, b := range blobs {
		names = append(names, b.Digest)
	}
	release := store.hold(names...)
	defer release()
	var added []string
	seen := map[string]bool{}
	for _, blob := range blobs {
		if seen[blob.Digest] {
			continue
		}
		seen[blob.Digest] = true
		if file, err := store.openBlob(blob); err == nil {
			file.Close()
			continue
		}
		stream, err := envelope.OpenBlob(blob.Digest)
		if err != nil {
			return added, err
		}
		putErr := store.putBlob(blob, stream)
		stream.Close()
		if putErr != nil {
			return added, putErr
		}
		added = append(added, blob.Digest)
	}
	return added, nil
}

// streamUpload copies a request body into a fresh staging file, bounded, and returns the open file.
func (p *platformImageStore) streamUpload(body io.Reader, limit int64) (*os.File, int64, string, error) {
	release, err := p.reserveStaging(limit, "")
	if err != nil {
		return nil, 0, "", err
	}
	defer release()
	key := newPlatformUploadKey()
	file, _, err := p.openPartial(key)
	if err != nil {
		return nil, 0, "", err
	}
	n, err := io.Copy(file, io.LimitReader(body, limit+1))
	if err == nil && n > limit {
		err = appplatform.ErrInvalidOfflineEnvelope
	}
	if err != nil {
		file.Close()
		p.discardPartial(key)
		return nil, 0, "", appplatform.ErrInvalidOfflineEnvelope
	}
	return file, n, key, nil
}
