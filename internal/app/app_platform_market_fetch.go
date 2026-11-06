package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errPlatformMarketFetch = errors.New("application market fetch unavailable")

type platformMarketClient struct{ safe *safeClient }

type platformMarketFetchPart struct {
	body         []byte
	etag         string
	lastModified string
	total        int64
	complete     bool
	restart      bool
}

func newPlatformMarketClient() *platformMarketClient {
	c := &platformMarketClient{safe: newSafeClient(false)}
	c.safe.http.Timeout = 10 * time.Second
	c.safe.http.Transport.(*http.Transport).DisableCompression = true
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		return c.checkIP(net.ParseIP(host))
	}}
	c.safe.http.Transport.(*http.Transport).DialContext = dialer.DialContext
	c.safe.http.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= safeFetchRedirects {
			return errPlatformMarketFetch
		}
		// GitHub release assets use signed redirect queries. Query parameters
		// may come only from a redirect; scheme/host/path and dial policy remain.
		copyURL := *r.URL
		copyURL.RawQuery = ""
		copyURL.ForceQuery = false
		if len(r.URL.String()) > 8192 || c.checkURL(copyURL.String()) != nil {
			return errPlatformMarketFetch
		}
		rangeHeader := via[0].Header.Get("Range")
		ifRange := via[0].Header.Get("If-Range")
		r.Header = make(http.Header)
		r.Header.Set("Accept-Encoding", "identity")
		if strings.HasPrefix(rangeHeader, "bytes=") &&
			!strings.ContainsAny(rangeHeader, "\r\n") {
			r.Header.Set("Range", rangeHeader)
		}
		if ifRange != "" && !strings.ContainsAny(ifRange, "\r\n") {
			r.Header.Set("If-Range", ifRange)
		}
		return nil
	}
	return c
}

func (c *platformMarketClient) checkURL(raw string) error {
	if appplatform.ValidateCatalogueURL(raw) != nil {
		return errPlatformMarketFetch
	}
	u, _ := url.Parse(raw)
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return c.checkIP(ip)
	}
	if u.Hostname() == "localhost" {
		return errPlatformMarketFetch
	}
	// The existing safe dialer's Control hook checks every concrete address.
	// Avoid an additional uncancelable DNS preflight lookup here.
	return nil
}

func (c *platformMarketClient) checkIP(ip net.IP) error {
	if err := c.safe.checkIP(ip); err != nil {
		return err
	}
	if c.safe.allowInsecureForTest && ip.IsLoopback() {
		return nil
	}
	if !ip.IsGlobalUnicast() {
		return errPlatformMarketFetch
	}
	if v4 := ip.To4(); v4 != nil && (v4[0] == 0 || v4[0] >= 240) {
		return errPlatformMarketFetch
	}
	return nil
}

func (c *platformMarketClient) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > appplatform.MaxFrontendArchiveBytes || c.checkURL(rawURL) != nil {
		return nil, errPlatformMarketFetch
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, errPlatformMarketFetch
	}
	// ValidateCatalogueURL has already fixed this to a canonical public HTTPS
	// URL. Construct the request from those parsed components rather than
	// reparsing an arbitrary transport string.
	target = &url.URL{Scheme: "https", Host: target.Host, Path: target.Path}
	req := (&http.Request{
		Method: http.MethodGet,
		URL:    target,
		Header: make(http.Header),
		Host:   target.Host,
	}).WithContext(ctx)
	req.Header.Set("Accept-Encoding", "identity")
	response, err := c.safe.http.Do(req)
	if err != nil {
		return nil, errPlatformMarketFetch
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || response.ContentLength > limit {
		return nil, errPlatformMarketFetch
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errPlatformMarketFetch
	}
	return raw, nil
}

func (c *platformMarketClient) fetchResumable(
	ctx context.Context,
	rawURL string,
	limit int64,
	attempts int,
) ([]byte, error) {
	if attempts < 1 || attempts > 5 {
		return nil, errPlatformMarketFetch
	}
	var complete []byte
	var validator string
	var total int64
	for range attempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := c.fetchPart(
			ctx, rawURL, limit, int64(len(complete)), validator, total)
		if part.restart {
			clear(complete)
			complete = complete[:0]
			total = 0
			validator = ""
		}
		if len(complete) > 0 && validator != "" {
			current := part.etag
			if current == "" {
				current = part.lastModified
			}
			if current != "" && current != validator {
				clear(complete)
				complete = complete[:0]
				total = 0
				validator = current
				continue
			}
		}
		if len(part.body) > 0 {
			if int64(len(complete))+int64(len(part.body)) > limit {
				return nil, errPlatformMarketFetch
			}
			complete = append(complete, part.body...)
		}
		part.body = nil
		if part.total > 0 {
			total = part.total
		}
		if part.etag != "" {
			validator = part.etag
		} else if part.lastModified != "" {
			validator = part.lastModified
		}
		if err == nil && part.complete {
			if total > 0 && int64(len(complete)) != total {
				return nil, errPlatformMarketFetch
			}
			return complete, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	clear(complete)
	return nil, errPlatformMarketFetch
}

func (c *platformMarketClient) fetchPart(
	ctx context.Context,
	rawURL string,
	limit, offset int64,
	validator string,
	knownTotal int64,
) (platformMarketFetchPart, error) {
	var result platformMarketFetchPart
	if limit <= 0 || limit > appplatform.MaxFrontendArchiveBytes ||
		offset < 0 || offset > limit || c.checkURL(rawURL) != nil {
		return result, errPlatformMarketFetch
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return result, errPlatformMarketFetch
	}
	target = &url.URL{Scheme: "https", Host: target.Host, Path: target.Path}
	request := (&http.Request{
		Method: http.MethodGet,
		URL:    target,
		Header: make(http.Header),
		Host:   target.Host,
	}).WithContext(ctx)
	request.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" {
			request.Header.Set("If-Range", validator)
		}
	}
	response, err := c.safe.http.Do(request)
	if err != nil {
		return result, errPlatformMarketFetch
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Encoding") != "" &&
		response.Header.Get("Content-Encoding") != "identity" {
		return result, errPlatformMarketFetch
	}
	result.etag = response.Header.Get("ETag")
	result.lastModified = response.Header.Get("Last-Modified")
	switch response.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			result.restart = true
			offset = 0
		}
		result.total = response.ContentLength
		if response.ContentLength > limit {
			return result, errPlatformMarketFetch
		}
	case http.StatusPartialContent:
		start, end, total, ok := parseMarketContentRange(
			response.Header.Get("Content-Range"))
		if offset == 0 || !ok || start != offset ||
			end < start || end >= limit || end >= total || total > limit {
			return result, errPlatformMarketFetch
		}
		result.total = total
		if response.ContentLength >= 0 &&
			response.ContentLength != end-start+1 {
			return result, errPlatformMarketFetch
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if offset > 0 && knownTotal > 0 && offset == knownTotal {
			result.total = knownTotal
			result.complete = true
			return result, nil
		}
		return result, errPlatformMarketFetch
	default:
		return result, errPlatformMarketFetch
	}
	remaining := limit - offset
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, remaining+1))
	result.body = raw
	if int64(len(raw)) > remaining || readErr != nil {
		return result, errPlatformMarketFetch
	}
	if response.ContentLength >= 0 &&
		int64(len(raw)) != response.ContentLength {
		return result, errPlatformMarketFetch
	}
	result.complete = result.total < 0 ||
		offset+int64(len(raw)) == result.total
	if !result.complete {
		return result, errPlatformMarketFetch
	}
	return result, nil
}

func parseMarketContentRange(value string) (
	start, end, total int64,
	ok bool,
) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	rangePart, totalPart, found := strings.Cut(
		strings.TrimPrefix(value, "bytes "), "/")
	if !found || totalPart == "*" {
		return 0, 0, 0, false
	}
	startPart, endPart, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, 0, 0, false
	}
	start, startErr := strconv.ParseInt(startPart, 10, 64)
	end, endErr := strconv.ParseInt(endPart, 10, 64)
	total, totalErr := strconv.ParseInt(totalPart, 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil ||
		start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func (c *platformMarketClient) close() { c.safe.http.CloseIdleConnections() }

func (s *Server) fetchAndStagePlatformMarketRelease(ctx context.Context, actor platformActorSession, catalogueURL, digest string, client *platformMarketClient) (platformStagedRelease, error) {
	var zero platformStagedRelease
	if !platformValidSHA(digest) || appplatform.ValidateCatalogueURL(catalogueURL) != nil {
		return zero, errPlatformManagementInput
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var trust appplatform.PublisherTrust
	var now int64
	// Authority is checked before any outbound request; no network request is
	// inside a database transaction. This snapshot is rechecked at staging.
	err := s.withPlatformTrustActorContext(ctx, actor, func(e migExec, facts platformActorFacts, control platformActorControl, current appplatform.PublisherTrust) error {
		refreshed, err := platformRefreshActor(e, actor, facts, control)
		trust, now = current, refreshed.nowMS
		return err
	})
	if err != nil {
		return zero, err
	}
	raw, err := client.fetchResumable(
		ctx, catalogueURL, appplatform.MaxCatalogueBytes, 3)
	if err != nil {
		return zero, err
	}
	catalogue, err := appplatform.ParseCatalogue(raw)
	if err != nil {
		return zero, errPlatformMarketFetch
	}
	var entry *appplatform.CatalogueRelease
	for i := range catalogue.Releases {
		if catalogue.Releases[i].DescriptorSHA256 == digest {
			entry = &catalogue.Releases[i]
			break
		}
	}
	if entry == nil {
		return zero, errAppPlatformReleaseConflict
	}
	descriptor, err := client.fetchResumable(
		ctx, entry.DescriptorURL, appplatform.MaxDescriptorBytes, 3)
	if err != nil {
		return zero, err
	}
	if platformDigest(descriptor) != digest {
		return zero, errAppPlatformReleaseConflict
	}
	signature, err := client.fetchResumable(
		ctx, entry.SignatureURL, appplatform.MaxDescriptorSignatureBytes, 3)
	if err != nil {
		return zero, err
	}
	// Network waits may outlast a key/session's validity or a trust change.
	// Re-read current authority/time before allowing the archive request.
	err = s.withPlatformTrustActorContext(ctx, actor, func(e migExec, facts platformActorFacts, control platformActorControl, current appplatform.PublisherTrust) error {
		refreshed, err := platformRefreshActor(e, actor, facts, control)
		trust, now = current, refreshed.nowMS
		return err
	})
	if err != nil {
		return zero, err
	}
	verified, err := appplatform.VerifyPublisherDescriptor(descriptor, signature, trust, strconv.FormatInt(now, 10))
	if err != nil {
		return zero, errAppPlatformReleaseConflict
	}
	manifest := verified.Descriptor.Manifest
	if manifest.ID != entry.AppID || manifest.Publisher != entry.PublisherID || manifest.Version != entry.Version {
		return zero, errAppPlatformReleaseConflict
	}
	if platformPackageRequirements(manifest) != nil {
		return zero, errAppPlatformInstallUnavailable
	}
	// Resolve only the signed relative artifact path against the descriptor's
	// original directory. Redirect URLs never redefine artifact authority/base.
	base, _ := url.Parse(entry.DescriptorURL)
	relative, err := url.Parse(manifest.Frontend.Artifact)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return zero, errAppPlatformReleaseConflict
	}
	archive, err := client.fetchResumable(
		ctx, base.ResolveReference(relative).String(),
		appplatform.MaxFrontendArchiveBytes, 3)
	if err != nil {
		return zero, err
	}
	// The existing transaction verifies the full archive and current trust,
	// account, pause and namespace policy again, then atomically persists it.
	return s.stagePlatformReleaseContext(ctx, actor, descriptor, signature, archive)
}
