package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReleaseCatalogUsesGitHubETagAndLastSuccessfulAnswer(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	requests := 0
	fail := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if fail {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("If-None-Match") == `"catalog-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"catalog-1"`)
		writeJSON(w, []githubRelease{{Tag: "v2026.38.2", Body: "# current", Prerelease: true}})
	}))
	defer upstream.Close()

	catalog := releaseCatalog{
		client: upstream.Client(), apiURL: upstream.URL, ttl: time.Minute,
		now: func() time.Time { return now },
	}
	items, stale, err := catalog.list(context.Background())
	if err != nil || stale || len(items) != 1 || items[0].Maturity != "beta" {
		t.Fatalf("initial GitHub result = %#v, stale=%v, err=%v", items, stale, err)
	}
	if _, _, err := catalog.list(context.Background()); err != nil || requests != 1 {
		t.Fatalf("fresh cache made %d requests, err=%v", requests, err)
	}

	now = now.Add(2 * time.Minute)
	items, stale, err = catalog.list(context.Background())
	if err != nil || stale || requests != 2 || len(items) != 1 {
		t.Fatalf("ETag refresh = %#v, requests=%d, stale=%v, err=%v", items, requests, stale, err)
	}

	now = now.Add(2 * time.Minute)
	fail = true
	items, stale, err = catalog.list(context.Background())
	if err != nil || !stale || len(items) != 1 {
		t.Fatalf("upstream failure did not retain GitHub data: %#v, stale=%v, err=%v", items, stale, err)
	}
	if _, stale, err = catalog.list(context.Background()); err != nil || !stale || requests != 3 {
		t.Fatalf("stale backoff made %d requests, stale=%v, err=%v", requests, stale, err)
	}
}

func TestReleaseCatalogHasNoNonGitHubFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	catalog := releaseCatalog{client: upstream.Client(), apiURL: upstream.URL}
	if items, stale, err := catalog.list(context.Background()); err == nil || stale || items != nil {
		t.Fatalf("empty catalog invented a fallback: %#v, stale=%v, err=%v", items, stale, err)
	}
}

func TestNormalizePublishedReleasesUsesOnlyPublishedGitHubMetadata(t *testing.T) {
	items := normalizePublishedReleases([]githubRelease{
		{Tag: "v2026.38.3", Name: "v2026.38.3", Body: "# v2026.38.3 — Live title", Prerelease: true},
		{Tag: "v2026.38.2", Name: "Stable title", Body: "stable"},
		{Tag: "v2026.38.1", Body: "draft", Draft: true},
		{Tag: "v0.4.72", Body: "legacy"},
	})
	if len(items) != 2 || items[0].Tag != "v2026.38.3" || items[0].Maturity != "beta" || items[0].Title != "Live title" {
		t.Fatalf("normalized releases = %#v", items)
	}
	if items[1].Maturity != "release" || items[1].Title != "Stable title" {
		t.Fatalf("stable release = %#v", items[1])
	}
}

func TestVisibleHistoryChangesWhenOneReleaseIsPromoted(t *testing.T) {
	items := []publishedRelease{
		{Tag: "v2026.38.9", Maturity: "beta"},
		{Tag: "v2026.38.8", Maturity: "beta"},
		{Tag: "v2026.38.7", Maturity: "release"},
		{Tag: "v2026.38.3", Maturity: "release"},
		{Tag: "v2026.38", Maturity: "release"},
	}
	got := visibleReleaseHistory(items, "v2026.38.9", 10)
	if len(got) != 5 || got[0].Tag != "v2026.38.9" || got[2].Tag != "v2026.38.7" {
		t.Fatalf("beta history = %#v", got)
	}
	items[0].Maturity = "release"
	got = visibleReleaseHistory(items, "v2026.38.9", 10)
	if len(got) != 4 || got[0].Tag != "v2026.38.9" || got[1].Tag != "v2026.38.7" {
		t.Fatalf("promoted history = %#v", got)
	}
}
