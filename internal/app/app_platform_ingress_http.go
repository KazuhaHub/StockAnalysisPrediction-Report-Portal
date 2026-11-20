package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The authenticated inbound route (doc 180 section 3.3):
//
//	POST /ingress/apps/{installation}/{route}/{endpoint}
//
// Nothing here reads a cookie, a bearer credential or a session, and no step before the forward
// writes to the database. Every authentication failure is the same 401 with the same body.

const (
	platformIngressAuditSummary = "app_platform.ingress_summary"
	// platformIngressMaxBody is the largest body any route may declare; a request over it is refused
	// before the route is even looked up.
	platformIngressMaxBody = 4096
	// platformIngressBodyDeadline bounds how long a sender may take to send its headers' promised body.
	platformIngressBodyDeadline = 10 * time.Second
)

// platformIngressURL is the path a sender posts to (the host is the Portal's own).
func platformIngressURL(installation, route, endpoint string) string {
	return "/ingress/apps/" + installation + "/" + route + "/" + endpoint
}

func platformIngressReply(w http.ResponseWriter, status int, body string, retryAfter int) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	io.WriteString(w, body)
}

const (
	platformIngressDenied   = `{"ok":false}`
	platformIngressAccepted = `{"ok":true}`
)

// platformIngressRefuse answers an authentication failure: always the one body, whatever the cause.
func platformIngressRefuse(w http.ResponseWriter) {
	platformIngressReply(w, 401, platformIngressDenied, 0)
}

func (s *Server) recordPlatformIngress(installation, endpoint, class string) {
	s.platformIngress.record(installation, endpoint, time.Now(), class)
}

// writePlatformIngressSummary is the aggregated traffic audit (doc 180 section 5): counts per
// window and class, never a client address.
func (s *Server) writePlatformIngressSummary(installation string, st *platformIngressStats) {
	detail, err := json.Marshal(st.detail(time.Now()))
	if err != nil {
		return
	}
	if _, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`, time.Now().UTC().Format(time.RFC3339), platformIngressAuditSummary, installation, string(detail)); err != nil {
		log.Printf("app-platform ingress audit: %v", err)
	}
}

// platformIngressSummaryLoop writes each traffic window within a minute of its end. Only this timer
// writes: a request never does.
func (s *Server) platformIngressSummaryLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	stop := s.platformIngress.stopped()
	for {
		select {
		case <-ticker.C:
			s.writePlatformIngressDue(time.Now())
		case <-stop:
			return
		}
	}
}

func (s *Server) writePlatformIngressDue(now time.Time) {
	for _, due := range s.platformIngress.takeDue(now) {
		s.writePlatformIngressSummary(due.installation, due.stats)
	}
}

// stopPlatformIngress writes every open window at shutdown.
func (s *Server) stopPlatformIngress(context.Context) {
	s.platformIngress.shutdown()
	for _, due := range s.platformIngress.takeAll() {
		s.writePlatformIngressSummary(due.installation, due.stats)
	}
}

// flushPlatformIngressSummary writes every open window now (tests).
func (s *Server) flushPlatformIngressSummary() {
	for _, due := range s.platformIngress.takeAll() {
		s.writePlatformIngressSummary(due.installation, due.stats)
	}
}

// platformIngressDummy spends the work of a signature check on a request whose endpoint does not
// exist, so that refusing it takes the same time class as refusing a wrong signature.
func platformIngressDummy(installation, route, endpoint, timestamp, nonce string, body []byte, header string) {
	appplatform.IngressVerify(make([]byte, appplatform.MinIngressSecret), installation, route, endpoint, timestamp, nonce, body, header)
}

func (s *Server) ingressRequest(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	installation, route, endpoint := r.PathValue("installation"), r.PathValue("route"), r.PathValue("endpoint")
	// 1. Static checks. Nothing here looks anything up: path shape, content type, body bound, header
	// shapes and the timestamp window. Only a body over every route's bound is a 413 here.
	if r.ContentLength > platformIngressMaxBody {
		platformIngressReply(w, 413, `{"ok":false,"code":"too_large"}`, 0)
		s.recordPlatformIngress("", "", "too_large")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(now.Add(platformIngressBodyDeadline))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformIngressMaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			platformIngressReply(w, 413, `{"ok":false,"code":"too_large"}`, 0)
			s.recordPlatformIngress("", "", "too_large")
			return
		}
		platformIngressRefuse(w)
		s.recordPlatformIngress("", "", "static")
		return
	}
	defer clear(body)
	timestamp, nonce, signature := r.Header.Get(appplatform.IngressTimestampHeader), r.Header.Get(appplatform.IngressNonceHeader), r.Header.Get(appplatform.IngressSignatureHeader)
	media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if _, inWindow := appplatform.IngressTimestampWithin(timestamp, now); !platformValidUUID(installation) || !appplatform.ValidIngressRouteID(route) || !appplatform.ValidIngressEndpoint(endpoint) ||
		mediaErr != nil || media != "application/json" || len(r.Header.Values(appplatform.IngressTimestampHeader)) != 1 || len(r.Header.Values(appplatform.IngressNonceHeader)) != 1 || len(r.Header.Values(appplatform.IngressSignatureHeader)) != 1 ||
		!appplatform.ValidIngressNonce(nonce) || !appplatform.ValidIngressSignatureHeader(signature) || !inWindow || !json.Valid(body) {
		platformIngressRefuse(w)
		s.recordPlatformIngress("", "", "static")
		return
	}
	// 2. The ceiling that applies before anything is looked up: per source. The per-installation budget is
	// charged after the signature has been verified (below).
	if !s.platformIngress.clients.allow(platformPublicClientKey(clientIP(r, s.trustedNets)), platformIngressClientPerMinute, time.Minute, now) {
		platformIngressReply(w, 429, `{"ok":false,"code":"rate_limited"}`, 60)
		s.recordPlatformIngress("", "", "rate_client")
		return
	}
	// 3. Authority without a session.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	light, err := s.readPlatformIngressLight(ctx, installation, route, endpoint)
	// Once the route is a declared one of a granted installation its own bound applies, before any
	// authentication work, whether or not the endpoint exists.
	if light.routeKnown && len(body) > light.decl.BodyBytes {
		platformIngressReply(w, 413, `{"ok":false,"code":"too_large"}`, 0)
		s.recordPlatformIngress(installation, "", "too_large")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, errPlatformIngressStore):
			s.recordPlatformIngress("", "", "store_error")
			platformIngressReply(w, 503, `{"ok":false,"code":"unavailable"}`, 30)
		case errors.Is(err, errPlatformIngressUnavailable):
			s.recordPlatformIngress(installation, endpoint, "unavailable")
			platformIngressReply(w, 503, `{"ok":false,"code":"unavailable"}`, 30)
		default:
			if light.bindingID == "" {
				platformIngressDummy(installation, route, endpoint, timestamp, nonce, body, signature)
			}
			if light.routeKnown {
				s.recordPlatformIngress(installation, "", "auth")
			} else {
				s.recordPlatformIngress("", "", "auth")
			}
			platformIngressRefuse(w)
		}
		return
	}
	s.recordPlatformIngress(installation, endpoint, "request")
	// 4. Decrypt inside the request, verify in constant time, clear the secret.
	secret, err := s.openPlatformCredentialValue(light.credentialID, light.valueRevision, light.sealed)
	if err != nil {
		s.recordPlatformIngress(installation, endpoint, "unavailable")
		platformIngressReply(w, 503, `{"ok":false,"code":"unavailable"}`, 30)
		return
	}
	valid := len(secret) >= appplatform.MinIngressSecret && len(secret) <= appplatform.MaxIngressSecret &&
		appplatform.IngressVerify(secret, installation, route, endpoint, timestamp, nonce, body, signature)
	clear(secret)
	if !valid {
		s.recordPlatformIngress(installation, endpoint, "auth")
		platformIngressRefuse(w)
		return
	}
	// 5. A nonce seen inside the window is a replay. Only an authenticated request reaches the cache.
	if !s.platformIngress.claimNonce(installation, endpoint, appplatform.IngressDigest([]byte(nonce)), now) {
		s.recordPlatformIngress(installation, endpoint, "replay")
		platformIngressRefuse(w)
		return
	}
	// The installation's budget is spent by authenticated requests only. Charged before authentication it
	// would be keyed on an identifier anyone can put in a URL, and a few sources sending junk signatures
	// could starve every real sender; what bounds the cost of junk is the per-client ceiling of step 2.
	if !s.platformIngress.installations.allow("installation:"+installation, platformIngressInstallationPerMinute, time.Minute, now) {
		platformIngressReply(w, 429, `{"ok":false,"code":"rate_limited"}`, 60)
		s.recordPlatformIngress(installation, endpoint, "rate_installation")
		return
	}
	// 6. The endpoint's own ceiling, from the descriptor.
	if !s.platformIngress.endpoints.allow("endpoint:"+installation+"|"+endpoint, light.decl.PerEndpointPerMinute, time.Minute, now) {
		platformIngressReply(w, 429, `{"ok":false,"code":"rate_limited"}`, 60)
		s.recordPlatformIngress(installation, endpoint, "rate_endpoint")
		return
	}
	// 7. Forward. The body is unchanged; the backend gets the verified fact and the nonce digest.
	s.recordPlatformIngress(installation, endpoint, "forwarded")
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Second)
	defer fcancel()
	response, err := s.requestPlatformIngressBackend(fctx, light, body, nonce, now)
	if err != nil {
		s.recordPlatformIngress(installation, endpoint, "backend_failure")
		platformIngressReply(w, 503, `{"ok":false,"code":"unavailable"}`, 30)
		return
	}
	defer clear(response.Body)
	// 8. Reply.
	switch {
	case response.Status >= 200 && response.Status < 300:
		s.recordPlatformIngress(installation, endpoint, "accepted")
		platformIngressReply(w, 202, platformIngressAccepted, 0)
	case response.Status >= 400 && response.Status < 500:
		s.recordPlatformIngress(installation, endpoint, "backend_failure")
		platformIngressReply(w, 400, `{"ok":false,"code":"rejected"}`, 0)
	default:
		s.recordPlatformIngress(installation, endpoint, "backend_failure")
		platformIngressReply(w, 503, `{"ok":false,"code":"unavailable"}`, 30)
	}
}

// requestPlatformIngressBackend runs one verified inbound POST through the same signed relay a
// viewer request uses, with the sessionless access and an ingress assertion in place of the viewer's.
func (s *Server) requestPlatformIngressBackend(ctx context.Context, l platformIngressLight, body []byte, nonce string, received time.Time) (driver.BackendResponse, error) {
	if hook := s.platformIngressForwardHook; hook != nil {
		return hook(ctx, l, body, nonce, received)
	}
	var zero driver.BackendResponse
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		return zero, errAppPlatformRuntimeConflict
	}
	return s.requestPlatformWritableBackendAs(ctx, platformActorSession{anonymousIngress: l.endpoint}, l.installation, nil, "POST", l.decl.Path, body, *endpoint, platformIngressAssertion(l, body, nonce, received))
}

// platformIngressAssertion signs the ingress assertion of one verified request. The access it is
// given was read just now: it must still show exactly the grant and binding this request was
// admitted under, and the generation the assertion binds is the access's own, never the caller's.
func platformIngressAssertion(l platformIngressLight, body []byte, nonce string, received time.Time) platformBackendAssertionFunc {
	return func(signer protocol.Signer, access platformWritableBackendAccess, now time.Time) (string, error) {
		if access.publicPolicy == "" || access.publicPolicy != l.grantKey() || access.intent.generation != l.generation {
			return "", errAppPlatformLaunchUnavailable
		}
		return appplatform.SignIngressAssertion(signer, access.intent.generation, l.route, l.endpoint, l.decl.Path, received, nonce, body, now)
	}
}
