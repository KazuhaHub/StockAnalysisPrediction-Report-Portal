package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// readMetadata returns a manifest or config blob after hashing it against its name.
func (p *platformImageStore) readMetadata(digest string) ([]byte, error) {
	if !platformValidSHA(digest) {
		return nil, errPlatformImageBlobMissing
	}
	info, err := os.Lstat(p.blobPath(digest))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errPlatformImageBlobMissing
	}
	if info.Size() < 1 || info.Size() > appplatform.MaxImageMetadataBytes {
		_ = os.Remove(p.blobPath(digest))
		return nil, errPlatformImageBlobCorrupt
	}
	raw, err := os.ReadFile(p.blobPath(digest))
	sum := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(sum[:]) != digest {
		_ = os.Remove(p.blobPath(digest))
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

// platformReleaseBlobs lists the blobs a stored release's image entries name, as far as the stored manifests
// say. A release whose manifest is gone names only what the descriptor does.
func (p *platformImageStore) releaseBlobs(descriptor []byte) []string {
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return nil
	}
	var digests []string
	for _, entry := range parsed.Manifest.Backend.Images {
		digests = append(digests, entry.ManifestDigest, entry.ConfigDigest)
		if plan, err := p.planFor(entry); err == nil {
			for _, layer := range plan.Layers {
				digests = append(digests, layer.Digest)
			}
		}
	}
	return digests
}

// platformPinnedBlobSet computes, from the existing rows, the blobs no sweep may remove: those of a release
// that is active or a candidate, that a retained recovery set restores, that a non-terminal operation moves
// between, or that an open review plan targets.
func (s *Server) platformPinnedBlobSet(ctx context.Context, p *platformImageStore) (map[string]bool, error) {
	nowMS := time.Now().UnixMilli()
	queries := []struct {
		query string
		args  []any
	}{
		{`SELECT release_sha256 FROM app_platform_activations WHERE state IN ('candidate','active')`, nil},
		{`SELECT release_sha256 FROM app_platform_recovery_sets WHERE state<>'deleted'`, nil},
		{`SELECT p.source_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.completed_ms IS NULL AND p.source_release_sha256 IS NOT NULL`, nil},
		{`SELECT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.completed_ms IS NULL AND p.target_release_sha256 IS NOT NULL`, nil},
		{`SELECT source_release_sha256 FROM app_platform_review_plans WHERE consumed_ms IS NULL AND expires_ms>? AND source_release_sha256 IS NOT NULL`, []any{nowMS}},
		{`SELECT target_release_sha256 FROM app_platform_review_plans WHERE consumed_ms IS NULL AND expires_ms>? AND target_release_sha256 IS NOT NULL`, []any{nowMS}},
	}
	releases := map[string]bool{}
	for _, q := range queries {
		rows, err := s.st.db.QueryContext(ctx, s.st.bind(q.query), q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				rows.Close()
				return nil, err
			}
			releases[sha] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	pinned := map[string]bool{}
	for sha := range releases {
		var descriptor []byte
		if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), sha).Scan(&descriptor); err != nil {
			continue
		}
		for _, d := range p.releaseBlobs(descriptor) {
			pinned[d] = true
		}
	}
	return pinned, nil
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
		log.Printf("app-platform image sweep: %v", err)
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
func (s *Server) stagePlatformPackageFile(ctx context.Context, actor platformActorSession, file *os.File, size int64) (platformPackageStaged, error) {
	var zero platformPackageStaged
	envelope, err := appplatform.InspectOfflineEnvelopeAt(file, size)
	if err != nil {
		return zero, err
	}
	var added []string
	var store *platformImageStore
	if envelope.Inspection.Execution == "managed" {
		if store, err = s.platformImages(); err != nil {
			return zero, err
		}
		if added, err = s.storePlatformImageBlobs(store, envelope); err != nil {
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
	if !store.fitsBlobs(blobs) {
		return nil, errPlatformImageStoreFull
	}
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
	if err := p.reserveStaging(limit, ""); err != nil {
		return nil, 0, "", err
	}
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
