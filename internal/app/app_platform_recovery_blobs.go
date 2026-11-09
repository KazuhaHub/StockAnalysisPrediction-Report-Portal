package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The managed half of the recovery bundle (doc 170, "Fresh-host restore, managed half"). The bundle carries the
// image blobs of the pinned set; the receiving host verifies every blob before it takes the blob's name. After
// recovery ends, a managed app starts through the ordinary load-image effect that precedes every start.

// bundlePlatformImageBlobs resolves every pending release of the inventory and writes its blobs into the
// bundle. It refuses when a managed release's blobs are not all present and intact: a bundle that could not
// restore the release is not written, because the operator would find out on the new host.
func (s *Server) bundlePlatformImageBlobs(dir string, releases []platformReleaseBlob) ([]platformBundleFile, error) {
	var store *platformImageStore
	files := map[string]platformBundleFile{}
	for i := range releases {
		release := &releases[i]
		if release.Backend != platformBlobPending {
			continue
		}
		var descriptor []byte
		if err := s.st.db.QueryRow(s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), release.ReleaseSHA256).Scan(&descriptor); err != nil {
			return nil, fmt.Errorf("%w: release %s is not stored", errPlatformBundle, release.ReleaseSHA256)
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.Backend == nil {
			return nil, fmt.Errorf("%w: release %s has no readable backend", errPlatformBundle, release.ReleaseSHA256)
		}
		if len(parsed.Manifest.Backend.Images) == 0 {
			release.Backend = platformBlobReference
			continue
		}
		if store == nil {
			var err error
			if store, err = s.platformImages(); err != nil {
				return nil, err
			}
		}
		digests := map[string]bool{}
		for _, entry := range parsed.Manifest.Backend.Images {
			plan, err := store.planFor(entry)
			if err != nil {
				return nil, fmt.Errorf("%w: the image of release %s is not stored in good order: %v", errPlatformBundle, release.ReleaseSHA256, err)
			}
			for _, blob := range plan.Blobs() {
				if _, done := files[blob.Digest]; !done {
					file, err := bundleOnePlatformBlob(dir, store, blob)
					if err != nil {
						return nil, fmt.Errorf("%w: blob %s of release %s: %v", errPlatformBundle, blob.Digest, release.ReleaseSHA256, err)
					}
					files[blob.Digest] = file
				}
				digests[blob.Digest] = true
			}
		}
		release.Backend = platformBlobInBundle
		release.ImageBlobs = release.ImageBlobs[:0]
		for d := range digests {
			release.ImageBlobs = append(release.ImageBlobs, d)
		}
		sort.Strings(release.ImageBlobs)
	}
	out := make([]platformBundleFile, 0, len(files))
	for _, f := range files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 })
	return out, nil
}

func bundleOnePlatformBlob(dir string, store *platformImageStore, blob appplatform.ImageBlob) (platformBundleFile, error) {
	// openBlob hashes the stored bytes; the copy is hashed again as it is written, and must agree.
	source, err := store.openBlob(blob)
	if err != nil {
		return platformBundleFile{}, err
	}
	defer source.Close()
	file, err := writePlatformBundleFile(dir, "app-blobs/"+blob.Digest, source)
	if err != nil {
		return platformBundleFile{}, err
	}
	if file.SHA256 != blob.Digest || file.Bytes != blob.Size {
		return platformBundleFile{}, errPlatformImageBlobCorrupt
	}
	return file, nil
}

// restorePlatformBundleBlobs places the blobs of a verified bundle into the blob store of the receiving host.
// Each blob is hashed while it is written to a partial file and takes its name only when it matches, so a
// damaged blob is never placed, whatever the manifest says.
func (s *Server) restorePlatformBundleBlobs(dir string, manifest platformRecoveryBundleManifest) error {
	if len(manifest.ImageBlobs) == 0 {
		return nil
	}
	store, err := s.platformImages()
	if err != nil {
		return err
	}
	var blobs []appplatform.ImageBlob
	for _, f := range manifest.ImageBlobs {
		blobs = append(blobs, appplatform.ImageBlob{Digest: f.SHA256, Size: f.Bytes})
	}
	unreserve, err := store.reserveBlobs(blobs)
	if err != nil {
		return err
	}
	defer unreserve()
	for _, f := range manifest.ImageBlobs {
		in, err := os.Open(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			return err
		}
		putErr := store.putBlob(appplatform.ImageBlob{Digest: f.SHA256, Size: f.Bytes}, io.Reader(in))
		in.Close()
		if putErr != nil {
			return fmt.Errorf("%w: %s: %v", errPlatformBundle, f.Path, putErr)
		}
	}
	return nil
}
