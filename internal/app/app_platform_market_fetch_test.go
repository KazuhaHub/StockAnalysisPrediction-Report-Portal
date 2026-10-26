package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformMarketVerifiedRemoteStaging(t *testing.T, pg bool) {
	for _, kind := range []string{"valid", "descriptor_changed", "signature_changed", "archive_changed", "trust_revoked", "trust_before_archive", "cancelled"} {
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
			if kind == "valid" {
				if err != nil || result.sha256 != digest {
					t.Fatal("verified package not staged", err)
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
		case "/cycle":
			http.Redirect(w, r, "https://127.0.0.1/cycle", http.StatusFound)
		case "/partial":
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("partial"))
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
