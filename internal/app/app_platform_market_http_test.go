package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlatformMarketHTTPAuthorityAndInput(t *testing.T) {
	s, mux, actor, _, cookie := platformManagementFixture(t, false)
	for _, endpoint := range []string{"catalogue", "stage"} {
		path := "/api/admin/app-platform/market/" + endpoint
		body := `{"catalogue_url":"https://127.0.0.1/index.json"}`
		if endpoint == "stage" {
			body = fmt.Sprintf(`{"catalogue_url":"https://127.0.0.1/index.json","release_sha256":%q}`, strings.Repeat("a", 64))
		}
		if got := sessionTestRequest(mux, "POST", path, "", body); got.Code != 401 {
			t.Fatal("anonymous remote fetch allowed", got.Code)
		}
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 503 || strings.Contains(got.Body.String(), "127.0.0.1") {
			t.Fatal("private fetch allowed or exposed target", got.Code, got.Body.String())
		}
		for _, bad := range []string{`null`, `{}`, strings.Replace(body, `https://127.0.0.1/index.json`, `http://127.0.0.1/index.json`, 1), strings.Replace(body, `"catalogue_url":`, `"Catalogue_url":`, 1), strings.Replace(body, `"catalogue_url":`, `"endpoint":"/tmp/socket","catalogue_url":`, 1)} {
			if got := sessionTestRequest(mux, "POST", path, cookie, bad); got.Code != 400 {
				t.Fatal("bad remote fetch input accepted", got.Code, got.Body.String())
			}
		}
		mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 403 {
			t.Fatal("ordinary user remote fetch allowed", got.Code)
		}
		mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("remote fetch route admitted installation")
	}
}

func TestPlatformMarketFetchCancellation(t *testing.T) {
	requested := make(chan struct{})
	finished := make(chan struct{})
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
		close(finished)
	}))
	defer remote.Close()
	client := newPlatformMarketClient()
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.fetch(ctx, "https://127.0.0.1/index.json", 1024); done <- err }()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS fetch did not reach owned fixture")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled download succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download ignored cancellation")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled fetch did not close upstream request")
	}
}

// marketCatalogueHarness serves a catalogue of signed descriptors from an owned TLS listener and returns the
// catalogue endpoint's response for it. `paths` records every descriptor or signature path requested.
type marketCatalogueHarness struct {
	t        *testing.T
	s        *Server
	cookie   string
	files    map[string][]byte
	releases []appplatform.CatalogueRelease
	mu       sync.Mutex
	paths    []string
}

func newMarketCatalogueHarness(t *testing.T) *marketCatalogueHarness {
	s, _, _, _, cookie := platformManagementFixture(t, false)
	return &marketCatalogueHarness{t: t, s: s, cookie: cookie, files: map[string][]byte{}}
}

// signed builds a descriptor for the given identity and signs it as the fixture publisher.
func (h *marketCatalogueHarness) signed(id, publisher, version, name string) (digest string, raw, sig []byte) {
	h.t.Helper()
	rawFixture, _, _ := platformSignedReleaseFixture(h.t, version)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(rawFixture, &manifest); err != nil {
		h.t.Fatal(err)
	}
	manifest.ID, manifest.Version, manifest.Publisher = id, version, publisher
	manifest.Presentation.Display.Texts[0].Name = name
	raw, err := json.Marshal(manifest)
	if err != nil {
		h.t.Fatal(err)
	}
	return platformDigest(raw), raw, platformSignTestDescriptorAs(h.t, raw, publisher)
}

// add lists a release in the catalogue, serving the given descriptor bytes for it.
func (h *marketCatalogueHarness) add(publisher, id, version, digest string, raw, sig []byte) {
	path := "/" + publisher + "-" + id + "-" + version
	h.files[path+".json"], h.files[path+".sig"] = raw, sig
	h.releases = append(h.releases, appplatform.CatalogueRelease{AppID: id, PublisherID: publisher, Version: version, DescriptorSHA256: digest, DescriptorURL: "https://127.0.0.1" + path + ".json", SignatureURL: "https://127.0.0.1" + path + ".sig"})
}

type marketCatalogueApp struct {
	PublisherID string `json:"publisher_id"`
	AppID       string `json:"app_id"`
	Versions    []struct {
		Version string `json:"version"`
		SHA     string `json:"descriptor_sha256"`
	} `json:"versions"`
	Display *struct {
		Icon  string `json:"icon"`
		Texts []struct {
			Name string `json:"name"`
		} `json:"texts"`
	} `json:"display"`
	Trusted bool `json:"publisher_trusted"`
}

func (h *marketCatalogueHarness) fetch() (releases int, apps []marketCatalogueApp, raw string) {
	h.t.Helper()
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.json" {
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": h.releases})
			return
		}
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.Path)
		h.mu.Unlock()
		if body, ok := h.files[r.URL.Path]; ok {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	h.t.Cleanup(remote.Close)
	mux := http.NewServeMux()
	h.s.platformAdminRoute(mux, "POST /api/admin/app-platform/market/catalogue", func(w http.ResponseWriter, r *http.Request, user string) {
		c := newPlatformMarketClient()
		c.safe.allowInsecureForTest = true
		platformMarketTestTLS(h.t, c, remote)
		defer c.close()
		h.s.apiPlatformMarketCatalogueClient(w, r, user, c)
	})
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/market/catalogue", h.cookie, `{"catalogue_url":"https://127.0.0.1/index.json"}`)
	if got.Code != 200 {
		h.t.Fatal(got.Code, got.Body.String())
	}
	var body struct {
		Releases []json.RawMessage    `json:"releases"`
		Apps     []marketCatalogueApp `json:"apps"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		h.t.Fatal(err)
	}
	return len(body.Releases), body.Apps, got.Body.String()
}

func (h *marketCatalogueHarness) requested(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.paths {
		if strings.HasPrefix(p, path) {
			return true
		}
	}
	return false
}

func TestPlatformMarketCatalogueGroupsApps(t *testing.T) {
	h := newMarketCatalogueHarness(t)
	for _, v := range []string{"0.1.0-beta.2", "0.1.0", "0.1.0-beta.10"} {
		d, raw, sig := h.signed("notes-alpha", "fixture-publisher", v, "Alpha "+v)
		h.add("fixture-publisher", "notes-alpha", v, d, raw, sig)
	}
	// Digest listed in the catalogue differs from the bytes served: no display.
	_, raw, sig := h.signed("notes-bravo", "fixture-publisher", "1.0.0", "Bravo")
	h.add("fixture-publisher", "notes-bravo", "1.0.0", strings.Repeat("b", 64), raw, sig)
	// A publisher nobody trusts: not trusted, no display, and no request for its descriptor.
	d, raw, sig := h.signed("notes-delta", "stranger", "1.0.0", "Delta")
	h.add("stranger", "notes-delta", "1.0.0", d, raw, sig)

	releases, apps, body := h.fetch()
	if releases != len(h.releases) || len(apps) != 3 {
		t.Fatal("releases kept and one entry per app expected", body)
	}
	alpha := apps[0]
	order := []string{}
	for _, v := range alpha.Versions {
		order = append(order, v.Version)
	}
	if alpha.AppID != "notes-alpha" || strings.Join(order, ",") != "0.1.0,0.1.0-beta.10,0.1.0-beta.2" || alpha.Versions[0].SHA != h.releases[1].DescriptorSHA256 {
		t.Fatal("versions not newest first", order)
	}
	if !alpha.Trusted || alpha.Display == nil || alpha.Display.Icon != "file-text" || alpha.Display.Texts[0].Name != "Alpha 0.1.0" {
		t.Fatal("display must come from the newest version", body)
	}
	if bravo := apps[1]; !bravo.Trusted || bravo.Display != nil {
		t.Fatal("a descriptor that does not match its digest must leave display out", bravo)
	}
	if delta := apps[2]; delta.Trusted || delta.Display != nil {
		t.Fatal("untrusted publisher", delta)
	}
	if h.requested("/stranger-") {
		t.Fatal("an untrusted publisher's descriptor was fetched")
	}
}

// A signed descriptor that names another app, version or publisher than its catalogue entry is never shown.
func TestPlatformMarketCatalogueDropsMismatchedIdentity(t *testing.T) {
	s, _, actor, _, cookie := platformManagementFixture(t, false)
	otherKey := platformTrustFixtureKey(8, "other-key")
	if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "register", publisherID: "second-publisher", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "other-"}}, key: &otherKey}); err != nil {
		t.Fatal(err)
	}
	h := &marketCatalogueHarness{t: t, s: s, cookie: cookie, files: map[string][]byte{}}
	// The entry says notes-charlie, the signed descriptor says notes-other.
	d, raw, sig := h.signed("notes-other", "fixture-publisher", "1.0.0", "Other")
	h.add("fixture-publisher", "notes-charlie", "1.0.0", d, raw, sig)
	// The entry says 1.0.1, the signed descriptor says 1.0.0.
	d, raw, sig = h.signed("notes-echo", "fixture-publisher", "1.0.0", "Echo")
	h.add("fixture-publisher", "notes-echo", "1.0.1", d, raw, sig)
	// The entry says second-publisher, the signed descriptor says fixture-publisher.
	d, raw, sig = h.signed("notes-foxtrot", "fixture-publisher", "1.0.0", "Foxtrot")
	h.add("second-publisher", "notes-foxtrot", "1.0.0", d, raw, sig)
	// A control that does match, so the fetch itself is known to work.
	d, raw, sig = h.signed("notes-golf", "fixture-publisher", "1.0.0", "Golf")
	h.add("fixture-publisher", "notes-golf", "1.0.0", d, raw, sig)

	_, apps, body := h.fetch()
	if len(apps) != 4 {
		t.Fatal(body)
	}
	for _, a := range apps[:3] {
		if !a.Trusted || a.Display != nil {
			t.Fatal("a mismatched descriptor must leave display out", a.AppID, body)
		}
	}
	if apps[3].Display == nil {
		t.Fatal("the matching control must have a display", body)
	}
}

// A publisher that is registered but disabled is not trusted: no display and no request for its descriptor.
func TestPlatformMarketCatalogueDisabledPublisherIsNotTrusted(t *testing.T) {
	h := newMarketCatalogueHarness(t)
	d, raw, sig := h.signed("notes-hotel", "fixture-publisher", "1.0.0", "Hotel")
	h.add("fixture-publisher", "notes-hotel", "1.0.0", d, raw, sig)
	mustExec(t, h.s.st, `UPDATE app_platform_publishers SET enabled=0`)
	_, apps, body := h.fetch()
	if len(apps) != 1 || apps[0].Trusted || apps[0].Display != nil {
		t.Fatal("a disabled publisher must not be trusted", body)
	}
	if h.requested("/fixture-publisher-") || len(h.paths) != 0 {
		t.Fatal("a disabled publisher's descriptor or signature was fetched", h.paths)
	}
}
