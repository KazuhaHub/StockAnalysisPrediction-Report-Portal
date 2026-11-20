package app

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The authenticated machine read routes (doc 183 section 4.4):
//
//	GET /machine/apps/{installation}/data/{route}?{query}
//	GET /machine/apps/{installation}/openapi.json
//
// The credential is "Authorization: Bearer pmr_<handle>_<secret>" and nothing else: no cookie is read
// (a session changes nothing), no CORS header is sent, OPTIONS is not served, and nothing is cached.
// Every failure to authenticate is the same 401, so a missing, malformed, unknown, revoked, expired or
// foreign token, a source outside the token's list and an unknown installation cannot be told apart.

const (
	platformMachineCSP = "default-src 'none'; frame-ancestors 'none'"
	// platformMachinePassBytes bounds an app's 400, 404 or 422 body that is passed through.
	platformMachinePassBytes = 64 << 10
	platformMachineAuthWait  = 5 * time.Second
)

var platformMachineRouteID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func platformMachineHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", platformMachineCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
}

// platformMachineFail answers with a fixed code and nothing else.
func platformMachineFail(w http.ResponseWriter, status int, code string, retryAfter int) {
	platformMachineHeaders(w)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":%q}`, code)
}

// platformMachineClient is who a request is, after authentication: the token row and the source.
type platformMachineClient struct {
	token  platformMachineToken
	source string
}

func (s *Server) recordPlatformMachine(c *platformMachineClient, class string, n int) {
	token, installation, handle := "", "", ""
	if c != nil {
		token, installation, handle = c.token.ID, c.token.Installation, c.token.Handle
	}
	if done := s.platformMachine.record(token, installation, handle, time.Now(), class, n); done != nil {
		go s.writePlatformMachineSummary(done)
	}
}

// writePlatformMachineSummary is the aggregated traffic audit (doc 183 section 6.2): counts per window
// and class, never a client address, a header or a body.
func (s *Server) writePlatformMachineSummary(st *platformMachineStats) {
	detail, err := json.Marshal(st.detail(time.Now()))
	if err != nil {
		return
	}
	targetType, target := "app_platform_installation", st.installation
	if st.installation == "" {
		targetType = "app_platform"
	}
	if _, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,?,?,?,'')`, time.Now().UTC().Format(time.RFC3339), platformMachineSummaryAudit, targetType, target, string(detail)); err != nil {
		log.Printf("app-platform machine read audit: %v", err)
	}
}

// platformMachineSummaryLoop writes each traffic window within a minute of its end and reports tokens
// whose expiry has passed.
func (s *Server) platformMachineSummaryLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	stop := s.platformMachine.stopped()
	sweep := 0
	for {
		select {
		case <-ticker.C:
			s.writePlatformMachineDue(time.Now())
			if sweep++; sweep%10 == 0 {
				s.sweepPlatformMachineExpiry(time.Now())
			}
		case <-stop:
			return
		}
	}
}

func (s *Server) writePlatformMachineDue(now time.Time) {
	for _, st := range s.platformMachine.takeDue(now) {
		s.writePlatformMachineSummary(st)
	}
}

func (s *Server) stopPlatformMachine(context.Context) {
	s.platformMachine.shutdown()
	for _, st := range s.platformMachine.takeAll() {
		s.writePlatformMachineSummary(st)
	}
}

// flushPlatformMachineSummary writes the open windows of an installation's tokens now.
func (s *Server) flushPlatformMachineSummary(installation string) {
	for _, st := range s.platformMachine.takeInstallation(installation) {
		s.writePlatformMachineSummary(st)
	}
}

// sweepPlatformMachineExpiry writes one audit row for each token whose expiry passed in the last month
// and has none yet. Requests check expires_ms themselves: the sweep is only for reporting, and a
// restart repeats nothing because the audit row is the record.
func (s *Server) sweepPlatformMachineExpiry(now time.Time) {
	rows, err := s.st.query(`SELECT installation_id,handle,label,expires_ms FROM app_platform_machine_tokens WHERE state='active' AND expires_ms<=? AND expires_ms>? ORDER BY expires_ms LIMIT 100`, now.UnixMilli(), now.Add(-30*platformMachineDay).UnixMilli())
	if err != nil {
		log.Printf("app-platform machine token expiry: %v", err)
		return
	}
	type expired struct {
		installation, handle, label string
		expires                     int64
	}
	var due []expired
	for rows.Next() {
		var x expired
		if rows.Scan(&x.installation, &x.handle, &x.label, &x.expires) == nil {
			due = append(due, x)
		}
	}
	rows.Close()
	for _, x := range due {
		var n int
		pattern := `%"handle":"` + x.handle + `"%`
		if err := s.st.queryRow(`SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=? AND detail LIKE ?`, platformMachineTokenExpiredAudit, x.installation, pattern).Scan(&n); err != nil || n > 0 {
			continue
		}
		detail, _ := json.Marshal(map[string]any{"handle": x.handle, "label": x.label, "expires_ms": x.expires})
		if _, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`, now.UTC().Format(time.RFC3339), platformMachineTokenExpiredAudit, x.installation, string(detail)); err != nil {
			log.Printf("app-platform machine token expiry audit: %v", err)
		}
	}
}

// noteMachineSource records the source address of a token once a day, in audit only, so an
// administrator can spot a leaked token used from a new place (doc 183 section 11, Q9).
func (s *Server) noteMachineSource(c *platformMachineClient, now time.Time) {
	if !s.platformMachine.firstSourceToday(c.token.ID, now) {
		return
	}
	since := now.UTC().Format("2006-01-02") + "T00:00:00Z"
	var n int
	pattern := `%"handle":"` + c.token.Handle + `"%`
	if err := s.st.queryRow(`SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=? AND detail LIKE ? AND at>=?`, platformMachineTokenSourceAudit, c.token.Installation, pattern, since).Scan(&n); err != nil || n > 0 {
		return
	}
	detail, _ := json.Marshal(map[string]any{"handle": c.token.Handle, "label": c.token.Label, "address": c.source})
	if _, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`, now.UTC().Format(time.RFC3339), platformMachineTokenSourceAudit, c.token.Installation, string(detail)); err != nil {
		log.Printf("app-platform machine token source audit: %v", err)
	}
}

func (s *Server) touchMachineToken(c *platformMachineClient, now time.Time) {
	if !s.platformMachine.shouldTouch(c.token.ID, now) {
		return
	}
	if _, err := s.st.exec(`UPDATE app_platform_machine_tokens SET last_used_ms=? WHERE id=? AND (last_used_ms IS NULL OR last_used_ms<?)`, now.UnixMilli(), c.token.ID, now.UnixMilli()); err != nil {
		log.Printf("app-platform machine token last use: %v", err)
	}
}

// machineAuthenticate is steps 1 to 4 of the processing order: the per-source ceiling, the
// Authorization header, the one indexed read of the token, and the per-token limit. It has answered
// the request when ok is false. Nothing about the installation is read before the token is known to
// be valid for it.
func (s *Server) machineAuthenticate(w http.ResponseWriter, r *http.Request) (*platformMachineClient, bool) {
	now := time.Now()
	source := clientIP(r, s.trustedNets)
	// 1. The ceiling that applies before anything is looked up, so the flood that is cheapest to send
	// is the first to be shed.
	if !s.platformMachine.clients.allow(platformPublicClientKey(source), platformMachineSourcePerMinute, time.Minute, now) {
		platformMachineFail(w, 429, "rate_limited", 60)
		s.recordPlatformMachine(nil, "rate_source", 0)
		return nil, false
	}
	s.recordPlatformMachine(nil, "request_unauthenticated", 0)
	// 2. Exactly one Authorization header, exactly "Bearer pmr_<handle>_<secret>". The Cookie header
	// is never read.
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		platformMachineFail(w, 401, "unauthorized", 0)
		s.recordPlatformMachine(nil, "unauthorized", 0)
		return nil, false
	}
	handle, secret, err := appplatform.ParseMachineAuthorization(values[0])
	if err != nil {
		platformMachineFail(w, 401, "unauthorized", 0)
		s.recordPlatformMachine(nil, "unauthorized", 0)
		return nil, false
	}
	// 3. One indexed read, bounded in concurrency because it is reachable before authentication.
	if !s.platformMachine.slots.acquire() {
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(nil, "busy", 0)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), platformMachineAuthWait)
	defer cancel()
	token, err := s.lookupPlatformMachineToken(ctx, handle)
	s.platformMachine.slots.release()
	if err != nil {
		token = platformMachineToken{}
		if !errors.Is(err, errPlatformMachineTokenGone) && !errors.Is(err, sql.ErrNoRows) {
			// A database failure says nothing about the token: it is not the caller's to learn.
			platformMachineFail(w, 503, "unavailable", 1)
			s.recordPlatformMachine(nil, "store_error", 0)
			return nil, false
		}
	}
	reason, valid := token.admits(appplatform.MachineSecretDigest(secret), r.PathValue("installation"), now, source)
	if !valid {
		platformMachineFail(w, 401, "unauthorized", 0)
		if token.ID != "" && reason != "unauthorized" {
			// The secret was right, so the refusal is attributable to the token; the caller is told nothing.
			s.recordPlatformMachine(&platformMachineClient{token: token, source: source}, reason, 0)
		} else {
			s.recordPlatformMachine(nil, "unauthorized", 0)
		}
		return nil, false
	}
	client := &platformMachineClient{token: token, source: source}
	s.recordPlatformMachine(client, "request", 0)
	// 4. The token's own ceiling.
	if !s.platformMachine.tokens.allow("token:"+token.ID, int(token.PerMinute), time.Minute, now) {
		platformMachineFail(w, 429, "rate_limited", 60)
		s.recordPlatformMachine(client, "rate_token", 0)
		return nil, false
	}
	s.noteMachineSource(client, now)
	s.touchMachineToken(client, now)
	return client, true
}

// machineAuthority is step 5: the session-less authority read, bounded in concurrency, answered per
// section 7 of the doc. It has answered the request when ok is false.
func (s *Server) machineAuthority(w http.ResponseWriter, r *http.Request, c *platformMachineClient) (platformMachineAuthority, bool) {
	var zero platformMachineAuthority
	if !s.platformMachine.slots.acquire() {
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(c, "busy", 0)
		return zero, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), platformMachineAuthWait)
	defer cancel()
	a, err := s.readPlatformMachineAuthority(ctx, c.token.Installation)
	s.platformMachine.slots.release()
	switch {
	case err == nil:
		return a, true
	case errors.Is(err, errPlatformMachineRefused):
		// The capability is off: the token no longer works, and says so the way every dead token does.
		platformMachineFail(w, 401, "unauthorized", 0)
		s.recordPlatformMachine(c, "capability_off", 0)
	case errors.Is(err, errPlatformMachineStore):
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(c, "store_error", 0)
	default:
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(c, "unavailable", 0)
	}
	return zero, false
}

func (s *Server) machineAppData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		machineMethodNotAllowed(w)
		return
	}
	client, ok := s.machineAuthenticate(w, r)
	if !ok {
		return
	}
	auth, ok := s.machineAuthority(w, r, client)
	if !ok {
		return
	}
	id := r.PathValue("route")
	route, declared := auth.route(id)
	if !platformMachineRouteID.MatchString(id) || !declared {
		platformMachineFail(w, 404, "not_found", 0)
		s.recordPlatformMachine(client, "route_unknown", 0)
		return
	}
	effective := client.token.effectiveRoutes(auth.policy)
	if !containsString(effective, id) {
		// Safe to say: the caller has proven possession of a valid token.
		platformMachineFail(w, 403, "route_not_permitted", 0)
		s.recordPlatformMachine(client, "route_not_permitted", 0)
		return
	}
	query, err := appplatform.CanonicalMachineQuery(route.Query, r.URL.RawQuery)
	if err != nil {
		platformMachineFail(w, 400, "bad_query", 0)
		s.recordPlatformMachine(client, "bad_query", 0)
		return
	}
	// 7. The installation's per-second rate and concurrency, now that the reviewed policy is known.
	if !s.platformMachine.installations.allow("installation:"+auth.installation, auth.policy.InstallationPerSecond, time.Second, time.Now()) {
		platformMachineFail(w, 429, "rate_limited", 1)
		s.recordPlatformMachine(client, "rate_installation", 0)
		return
	}
	end, admitted := s.platformMachine.beginBackend(auth.installation, auth.policy.InstallationConcurrency)
	if !admitted {
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(client, "busy_installation", 0)
		return
	}
	defer end()
	s.recordPlatformMachine(client, "backend", 0)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Duration(route.TimeoutMS)*time.Millisecond)
	defer cancel()
	response, err := s.requestPlatformMachineBackend(ctx, auth, client.token, route, query)
	if err != nil {
		platformMachineFail(w, 503, "unavailable", 1)
		s.recordPlatformMachine(client, "unavailable", 0)
		return
	}
	defer clear(response.Body)
	switch {
	case response.Status == 200 && len(response.Body) > 0 && len(response.Body) <= route.ResponseBytes && json.Valid(response.Body):
		platformMachineHeaders(w)
		w.WriteHeader(200)
		w.Write(response.Body)
		s.recordPlatformMachine(client, "bytes", len(response.Body))
	case (response.Status == 400 || response.Status == 404 || response.Status == 422) && len(response.Body) > 0 && len(response.Body) <= platformMachinePassBytes && json.Valid(response.Body):
		// The app's own validation message reaches the tool; nothing else of the app's answer does.
		platformMachineHeaders(w)
		w.WriteHeader(response.Status)
		w.Write(response.Body)
		s.recordPlatformMachine(client, "bytes", len(response.Body))
		s.recordPlatformMachine(client, "app_"+strconv.Itoa(response.Status), 0)
	case response.Status == 429:
		platformMachineFail(w, 429, "rate_limited", 1)
		s.recordPlatformMachine(client, "app_429", 0)
	default:
		platformMachineFail(w, 502, "backend_error", 0)
		s.recordPlatformMachine(client, "backend_error", 0)
	}
}

// requestPlatformMachineBackend runs one reviewed GET through the same signed relay a viewer request
// uses, with the sessionless access and a machine assertion in place of the viewer's.
func (s *Server) requestPlatformMachineBackend(ctx context.Context, a platformMachineAuthority, token platformMachineToken, route appplatform.MachineRoute, query string) (driver.BackendResponse, error) {
	if hook := s.platformMachineFetchHook; hook != nil {
		return hook(ctx, a, token, route, query)
	}
	var zero driver.BackendResponse
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		return zero, errAppPlatformRuntimeConflict
	}
	target := appplatform.MachineRequestTarget(route.Path, query)
	return s.requestPlatformWritableBackendAs(ctx, platformActorSession{anonymousMachine: token.ID}, a.installation, nil, "GET", target, nil, *endpoint, platformMachineAssertion(a, token, route, query))
}

// platformMachineAssertion signs the machine assertion of one reviewed request. The access it is given
// was read just now: it must still show exactly the policy and the token this request was admitted
// under, and the generation the assertion binds is the access's own, never the caller's. The secret is
// not in it; the app learns the token's handle and label.
func platformMachineAssertion(a platformMachineAuthority, token platformMachineToken, route appplatform.MachineRoute, query string) platformBackendAssertionFunc {
	want := a.policyJSON + "\x00" + token.ID + "\x00" + strconv.FormatInt(token.Revision, 10)
	return func(signer protocol.Signer, access platformWritableBackendAccess, now time.Time) (string, error) {
		if access.machinePolicy == "" || subtle.ConstantTimeCompare([]byte(access.machinePolicy), []byte(want)) != 1 || access.intent.generation != a.generation {
			return "", errAppPlatformLaunchUnavailable
		}
		return appplatform.SignMachineAssertion(signer, access.intent.generation, route.ID, route.Path, query, token.Handle, token.Label, now)
	}
}

// machineOther answers a GET of an unknown path under /machine/ with a 404, never the application's
// single page. HEAD, which the mux routes with GET, is refused like every other method.
func (s *Server) machineOther(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		machineMethodNotAllowed(w)
		return
	}
	platformMachineFail(w, 404, "not_found", 0)
}

// machineMethodNotAllowed is the 405 of this surface: no body, no CORS header. (The mux sends the same
// for every method that has no registration; this is for HEAD.)
func machineMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMethodNotAllowed)
}
