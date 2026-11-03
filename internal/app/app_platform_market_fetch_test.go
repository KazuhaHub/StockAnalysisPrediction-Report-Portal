package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformMarketVerifiedRemoteStaging(t *testing.T, pg bool) {
	for _, kind := range []string{"valid", "descriptor_changed", "signature_changed", "archive_changed", "archive_interrupted", "archive_retry", "archive_range_retry", "trust_revoked", "trust_before_archive", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, actor := platformTrustTestServer(t, pg)
			platformRegisterTestPublisher(t, s, actor)
			raw, signature, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
			var manifest appplatform.Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
			manifest.Capabilities.Required = []string{}
			raw, _ = json.Marshal(manifest)
			signature = platformSignTestDescriptor(t, raw)
			digest := platformDigest(raw)
			var requests, archives atomic.Int32
			var rangePartialServed atomic.Bool
			var base string
			remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
					t.Error("deployment credentials leaked")
				}
				switch r.URL.Path {
				case "/index.json":
					json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": []appplatform.CatalogueRelease{{AppID: manifest.ID, PublisherID: manifest.Publisher, Version: manifest.Version, DescriptorSHA256: digest, DescriptorURL: base + "/app.json", SignatureURL: base + "/app.sig"}}})
				case "/app.json":
					if kind == "descriptor_changed" {
						w.Write(append(raw, ' '))
					} else {
						w.Write(raw)
					}
				case "/app.sig":
					if kind == "trust_before_archive" {
						mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
					}
					if kind == "signature_changed" {
						w.Write([]byte("invalid"))
					} else {
						w.Write(signature)
					}
				case "/" + manifest.Frontend.Artifact:
					archives.Add(1)
					if kind == "trust_revoked" {
						mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
					}
					if kind == "archive_changed" {
						w.Write([]byte("wrong archive"))
					} else if kind == "archive_interrupted" || kind == "archive_retry" && archives.Load() == 1 {
						w.Header().Set("Content-Length", strconv.Itoa(len(archive)+1))
						w.Write(archive[:len(archive)/2])
						w.(http.Flusher).Flush()
					} else if kind == "archive_range_retry" {
						half := len(archive) / 2
						if r.Header.Get("Range") == "" &&
							rangePartialServed.CompareAndSwap(false, true) {
							w.Header().Set("Content-Length", strconv.Itoa(len(archive)+1))
							w.Header().Set("ETag", `"archive-v1"`)
							w.Write(archive[:half])
							w.(http.Flusher).Flush()
							return
						}
						if r.Header.Get("Range") == "" {
							w.Write(archive)
							return
						}
						expectedRange := "bytes=" + strconv.Itoa(half) + "-"
						if r.Header.Get("Range") != expectedRange ||
							r.Header.Get("If-Range") != `"archive-v1"` {
							t.Errorf("range retry headers: Range=%q If-Range=%q",
								r.Header.Get("Range"), r.Header.Get("If-Range"))
							http.Error(w, "range required", http.StatusBadRequest)
							return
						}
						w.Header().Set("ETag", `"archive-v1"`)
						w.Header().Set("Content-Range",
							"bytes "+strconv.Itoa(half)+"-"+
								strconv.Itoa(len(archive)-1)+"/"+
								strconv.Itoa(len(archive)))
						w.WriteHeader(http.StatusPartialContent)
						w.Write(archive[half:])
					} else {
						w.Write(archive)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			base = "https://127.0.0.1"
			client := newPlatformMarketClient()
			client.safe.allowInsecureForTest = true
			platformMarketTestTLS(t, client, remote)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			result, err := s.fetchAndStagePlatformMarketRelease(ctx, actor, base+"/index.json", digest, client)
			if kind == "valid" || kind == "archive_retry" ||
				kind == "archive_range_retry" {
				if err != nil || result.sha256 != digest {
					t.Fatal("verified package not staged", err)
				}
				if kind != "valid" && archives.Load() != 2 {
					t.Fatalf("resumed download made %d archive requests",
						archives.Load())
				}
				if _, err = s.createPlatformInstallReview(actor, digest, 1); err != nil {
					t.Fatal("remote package cannot be reviewed", err)
				}
				again, err := s.fetchAndStagePlatformMarketRelease(ctx, actor, base+"/index.json", digest, client)
				if err != nil || !again.reused || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
					t.Fatal("remote retry installed or duplicated release", err)
				}
			} else {
				if err == nil || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 {
					t.Fatal("invalid remote package staged", err)
				}
				if (kind == "descriptor_changed" || kind == "signature_changed" || kind == "trust_before_archive") && archives.Load() != 0 {
					t.Fatal("untrusted descriptor triggered artifact download")
				}
				if kind == "cancelled" && requests.Load() != 0 {
					t.Fatal("canceled fetch transmitted request")
				}
				if kind == "archive_interrupted" && scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 {
					t.Fatal("interrupted artifact left a staged release")
				}
			}
		})
	}
}

func TestPlatformMarketTransportBounds(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			w.Write([]byte(strings.Repeat("x", 65)))
		case "/chunked":
			w.(http.Flusher).Flush()
			w.Write([]byte(strings.Repeat("x", 65)))
		case "/encoded":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write([]byte("not identity"))
		case "/redirect":
			http.Redirect(w, r, "https://127.0.0.1/private?signature=fixture", http.StatusFound)
		case "/redirect-range":
			if r.Header.Get("Range") != "bytes=3-" ||
				r.Header.Get("If-Range") != `"fixture"` {
				t.Errorf("redirect lost range headers: Range=%q If-Range=%q",
					r.Header.Get("Range"), r.Header.Get("If-Range"))
			}
			http.Redirect(w, r, "https://127.0.0.1/ok", http.StatusFound)
		case "/cycle":
			http.Redirect(w, r, "https://127.0.0.1/cycle", http.StatusFound)
		case "/partial":
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("partial"))
		case "/range-too-large":
			w.Header().Set("Content-Range", "bytes 3-5/65")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("abc"))
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer remote.Close()
	client := newPlatformMarketClient()
	defer client.close()
	for _, address := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "::1", "fc00::1", "0.0.0.1", "255.255.255.255"} {
		if client.checkIP(net.ParseIP(address)) == nil {
			t.Fatal("nonpublic market destination allowed", address)
		}
	}
	if _, err := client.fetch(context.Background(), "https://127.0.0.1/ok", 64); err == nil {
		t.Fatal("production transport accepted loopback")
	}
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	if got, err := client.fetch(context.Background(), "https://127.0.0.1/ok", 64); err != nil || string(got) != "ok" {
		t.Fatal("bounded fetch failed", err)
	}
	if got, err := client.fetch(context.Background(), "https://127.0.0.1/redirect", 64); err != nil || string(got) != "ok" {
		t.Fatal("bounded signed-query redirect failed", err)
	}
	part, err := client.fetchPart(
		context.Background(), "https://127.0.0.1/redirect-range",
		64, 3, `"fixture"`, 0)
	if err != nil || !part.restart || string(part.body) != "ok" {
		t.Fatal("redirect range restart failed", part, err)
	}
	if _, err := client.fetchPart(
		context.Background(), "https://127.0.0.1/range-too-large",
		64, 3, `"fixture"`, 0); err == nil {
		t.Fatal("range response exceeded the configured total limit")
	}
	for _, path := range []string{"/large", "/chunked", "/encoded", "/partial", "/cycle"} {
		if _, err := client.fetch(context.Background(), "https://127.0.0.1"+path, 64); err == nil {
			t.Fatal("invalid response accepted", path)
		}
	}
	client.safe.allowInsecureForTest = false
	request := httptest.NewRequest("GET", "https://public.example/artifact", nil)
	request.URL.Host = "127.0.0.1"
	if err := client.safe.http.CheckRedirect(request, []*http.Request{{}}); err == nil {
		t.Fatal("redirect to loopback allowed")
	}
}

func TestPlatformMarketPartialPartRetainsBytes(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "8")
			w.Header().Set("ETag", `"partial-v1"`)
			w.Write([]byte("abcd"))
			w.(http.Flusher).Flush()
		}))
	defer remote.Close()
	client := newPlatformMarketClient()
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	part, err := client.fetchPart(
		t.Context(), "https://127.0.0.1/partial", 64, 0, "", 0)
	if err == nil || string(part.body) != "abcd" ||
		part.total != 8 || part.etag != `"partial-v1"` {
		t.Fatalf("partial bytes lost: %+v %v", part, err)
	}
}

func platformMarketTestTLS(t *testing.T, client *platformMarketClient, remote *httptest.Server) {
	t.Helper()
	transport := client.safe.http.Transport.(*http.Transport)
	transport.TLSClientConfig = remote.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
	}
	t.Cleanup(client.close)
}

func TestPlatformMarketVerifiedRemoteStagingSQLite(t *testing.T) {
	testPlatformMarketVerifiedRemoteStaging(t, false)
}
func TestPlatformMarketVerifiedRemoteStagingPostgres(t *testing.T) {
	testPlatformMarketVerifiedRemoteStaging(t, true)
}

func testPlatformManagedMarketStaging(t *testing.T, pg bool) {
	s, actor := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, actor)
	raw, archive := platformManagedReleaseFixture(t, "linux/amd64")
	signature := platformSignTestDescriptor(t, raw)
	digest := platformDigest(raw)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var base string
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": []appplatform.CatalogueRelease{{
				AppID: manifest.ID, PublisherID: manifest.Publisher,
				Version: manifest.Version, DescriptorSHA256: digest,
				DescriptorURL: base + "/app.json", SignatureURL: base + "/app.sig",
			}}})
		case "/app.json":
			w.Write(raw)
		case "/app.sig":
			w.Write(signature)
		case "/" + manifest.Frontend.Artifact:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	base = "https://127.0.0.1"
	client := newPlatformMarketClient()
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	result, err := s.fetchAndStagePlatformMarketRelease(
		t.Context(), actor, base+"/index.json", digest, client)
	if err != nil || result.sha256 != digest || !result.managed {
		t.Fatal("managed market release was not staged", result, err)
	}

	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(
		*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	got := sessionTestRequest(
		mux, "GET", "/api/admin/app-platform/releases", cookie, "")
	var page struct {
		Items []platformStagedSummary `json:"items"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil ||
		len(page.Items) != 1 || !page.Items[0].Backend ||
		!page.Items[0].Installable || page.Items[0].Reason != "" {
		t.Fatal("managed market release missing from catalogue",
			got.Code, got.Body.String())
	}
	plan := sessionTestRequest(
		mux, "POST", "/api/admin/app-platform/plans", cookie,
		platformManagementPlanBody(digest, actor.username))
	if plan.Code != http.StatusServiceUnavailable ||
		!strings.Contains(plan.Body.String(), "app_platform_runtime_unavailable") ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("managed market install did not fail at runtime authority",
			plan.Code, plan.Body.String())
	}
}

func TestPlatformManagedMarketStagingSQLite(t *testing.T) {
	testPlatformManagedMarketStaging(t, false)
}

func TestPlatformManagedMarketStagingPostgres(t *testing.T) {
	testPlatformManagedMarketStaging(t, true)
}
