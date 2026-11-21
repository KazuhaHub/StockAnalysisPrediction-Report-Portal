package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A catalogue package stored as an OCI artifact (doc 188). The manifest is fetched by digest, with an anonymous
// Bearer token when the registry asks for one, and its one layer is the .portalapp.

const platformOCITokenBytes = 64 << 10

// errPlatformOCILayerMismatch is a downloaded layer that is not the manifest's size and digest. It is refused as
// a release conflict.
var errPlatformOCILayerMismatch = fmt.Errorf("%w: OCI layer is not the pinned digest", errAppPlatformReleaseConflict)

var platformOCITokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

type platformOCIPackage struct {
	blobURL string
	layer   appplatform.OCILayer
	token   string
}

// ociGet sends one GET to a canonical public https URL built from host and path, with an optional query the
// caller composed. The query is never part of the policy check: it carries only values Portal computed.
func (c *platformMarketClient) ociGet(ctx context.Context, rawURL string, query url.Values, accept, token string) (*http.Response, error) {
	if c.checkURL(rawURL) != nil {
		return nil, errPlatformMarketFetch
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errPlatformMarketFetch
	}
	target := &url.URL{Scheme: "https", Host: parsed.Host, Path: parsed.Path, RawQuery: query.Encode()}
	request := (&http.Request{Method: http.MethodGet, URL: target, Header: make(http.Header), Host: target.Host}).WithContext(ctx)
	request.Header.Set("Accept-Encoding", "identity")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.safe.http.Do(request)
	if err != nil {
		return nil, errPlatformMarketFetch
	}
	if enc := response.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		response.Body.Close()
		return nil, errPlatformMarketFetch
	}
	return response, nil
}

// resolveOCIPackage fetches and verifies the manifest of ref and returns where its layer is and the token that
// read it.
func (c *platformMarketClient) resolveOCIPackage(ctx context.Context, ref appplatform.CataloguePackageOCI) (platformOCIPackage, error) {
	var zero platformOCIPackage
	host, path, err := appplatform.SplitOCIRepository(ref.Repository)
	if err != nil || !appplatform.ValidOCIDigest(ref.Digest) {
		return zero, errPlatformMarketFetch
	}
	manifestURL := "https://" + host + "/v2/" + path + "/manifests/" + ref.Digest
	response, err := c.ociGet(ctx, manifestURL, nil, appplatform.OCIManifestMediaType, "")
	if err != nil {
		return zero, err
	}
	token := ""
	if response.StatusCode == http.StatusUnauthorized {
		challenges := response.Header.Values("WWW-Authenticate")
		response.Body.Close()
		if token, err = c.ociAnonymousToken(ctx, challenges, path); err != nil {
			return zero, err
		}
		if response, err = c.ociGet(ctx, manifestURL, nil, appplatform.OCIManifestMediaType, token); err != nil {
			return zero, err
		}
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaType != appplatform.OCIManifestMediaType || response.ContentLength > appplatform.MaxOCIManifestBytes {
		return zero, errPlatformMarketFetch
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, appplatform.MaxOCIManifestBytes+1))
	if err != nil || len(raw) > appplatform.MaxOCIManifestBytes {
		return zero, errPlatformMarketFetch
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != ref.Digest {
		return zero, errAppPlatformReleaseConflict
	}
	layer, err := appplatform.ParseOCIPackageManifest(raw)
	if err != nil {
		return zero, errAppPlatformReleaseConflict
	}
	return platformOCIPackage{blobURL: "https://" + host + "/v2/" + path + "/blobs/" + layer.Digest, layer: layer, token: token}, nil
}

// ociAnonymousToken asks the challenge's realm for a pull token for path, sending no credentials. The scope is
// the one Portal computes, never the challenge's.
func (c *platformMarketClient) ociAnonymousToken(ctx context.Context, challenges []string, path string) (string, error) {
	var realm, service string
	found := false
	for _, challenge := range challenges {
		if realm, service, found = parseOCIBearerChallenge(challenge); found {
			break
		}
	}
	if !found || appplatform.ValidateCatalogueURL(realm) != nil {
		return "", errPlatformMarketFetch
	}
	query := url.Values{"scope": {"repository:" + path + ":pull"}}
	if service != "" {
		query.Set("service", service)
	}
	response, err := c.ociGet(ctx, realm, query, "application/json", "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > platformOCITokenBytes {
		return "", errPlatformMarketFetch
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, platformOCITokenBytes+1))
	if err != nil || len(raw) > platformOCITokenBytes {
		return "", errPlatformMarketFetch
	}
	var answer struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &answer) != nil {
		return "", errPlatformMarketFetch
	}
	token := answer.Token
	if token == "" {
		token = answer.AccessToken
	}
	if len(token) > 8192 || !platformOCITokenPattern.MatchString(token) {
		return "", errPlatformMarketFetch
	}
	return token, nil
}

// parseOCIBearerChallenge reads `Bearer realm="…",service="…"[,scope="…"]`. Only quoted values without escapes
// or control characters are accepted; a repeated parameter is refused.
func parseOCIBearerChallenge(header string) (realm, service string, ok bool) {
	scheme, rest, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	params := map[string]string{}
	rest = strings.TrimSpace(rest)
	for rest != "" {
		key, after, found := strings.Cut(rest, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !found || key == "" || !strings.HasPrefix(after, `"`) {
			return "", "", false
		}
		value, tail, found := strings.Cut(after[1:], `"`)
		if !found || strings.ContainsAny(value, "\\\x00\r\n\t") || len(value) > 2048 {
			return "", "", false
		}
		if _, repeated := params[key]; repeated {
			return "", "", false
		}
		params[key] = value
		tail = strings.TrimSpace(tail)
		if tail != "" && !strings.HasPrefix(tail, ",") {
			return "", "", false
		}
		rest = strings.TrimSpace(strings.TrimPrefix(tail, ","))
	}
	realm, service = params["realm"], params["service"]
	if realm == "" || len(service) > 255 {
		return "", "", false
	}
	return realm, service, true
}

// downloadOCIPackage resolves ref and downloads its layer into the staging file of key. A finished file that is
// not exactly the layer's size and digest is discarded.
func (c *platformMarketClient) downloadOCIPackage(ctx context.Context, ref appplatform.CataloguePackageOCI, store *platformImageStore, key string) (int64, error) {
	pkg, err := c.resolveOCIPackage(ctx, ref)
	if err != nil {
		return 0, err
	}
	size, err := c.fetchToFile(ctx, pkg.blobURL, pkg.layer.Size, store, key, 3, pkg.token)
	if err != nil {
		return 0, err
	}
	if size != pkg.layer.Size || platformFileDigest(store.partialPath(key)) != pkg.layer.Digest {
		store.discardPartial(key)
		return 0, errPlatformOCILayerMismatch
	}
	return size, nil
}

func platformFileDigest(path string) string {
	file, err := os.OpenFile(path, os.O_RDONLY|platformNoFollow, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
