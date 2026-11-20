package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The anonymous, read-only public projection routes (doc 179 section 3.3):
//
//	GET /public/apps/{installation}/data/{route}?{query}
//	GET /public/apps/{installation}/
//	GET /public/apps/{installation}/{asset...}
//
// Nothing here reads a cookie, a bearer credential or a session; a request that carries them
// is answered exactly as one without. The handlers see only the reviewed routes of a ready,
// enabled installation whose grant is on.

// platformPublicVerifyPerSecond bounds release verifications per installation. Only a failed or an
// invalidated verification repeats (a success is kept), and neither is something a visitor can cause.
const platformPublicVerifyPerSecond = 4

const (
	platformPublicDataCSP     = "default-src 'none'; frame-ancestors 'none'"
	platformPublicDocumentCSP = "sandbox allow-scripts; default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	// Before the policy is known (it is read from the installation) one source may not exceed the
	// largest per minute ceiling a policy can grant.
	platformPublicCoarseClientPerMinute = appplatform.MaxPublicClient
	platformPublicMaxDocumentBytes      = 8 << 20
)

var platformPublicAssetName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,240}$`)
var platformPublicRouteID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// platformPublicFail answers with a fixed code and nothing else. It is never cacheable.
func platformPublicFail(w http.ResponseWriter, status int, code string, retryAfter int) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", platformPublicDataCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	if retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":%q}`, code)
}

// platformPublicClassify turns an error of the authority read into the one answer for it.
func platformPublicClassify(w http.ResponseWriter, err error) string {
	if errors.Is(err, errPlatformPublicNotFound) {
		platformPublicFail(w, 404, "not_found", 0)
		return "not_found"
	}
	platformPublicFail(w, 503, "unavailable", 0)
	return "unavailable"
}

func (s *Server) recordPlatformPublic(installation, class string) {
	if done := s.platformPublic.record(installation, time.Now(), class); done != nil {
		go s.writePlatformPublicSummary(installation, done)
	}
}

// writePlatformPublicSummary is the aggregated traffic audit (doc 179 section 5): counts per
// window and class, never a client address.
func (s *Server) writePlatformPublicSummary(installation string, st *platformPublicStats) {
	detail, err := json.Marshal(st.detail(time.Now()))
	if err != nil {
		return
	}
	if _, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`, time.Now().UTC().Format(time.RFC3339), platformPublicSummaryAudit, installation, string(detail)); err != nil {
		log.Printf("app-platform public projection audit: %v", err)
	}
}

// platformPublicSummaryLoop writes each traffic window within a minute of its end, so a summary
// never waits for the next request and never covers more than its window.
func (s *Server) platformPublicSummaryLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	stop := s.platformPublic.stopped()
	for {
		select {
		case <-ticker.C:
			s.writePlatformPublicDue(time.Now())
		case <-stop:
			return
		}
	}
}

func (s *Server) writePlatformPublicDue(now time.Time) {
	for installation, st := range s.platformPublic.takeDue(now) {
		s.writePlatformPublicSummary(installation, st)
	}
}

// stopPlatformPublic writes every open window at shutdown. Its detail ends at the shutdown, not at
// the end the window would have had.
func (s *Server) stopPlatformPublic(context.Context) {
	s.platformPublic.shutdown()
	for installation, st := range s.platformPublic.takeAll() {
		s.writePlatformPublicSummary(installation, st)
	}
}

// flushPlatformPublicSummary writes an installation's open window now: on disable and in tests.
func (s *Server) flushPlatformPublicSummary(installation string) {
	if st := s.platformPublic.takeStats(installation); st != nil {
		s.writePlatformPublicSummary(installation, st)
	}
}

func (s *Server) publicClientLimit(w http.ResponseWriter, r *http.Request, installation string, limit int, now time.Time) bool {
	key := platformPublicClientKey(clientIP(r, s.trustedNets))
	if installation != "" {
		key += "|" + installation
	}
	if s.platformPublic.clients.allow(key, limit, time.Minute, now) {
		return true
	}
	platformPublicFail(w, 429, "rate_limited", 60)
	return false
}

// publicAdmit runs the common front of both route kinds. The order is the cost control: the source
// ceiling, one cheap indexed read (installation, grant, tuple), the reviewed per client and per
// installation ceilings, and only then the verified release, which is in memory unless this is the
// first request of a tuple. It has answered the request when ok is false.
func (s *Server) publicAdmit(w http.ResponseWriter, r *http.Request) (platformPublicAuthority, bool) {
	var zero platformPublicAuthority
	now := time.Now()
	installation := r.PathValue("installation")
	// 1. The ceiling that applies before anything is looked up, so the flood that is cheapest to
	// send is the first to be shed. The key is the source only: an unknown installation cannot
	// add one.
	if !s.publicClientLimit(w, r, "", platformPublicCoarseClientPerMinute, now) {
		return zero, false
	}
	if !platformValidUUID(installation) {
		platformPublicFail(w, 404, "not_found", 0)
		return zero, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	// The read is the one thing here that costs the database, and anyone can ask for it: past a
	// bounded number at once the overflow is refused, not queued.
	if !s.platformPublic.slots.acquire() {
		platformPublicFail(w, 503, "unavailable", 1)
		s.recordPlatformPublic("", "busy")
		return zero, false
	}
	light, err := s.readPlatformPublicLight(ctx, installation)
	s.platformPublic.slots.release()
	if err != nil {
		class := platformPublicClassify(w, err)
		// Nothing is counted for an installation that is unknown or has the projection off, and
		// an unavailable answer only exists for one whose grant is on.
		switch {
		case errors.Is(err, errPlatformPublicStore):
			// A database failure says nothing about this identifier: one global counter, never a
			// per identifier record.
			s.recordPlatformPublic("", "store_error")
		case class != "not_found":
			s.recordPlatformPublic(installation, class)
		}
		return zero, false
	}
	s.recordPlatformPublic(installation, "request")
	var chosen appplatform.PublicPolicy
	if json.Unmarshal([]byte(light.policyJSON), &chosen) != nil {
		platformPublicFail(w, 404, "not_found", 0)
		return zero, false
	}
	// 2. The reviewed ceilings, now that the policy is known. They are keyed by a real
	// installation, so they cannot be filled by guessing identifiers.
	if !s.publicClientLimit(w, r, installation, chosen.ClientPerMinute, now) {
		s.recordPlatformPublic(installation, "rate_client")
		return zero, false
	}
	if !s.platformPublic.installations.allow("installation:"+installation, platformPublicInstallationPerSecond, time.Second, now) {
		platformPublicFail(w, 429, "rate_limited", 1)
		s.recordPlatformPublic(installation, "rate_installation")
		return zero, false
	}
	s.platformPublic.useTuple(installation, light.tuple())
	verified, err := s.platformPublicVerifiedFor(light)
	if err != nil {
		s.publicLoadFailure(w, installation, err)
		return zero, false
	}
	return platformPublicAuthority{platformPublicLight: light, platformPublicVerified: verified}, true
}

// platformPublicVerifiedFor returns the verified release of a tuple from memory, building it once
// (single flight, at most platformPublicVerifyPerSecond builds a second per installation) when it is absent or has expired.
func (s *Server) platformPublicVerifiedFor(l platformPublicLight) (*platformPublicVerified, error) {
	key := strings.Join([]string{l.installation, l.tuple(), "verified"}, "\x00")
	// A release that could not be verified is left alone for a while: the answer is 503 at no cost,
	// not another archive read per request.
	if s.platformPublic.verifyBlocked(l.installation, l.tuple(), time.Now()) {
		return nil, errPlatformPublicUnavailable
	}
	entry, _, err := s.platformPublicLoad(key, func(ctx context.Context) (*platformPublicEntry, error) {
		end, ok := s.platformPublic.beginFetch(l.installation+"#verify", platformPublicVerifyPerSecond, time.Now())
		if !ok {
			// Not the visitor's rate: the Portal is busy verifying, so it is unavailable.
			return nil, errPlatformPublicUnavailable
		}
		defer end()
		v, err := s.verifyPlatformPublicRelease(ctx, l)
		if err != nil {
			s.platformPublic.noteVerifyFailure(l.installation, l.tuple(), time.Now())
			return nil, err
		}
		now := time.Now()
		entry := &platformPublicEntry{key: key, installation: l.installation, tuple: l.tuple(), verified: v, extra: v.filesBytes, cacheSeconds: int(platformPublicVerifiedTTL / time.Second), expires: now.Add(platformPublicVerifiedTTL)}
		s.platformPublic.store(entry, now)
		return entry, nil
	})
	if err != nil {
		return nil, err
	}
	return entry.verified, nil
}

func (s *Server) publicAppData(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.publicAdmit(w, r)
	if !ok {
		return
	}
	route, granted := auth.route(r.PathValue("route"))
	if !platformPublicRouteID.MatchString(r.PathValue("route")) || !granted {
		platformPublicFail(w, 404, "not_found", 0)
		s.recordPlatformPublic(auth.installation, "route_refused")
		return
	}
	query, err := appplatform.CanonicalPublicQuery(route.Query, r.URL.RawQuery)
	if err != nil {
		platformPublicFail(w, 400, "bad_query", 0)
		s.recordPlatformPublic(auth.installation, "bad_query")
		return
	}
	key := strings.Join([]string{auth.installation, auth.tuple(), "data", route.ID, query}, "\x00")
	entry, class, err := s.platformPublicLoad(key, func(ctx context.Context) (*platformPublicEntry, error) {
		return s.fetchPlatformPublicData(ctx, auth, route, query, key)
	})
	if err != nil {
		s.publicLoadFailure(w, auth.installation, err)
		return
	}
	s.recordPlatformPublic(auth.installation, class)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "public, max-age="+strconv.Itoa(remainingSeconds(entry, time.Now())))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", platformPublicDataCSP)
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Write(entry.body)
}

func remainingSeconds(e *platformPublicEntry, now time.Time) int {
	left := int(e.expires.Sub(now).Seconds() + 0.999)
	if left < 1 {
		left = 1
	}
	if left > e.cacheSeconds {
		left = e.cacheSeconds
	}
	return left
}

var (
	errPlatformPublicRate    = errors.New("public backend rate limited")
	errPlatformPublicRefused = errors.New("public route refused")
)

func (s *Server) publicLoadFailure(w http.ResponseWriter, installation string, err error) {
	switch {
	case errors.Is(err, errPlatformPublicRate):
		platformPublicFail(w, 429, "rate_limited", 1)
		s.recordPlatformPublic(installation, "rate_backend")
	case errors.Is(err, errPlatformPublicRefused), errors.Is(err, errPlatformPublicNotFound):
		platformPublicFail(w, 404, "not_found", 0)
		s.recordPlatformPublic(installation, "route_refused")
	default:
		platformPublicFail(w, 503, "unavailable", 0)
		s.recordPlatformPublic(installation, "unavailable")
	}
}

// platformPublicLoad serves a key from the cache or joins the single fetch of it. The leader runs
// load within the installation's backend ceiling; followers wait for its result. Only successes are
// kept: a refusal or failure is answered to the callers that waited and forgotten.
func (s *Server) platformPublicLoad(key string, load func(context.Context) (*platformPublicEntry, error)) (*platformPublicEntry, string, error) {
	now := time.Now()
	if entry := s.platformPublic.lookup(key, now); entry != nil {
		return entry, "hit", nil
	}
	flight, leader := s.platformPublic.flight(key)
	if !leader {
		select {
		case <-flight.done:
		case <-time.After(25 * time.Second):
			return nil, "", errPlatformPublicUnavailable
		}
		if flight.err != nil {
			return nil, "", flight.err
		}
		return flight.entry, "hit", nil
	}
	var entry *platformPublicEntry
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				entry, err = nil, errPlatformPublicUnavailable
			}
			s.platformPublic.finish(key, flight, entry, err)
		}()
		// A fetch started by this leader belongs to the cache, not to one request: a client that
		// goes away must not fail the callers waiting on it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 25*time.Second)
		defer cancel()
		entry, err = load(ctx)
	}()
	if err != nil {
		return nil, "", err
	}
	return entry, "fetch", nil
}

func (s *Server) fetchPlatformPublicData(ctx context.Context, a platformPublicAuthority, route appplatform.PublicRoute, query, key string) (*platformPublicEntry, error) {
	end, ok := s.platformPublic.beginFetch(a.installation, a.policy.BackendPerSecond, time.Now())
	if !ok {
		return nil, errPlatformPublicRate
	}
	defer end()
	response, err := s.requestPlatformPublicBackend(ctx, a, route, query)
	if err != nil {
		return nil, errPlatformPublicUnavailable
	}
	defer clear(response.Body)
	switch {
	case response.Status == 404:
		return nil, errPlatformPublicRefused
	case response.Status != 200 || len(response.Body) == 0 || len(response.Body) > route.ResponseBytes || !json.Valid(response.Body):
		return nil, errPlatformPublicUnavailable
	}
	now := time.Now()
	entry := &platformPublicEntry{key: key, installation: a.installation, tuple: a.tuple(), body: append([]byte(nil), response.Body...), contentType: "application/json", cacheSeconds: route.CacheSeconds, expires: now.Add(time.Duration(route.CacheSeconds) * time.Second)}
	s.platformPublic.store(entry, now)
	return entry, nil
}

// requestPlatformPublicBackend runs one reviewed GET through the same signed relay a viewer request
// uses, with the sessionless access and a public assertion in place of the viewer's.
func (s *Server) requestPlatformPublicBackend(ctx context.Context, a platformPublicAuthority, route appplatform.PublicRoute, query string) (driver.BackendResponse, error) {
	if hook := s.platformPublicFetchHook; hook != nil {
		return hook(ctx, a, route, query)
	}
	var zero driver.BackendResponse
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		return zero, errAppPlatformRuntimeConflict
	}
	target := appplatform.PublicRequestTarget(route.Path, query)
	return s.requestPlatformWritableBackendAs(ctx, platformActorSession{anonymousPublic: true}, a.installation, nil, "GET", target, nil, *endpoint, platformPublicAssertion(a, route, query))
}

// platformPublicAssertion signs the public assertion of one reviewed request. The access it is
// given was read just now: it must still show exactly the policy this request was admitted under,
// and the generation the assertion binds is the access's own, never the caller's.
func platformPublicAssertion(a platformPublicAuthority, route appplatform.PublicRoute, query string) platformBackendAssertionFunc {
	return func(signer protocol.Signer, access platformWritableBackendAccess, now time.Time) (string, error) {
		if access.publicPolicy == "" || access.publicPolicy != a.policyJSON || access.intent.generation != a.generation {
			return "", errAppPlatformLaunchUnavailable
		}
		return appplatform.SignPublicAssertion(signer, access.intent.generation, route.ID, route.Path, query, now)
	}
}

func (s *Server) publicAppDocument(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.publicAdmit(w, r)
	if !ok {
		return
	}
	asset := r.PathValue("asset")
	name := auth.declared.Document
	if asset != "" {
		if !platformPublicAssetName.MatchString(asset) || strings.Contains(asset, "..") || strings.Contains(asset, "//") || strings.HasPrefix(asset, "/") || strings.HasSuffix(asset, "/") {
			platformPublicFail(w, 404, "not_found", 0)
			s.recordPlatformPublic(auth.installation, "route_refused")
			return
		}
		name = "public/" + asset
	}
	if !auth.policy.Document || name == "" || r.URL.RawQuery != "" {
		platformPublicFail(w, 404, "not_found", 0)
		s.recordPlatformPublic(auth.installation, "route_refused")
		return
	}
	file, found := auth.files[name]
	if !found {
		platformPublicFail(w, 404, "not_found", 0)
		s.recordPlatformPublic(auth.installation, "route_refused")
		return
	}
	s.recordPlatformPublic(auth.installation, "hit")
	h := w.Header()
	h.Set("Content-Type", file.Metadata.ContentType)
	h.Set("Cache-Control", "public, max-age="+strconv.Itoa(platformPublicDocumentSeconds(auth.declared)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", platformPublicDocumentCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	// No Cross-Origin-Resource-Policy: the sandbox gives the page an opaque origin, so "same-origin"
	// would block the page's own scripts.
	w.Write(file.Content)
}

// platformPublicDocumentSeconds is the browser lifetime of a public file: the shortest cache time
// any route of the page declares, since the page and its data change together.
func platformPublicDocumentSeconds(declared appplatform.Public) int {
	seconds := 300
	for _, route := range declared.Routes {
		seconds = min(seconds, route.CacheSeconds)
	}
	return seconds
}
