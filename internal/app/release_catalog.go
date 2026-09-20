package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

const (
	githubReleasesAPI = "https://api.github.com/repos/KazuhaHub/StockAnalysisPrediction-Report-Portal/releases?per_page=100"
	releaseCacheTTL   = time.Minute
	maxReleasePayload = 4 << 20
	portalNotesStart  = "<!-- portal-notes:start -->"
	portalNotesEnd    = "<!-- portal-notes:end -->"
)

// publishedRelease is the part of a GitHub Release the reader needs. GitHub Release metadata is
// the only authority for both the note and its maturity: a published pre-release may be promoted or
// demoted without changing its tag or rebuilding its artifacts.
type publishedRelease struct {
	Tag      string
	Title    string
	Markdown string
	Maturity string
}

// releaseCatalog is a small process-wide cache in each Server. It never invents or embeds release
// state: every retained entry came from GitHub, and an upstream failure can only serve the last
// successful GitHub answer. Holding the mutex across a refresh gives all readers one upstream
// request rather than a burst when the TTL expires.
type releaseCatalog struct {
	mu      sync.Mutex
	client  *http.Client
	apiURL  string
	ttl     time.Duration
	now     func() time.Time
	etag    string
	checked time.Time
	items   []publishedRelease
	stale   bool
}

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (c *releaseCatalog) list(ctx context.Context) ([]publishedRelease, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = releaseCacheTTL
	}
	if c.items != nil && now.Sub(c.checked) < ttl {
		return clonePublishedReleases(c.items), c.stale, nil
	}

	apiURL := c.apiURL
	if apiURL == "" {
		apiURL = githubReleasesAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return c.staleOrError(err, now)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "report-portal/"+version.Version)
	if c.etag != "" {
		req.Header.Set("If-None-Match", c.etag)
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return c.staleOrError(err, now)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && c.items != nil {
		c.checked = now
		c.stale = false
		return clonePublishedReleases(c.items), false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return c.staleOrError(fmt.Errorf("GitHub releases returned %s", resp.Status), now)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleasePayload+1))
	if err != nil {
		return c.staleOrError(err, now)
	}
	if len(body) > maxReleasePayload {
		return c.staleOrError(errors.New("GitHub releases response is too large"), now)
	}
	var raw []githubRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return c.staleOrError(err, now)
	}
	items := normalizePublishedReleases(raw)
	c.items = items
	c.etag = resp.Header.Get("ETag")
	c.checked = now
	c.stale = false
	return clonePublishedReleases(items), false, nil
}

func (c *releaseCatalog) staleOrError(err error, now time.Time) ([]publishedRelease, bool, error) {
	if c.items != nil {
		// Back off for one cache interval. Otherwise every reader request during an outage would retry
		// GitHub and turn a dependency failure into a request amplifier.
		c.checked = now
		c.stale = true
		return clonePublishedReleases(c.items), true, nil
	}
	return nil, false, err
}

func clonePublishedReleases(items []publishedRelease) []publishedRelease {
	return append([]publishedRelease(nil), items...)
}

func normalizePublishedReleases(raw []githubRelease) []publishedRelease {
	items := make([]publishedRelease, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		tag := strings.TrimSpace(item.Tag)
		if item.Draft || !version.IsReleaseTag(tag) || seen[tag] {
			continue
		}
		seen[tag] = true
		body := releaseReaderMarkdown(item.Body)
		title := strings.TrimSpace(item.Name)
		if title == tag {
			title = ""
		}
		if title == "" {
			title = releaseBodyTitle(body)
		}
		maturity := "release"
		if item.Prerelease {
			maturity = "beta"
		}
		items = append(items, publishedRelease{Tag: tag, Title: title, Markdown: body, Maturity: maturity})
	}
	sort.Slice(items, func(i, j int) bool { return releaseTagLess(items[j].Tag, items[i].Tag) })
	return items
}

// releaseReaderMarkdown separates the reader-facing changelog from operational material that
// belongs on the GitHub Release page. New releases carry explicit HTML-comment boundaries. Older
// releases predate those boundaries, so their known generated sections are trimmed at a heading;
// ordinary prose containing the same words remains untouched.
func releaseReaderMarkdown(body string) string {
	body = strings.TrimSpace(body)
	if start := strings.Index(body, portalNotesStart); start >= 0 {
		noteStart := start + len(portalNotesStart)
		if end := strings.Index(body[noteStart:], portalNotesEnd); end >= 0 {
			return strings.TrimSpace(body[noteStart : noteStart+end])
		}
		// A hand-edited Release may temporarily have only the opening marker. Keep its note readable
		// while the legacy heading boundary below prevents operational sections from leaking in.
		body = strings.TrimSpace(body[noteStart:])
	}

	cut := len(body)
	for _, heading := range []string{
		"### Container image (ghcr.io)",
		"## What's Changed",
		"**Full Changelog**:",
	} {
		if i := markdownLineIndex(body, heading); i >= 0 && i < cut {
			cut = i
		}
	}
	body = strings.TrimSpace(body[:cut])
	if end := strings.Index(body, portalNotesEnd); end >= 0 {
		body = strings.TrimSpace(body[:end])
	}
	return body
}

func markdownLineIndex(body, line string) int {
	if strings.HasPrefix(body, line) {
		return 0
	}
	if i := strings.Index(body, "\n"+line); i >= 0 {
		return i
	}
	return -1
}

func releaseBodyTitle(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "# "))
	if _, title, ok := strings.Cut(line, " — "); ok {
		return strings.TrimSpace(title)
	}
	return ""
}

func releaseTagLess(a, b string) bool {
	aa := releaseTagParts(a)
	bb := releaseTagParts(b)
	for i := range aa {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return false
}

func releaseTagParts(tag string) [3]int {
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	var out [3]int
	for i := 0; i < len(parts) && i < len(out); i++ {
		out[i], _ = strconv.Atoi(parts[i])
	}
	return out
}

func findPublishedRelease(items []publishedRelease, tag string) (publishedRelease, bool) {
	for _, item := range items {
		if item.Tag == tag {
			return item, true
		}
	}
	return publishedRelease{}, false
}

// visibleReleaseHistory applies the reader rule to current GitHub state. A full release shows full
// milestones only. A pre-release shows those milestones plus pre-releases after the latest full
// release. Promotion therefore changes the list on the next cache refresh without rebuilding.
func visibleReleaseHistory(items []publishedRelease, through string, limit int) []publishedRelease {
	if limit < 1 || !version.IsReleaseTag(through) {
		return nil
	}
	target, ok := findPublishedRelease(items, through)
	if !ok {
		return nil
	}
	eligible := make([]publishedRelease, 0, len(items))
	for _, item := range items {
		if !releaseTagLess(through, item.Tag) { // item <= through
			eligible = append(eligible, item)
		}
	}
	if target.Maturity == "release" {
		out := eligible[:0]
		for _, item := range eligible {
			if item.Maturity == "release" {
				out = append(out, item)
			}
		}
		return clonePublishedReleases(out[:min(limit, len(out))])
	}

	var latestStable [3]int
	hasStable := false
	for _, item := range eligible {
		if item.Maturity == "release" {
			latestStable = releaseTagParts(item.Tag)
			hasStable = true
			break
		}
	}
	out := make([]publishedRelease, 0, len(eligible))
	for _, item := range eligible {
		parts := releaseTagParts(item.Tag)
		if item.Maturity == "release" || (!hasStable || partsGreater(parts, latestStable)) {
			out = append(out, item)
		}
	}
	return out[:min(limit, len(out))]
}

func partsGreater(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
