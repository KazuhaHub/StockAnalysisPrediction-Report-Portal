package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformPackageDownloadTimeout bounds one call to fetch a package. The download is resumable, so a call that
// runs out of time leaves the file for the next one.
const platformPackageDownloadTimeout = 10 * time.Minute

// fetchToFile downloads a package into the staging file named by key, resuming what an earlier call or an earlier
// process left there. It asks for the missing range with Range and If-Range, the validator of which is kept in a
// sidecar of the same directory, so a restart resumes exactly as a retry does. It returns the size of the
// complete file.
func (c *platformMarketClient) fetchToFile(ctx context.Context, rawURL string, limit int64, store *platformImageStore, key string, attempts int) (int64, error) {
	if attempts < 1 || attempts > 5 || limit <= 0 || c.checkURL(rawURL) != nil {
		return 0, errPlatformMarketFetch
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return 0, errPlatformMarketFetch
	}
	target = &url.URL{Scheme: "https", Host: target.Host, Path: target.Path}
	// The shared client carries a short whole-request timeout meant for small documents; a package is bounded by
	// the context instead.
	client := *c.safe.http
	client.Timeout = 0
	for range attempts {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		file, offset, err := store.openPartial(key)
		if err != nil {
			return 0, err
		}
		validator, _ := os.ReadFile(store.validatorPath(key))
		done, total, err := c.fetchPartToFile(ctx, &client, target, limit, file, offset, string(validator), store.validatorPath(key))
		file.Close()
		if done {
			return total, nil
		}
		if err != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, errPlatformMarketFetch
}

func (c *platformMarketClient) fetchPartToFile(ctx context.Context, client *http.Client, target *url.URL, limit int64, file *os.File, offset int64, validator, validatorPath string) (done bool, total int64, err error) {
	if offset > limit {
		return false, 0, truncateAndFail(file)
	}
	request := (&http.Request{Method: http.MethodGet, URL: target, Header: make(http.Header), Host: target.Host}).WithContext(ctx)
	request.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" && !strings.ContainsAny(validator, "\r\n") {
			request.Header.Set("If-Range", validator)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return false, 0, errPlatformMarketFetch
	}
	defer response.Body.Close()
	if enc := response.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return false, 0, errPlatformMarketFetch
	}
	current := response.Header.Get("ETag")
	if current == "" {
		current = response.Header.Get("Last-Modified")
	}
	switch response.StatusCode {
	case http.StatusOK:
		if response.ContentLength > limit {
			return false, 0, errPlatformMarketFetch
		}
		// The server sent the whole file: what was held is not a prefix of it, or Range was ignored.
		if err := file.Truncate(0); err != nil {
			return false, 0, err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return false, 0, err
		}
		offset, total = 0, response.ContentLength
	case http.StatusPartialContent:
		start, end, all, ok := parseMarketContentRange(response.Header.Get("Content-Range"))
		if offset == 0 || !ok || start != offset || end < start || end >= all || all > limit ||
			(response.ContentLength >= 0 && response.ContentLength != end-start+1) {
			return false, 0, errPlatformMarketFetch
		}
		total = all
	case http.StatusRequestedRangeNotSatisfiable:
		// The held prefix does not fit the file as it is now: start again from nothing.
		return false, 0, truncateAndFail(file)
	default:
		return false, 0, errPlatformMarketFetch
	}
	if current != "" {
		if err := os.WriteFile(validatorPath, []byte(current), 0o600); err != nil {
			return false, 0, err
		}
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit-offset+1))
	size := offset + written
	if size > limit {
		return false, 0, truncateAndFail(file)
	}
	if total < 0 {
		// No length was announced: only a clean end of the body completes the file.
		return copyErr == nil, size, errPlatformMarketFetch
	}
	if copyErr != nil || size != total {
		// What arrived stays for the next attempt, which asks for the rest.
		return false, 0, errPlatformMarketFetch
	}
	return true, size, nil
}

func truncateAndFail(file *os.File) error {
	_ = file.Truncate(0)
	return errPlatformMarketFetch
}

// fetchAndStagePlatformPackage downloads the .portalapp of a managed catalogue release and runs exactly the
// offline path on it. The descriptor and signature the catalogue named and the signature check passed must be
// byte-equal to the package's, so online and offline install produce the identical release.
func (s *Server) fetchAndStagePlatformPackage(ctx context.Context, actor platformActorSession, client *platformMarketClient, packageURL string, descriptor, signature []byte) (platformStagedRelease, error) {
	var zero platformStagedRelease
	store, err := s.platformImages()
	if err != nil {
		return zero, err
	}
	key := "catalogue:" + packageURL
	same := func(e appplatform.OfflineEnvelope) error {
		if !bytes.Equal(e.Descriptor, descriptor) || !bytes.Equal(e.Signature, signature) {
			return errAppPlatformReleaseConflict
		}
		return nil
	}
	for fresh := 0; fresh < 2; fresh++ {
		download, cancel := context.WithTimeout(ctx, platformPackageDownloadTimeout)
		if err := store.reserveStaging(appplatform.MaxManagedOfflineEnvelopeBytes, store.partialName(key)); err != nil {
			cancel()
			return zero, err
		}
		size, err := client.fetchToFile(download, packageURL, appplatform.MaxManagedOfflineEnvelopeBytes, store, key, 3)
		cancel()
		if err != nil {
			// The partial stays: the next call resumes it.
			return zero, err
		}
		file, _, err := store.openPartial(key)
		if err != nil {
			return zero, err
		}
		staged, err := s.stagePlatformPackageFile(ctx, actor, file, size, same)
		file.Close()
		if err == nil {
			_, _, _ = store.finishPartial(key)
			return staged.release, nil
		}
		// A prefix kept from an earlier download may belong to a different version of the file. The package is
		// verified in full against the signed descriptor, so the damage is caught here; try once from nothing.
		store.discardPartial(key)
		if fresh == 1 || ctx.Err() != nil || !bytesIsPackageDamage(err) {
			return zero, err
		}
	}
	return zero, errAppPlatformReleaseConflict
}

// bytesIsPackageDamage reports whether an error is the kind a corrupt or mismatched download produces, as opposed
// to trust or storage.
func bytesIsPackageDamage(err error) bool {
	return err == appplatform.ErrInvalidOfflineEnvelope || err == errAppPlatformReleaseConflict
}
