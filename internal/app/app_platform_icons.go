package app

import (
	"container/list"
	"net/http"
	"strings"
	"sync"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A descriptor's custom icon is a PNG inside signed bytes. JSON responses never
// carry it: they carry its SHA-256, and the browser fetches the canonical
// re-encoded PNG from /api/app-platform/icons/{sha}.png. The cache is filled
// only when a descriptor has verified against publisher trust, so a miss means
// "not currently known", never "ask upstream".

const platformIconCacheEntries = 256

type platformIconCache struct {
	mu      sync.Mutex
	order   *list.List // front is the most recently used
	entries map[string]*list.Element
}

type platformIconEntry struct {
	sha string
	png []byte
}

func newPlatformIconCache() *platformIconCache {
	return &platformIconCache{order: list.New(), entries: map[string]*list.Element{}}
}

func (c *platformIconCache) put(sha string, png []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[sha]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.entries[sha] = c.order.PushFront(&platformIconEntry{sha, png})
	for c.order.Len() > platformIconCacheEntries {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*platformIconEntry).sha)
	}
}

func (c *platformIconCache) get(sha string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[sha]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*platformIconEntry).png, true
}

// platformIcons is content addressed, so sharing it across servers in one process is harmless.
var platformIcons = newPlatformIconCache()

// platformDisplayView is the JSON form of a verified display block: the built-in
// icon key stays the fallback and a custom icon is referenced by hash only.
type platformDisplayView struct {
	Icon       string                       `json:"icon"`
	IconSHA256 string                       `json:"icon_sha256,omitempty"`
	Texts      []appplatform.AppDisplayText `json:"texts"`
}

// platformDisplayViewOf projects a display block from a verified descriptor and
// remembers its custom icon for the icon route. A nil display stays nil.
func platformDisplayViewOf(d *appplatform.AppDisplay) *platformDisplayView {
	if d == nil {
		return nil
	}
	view := &platformDisplayView{Icon: d.Icon, Texts: d.Texts}
	if png, sha, ok := appplatform.DisplayIcon(d); ok {
		platformIcons.put(sha, png)
		view.IconSHA256 = sha
	}
	return view
}

// platformDisplayViewUnverified projects a display block that has not been verified against publisher
// trust: no icon hash and nothing cached, so the built-in icon key is the only icon.
func platformDisplayViewUnverified(d *appplatform.AppDisplay) *platformDisplayView {
	if d == nil {
		return nil
	}
	return &platformDisplayView{Icon: d.Icon, Texts: d.Texts}
}

// platformManifestPublisherName is the verified manifest's self-declared name, empty when it has none.
func platformManifestPublisherName(m appplatform.Manifest) string {
	if m.PublisherName == nil {
		return ""
	}
	return *m.PublisherName
}

// platformIconRoute serves the cached canonical icons to any signed-in user, like the other
// platform reader routes. The body is immutable per hash, so the browser may keep it for a year.
func (s *Server) platformIconRoute(mux *http.ServeMux) {
	s.route(mux, "GET /api/app-platform/icons/{file}", authSession, "", s.requireUserJSON(func(w http.ResponseWriter, r *http.Request, _ string) { s.apiPlatformIcon(w, r) }))
}

func (s *Server) apiPlatformIcon(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	sha, named := strings.CutSuffix(r.PathValue("file"), ".png")
	if !named || !platformBlobDigest.MatchString(sha) {
		h.Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	png, ok := platformIcons.get(sha)
	if !ok {
		h.Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(png) //nolint:errcheck // client went away
}
