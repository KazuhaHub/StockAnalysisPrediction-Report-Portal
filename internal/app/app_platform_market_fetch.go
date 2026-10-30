package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errPlatformMarketFetch = errors.New("application market fetch unavailable")

type platformMarketClient struct{ safe *safeClient }

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
		r.Header = make(http.Header)
		r.Header.Set("Accept-Encoding", "identity")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errPlatformMarketFetch
	}
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
	raw, err := client.fetch(ctx, catalogueURL, appplatform.MaxCatalogueBytes)
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
	descriptor, err := client.fetch(ctx, entry.DescriptorURL, appplatform.MaxDescriptorBytes)
	if err != nil {
		return zero, err
	}
	if platformDigest(descriptor) != digest {
		return zero, errAppPlatformReleaseConflict
	}
	signature, err := client.fetch(ctx, entry.SignatureURL, appplatform.MaxDescriptorSignatureBytes)
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
	if platformInstallRequirements(manifest) != nil || manifest.Backend != nil || manifest.Data.WritesOnStartup {
		return zero, errAppPlatformInstallUnavailable
	}
	// Resolve only the signed relative artifact path against the descriptor's
	// original directory. Redirect URLs never redefine artifact authority/base.
	base, _ := url.Parse(entry.DescriptorURL)
	relative, err := url.Parse(manifest.Frontend.Artifact)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return zero, errAppPlatformReleaseConflict
	}
	archive, err := client.fetch(ctx, base.ResolveReference(relative).String(), appplatform.MaxFrontendArchiveBytes)
	if err != nil {
		return zero, err
	}
	// The existing transaction verifies the full archive and current trust,
	// account, pause and namespace policy again, then atomically persists it.
	return s.stagePlatformReleaseContext(ctx, actor, descriptor, signature, archive)
}
