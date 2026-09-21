package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/captcha"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// pdf.html is the only server-side template kept (for PDF export); all pages are rendered by the React SPA (web/dist).
//
//go:embed templates/pdf.html
var tplFS embed.FS

const cookieName = "rp_session"

type Server struct {
	cfg                *config.Config
	st                 *Store
	names              *Names
	pdf                *template.Template
	geoUp              *geoUpdater                                                                // downloads the IP database; the reader notices the new file by itself
	geo                *geoService                                                                // IP → place for the audit log; nil-safe, empty until a .mmdb is installed
	proxySeen          atomic.Value                                                               // bool: a forwarded request arrived from a peer not in trusted_proxies
	seenAt             sync.Map                                                                   // username -> time.Time of the last activity stamp WRITTEN (throttle; see touchSeen)
	jobRuns            sync.Map                                                                   // jobID -> *jobRun; shared cancel scope for a job's in-flight runs (ADR 0011)
	itemCancels        sync.Map                                                                   // itemID -> context.CancelFunc; per-row cancel of an in-flight run (ADR 0011)
	jobNotify          sync.Map                                                                   // jobID -> bool; opt-in to email the submitter when the job finishes
	buildProv          func(BatchJob, func(runID, convID, taskID string)) (batch.Provider, error) // test seam for the run-item provider; nil → real buildProvider
	schedMu            sync.Mutex                                                                 // serializes scheduleTick (admission + finalize) so ticks can't over-admit or double-finalize (ADR 0004/0011)
	mailFn             func(to []string, subject, htmlBody string) error                          // test seam; nil → real SMTP send
	appTok             *appTokens                                                                 // short-lived scoped tokens for the iframe-app /api/v1 bridge (ADR 0003)
	chatMu             sync.Mutex                                                                 // guards chatLive/chatSeq
	chatLive           map[int64]*chatTurn                                                        // in-flight chat turns; independent ceiling + admin live view (ADR 0012), NOT the run queue
	chatSeq            int64                                                                      // monotonic in-flight chat-turn id
	cleanupMu          sync.Mutex                                                                 // serializes a storage-cleanup pass so the scheduled ticker and a manual "clean now" never overlap (ADR 0017)
	loginThr           *loginThrottle                                                             // per-IP + per-account failed-login rate limiter (brute-force + bcrypt DoS)
	v1Rate             *rateLimiter                                                               // per-source request ceiling on the machine API (limits.go); off until configured
	trustedNets        []*net.IPNet                                                               // reverse proxies allowed to supply the client IP chain
	mermaidCharts      mermaidChartCache                                                          // user-scoped, bounded rendered SVG cache for PDF export (ADR 0020)
	quotes             quoteCache                                                                 // vendor quotes: its OWN bounded LRU + single-flight + upstream ceiling; lazily initialised (quote_cache.go)
	ssoInsecureForTest bool                                                                       // test-only: permit a plain-http loopback IdP (ADR 0023)
	dekOnce            dekCache                                                                   // lazily unwrapped data key for stored auth secrets (ADR 0023)
	captchaSvc         *captcha.Service                                                           // public-form captcha (login / forgot password / registration)
	health             healthCache                                                                // memoized /healthz database verdict, so a public probe cannot be a query amplifier
	releases           releaseCatalog                                                             // GitHub Releases is the sole authority for notes and maturity
	routeTable         []routeRow                                                                 // every registered route and the credential it accepts (routes.go)
}

// statusRecorder records the response status code for use in request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// slowRequest is the latency at which a successful API read becomes worth a log line on its own.
// A page that takes a second is a complaint waiting to happen, and it is the one class of problem
// that leaves no other trace: no error, no audit entry, nothing.
const slowRequest = time.Second

// logMiddleware logs requests: method, status, latency, client address, path.
//
// It used to skip /api/ entirely, with a comment claiming those endpoints "have their own concise
// logs". They do not — a handful of handlers log an event apiece (an ingest, an app install) and the
// rest log nothing at all. So a 500 on an unaudited endpoint left NO trace anywhere: the audit log
// covers mutations it was told about, and reads and failures were invisible.
//
// The reason /api/ was skipped is real though: the SPA polls (home feed, announcements, queue badge)
// on a timer, per open tab, so logging every API request would bury everything else. The rule that
// keeps both properties is to log every API request EXCEPT a fast, successful read — which is
// exactly the polling traffic and nothing else. Errors, every mutation, and anything slow are
// always logged, and nothing that was logged before has stopped being logged.
//
// Static assets and the health probe stay silent: they are high-volume and say nothing.
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/assets/") || strings.HasPrefix(p, "/app-assets/") || strings.HasPrefix(p, "/site-assets/") ||
			p == "/healthz" || p == "/favicon.svg" || p == "/favicon.ico" || p == "/manifest.webmanifest" ||
			p == "/pwa-icon" || p == "/sw.js" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		took := time.Since(start)
		if !worthLogging(p, r.Method, sw.status, took) {
			return
		}
		path, _ := url.QueryUnescape(r.URL.RequestURI())
		// The client address, resolved through the same trusted-proxy rules the rate limiter and the
		// audit log use — so a reverse-proxied deployment logs the visitor rather than the gateway,
		// and an untrusted upstream cannot claim to be someone else.
		log.Printf("%-4s %3d %7s  %-15s %s", r.Method, sw.status, took.Round(time.Millisecond).String(),
			clientIP(r, s.trustedNets), path)
	})
}

// worthLogging decides whether one finished request earns a line. Split out because it is the whole
// policy, and a policy buried in an if-chain inside a middleware is one nobody can test.
func worthLogging(path, method string, status int, took time.Duration) bool {
	if status >= 400 || took >= slowRequest {
		return true // a failure or a slow request is always worth a line, whatever it was
	}
	if !strings.HasPrefix(path, "/api/") {
		return true // page loads are low-volume; they were logged before and still are
	}
	// A fast, successful API read is the SPA's own polling. Everything else on /api/ — every
	// mutation included — is a thing somebody did.
	return method != http.MethodGet && method != http.MethodHead
}

// RunServer loads config, opens the store, bootstraps first-run state, wires the
// HTTP routes, and serves until it errors. The CLI (cmd/report-portal) calls this.
func RunServer(cfgPath string) {
	cfg, err := config.EnsureConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config %s: %v", cfgPath, err)
	}
	if err := validateSessionSecret(cfg.SecretKey); err != nil {
		log.Fatalf("config: %v", err)
	}
	trustedNets, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		log.Fatalf("config: trusted_proxies: %v", err)
	}
	if trustsEverything(trustedNets) {
		// Loud, because it is correct exactly once — behind a listener nothing but the proxy can
		// reach — and an operator who set it for a Docker network and later published the port
		// would have no other reminder that any caller can now claim any address.
		log.Printf("WARNING: trusted_proxies is set to trust EVERY upstream. " +
			"Any caller can then claim any address via X-Forwarded-For, which spoofs the rate " +
			"limiter and the audit log. Only safe when the listen port is unreachable except " +
			"through your proxy.")
	}
	// Said before anything else touches the database, because it is about the file the operator is
	// most likely to have copied into place by hand.
	if msg := config.WarnIfWorldReadable(cfgPath); msg != "" {
		log.Print(msg)
	}
	if err := os.MkdirAll(config.DirOf(cfg.DBPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if st.CountUsers() == 0 { // first run with no accounts → generate a random admin and print it to the terminal
		pw := randPassword(14)
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err := st.UpsertUser(User{Username: "admin", PasswordHash: string(h), Role: "admin"}); err != nil {
			log.Fatalf("create initial admin: %v", err)
		}
		bar := strings.Repeat("=", 52)
		log.Printf("\n%s\n  first run: created admin account\n    username: admin\n    password: %s\n  log in and change the password in Users soon.\n%s", bar, pw, bar)
	}
	s := &Server{cfg: cfg, st: st, appTok: newAppTokens(30 * time.Minute), loginThr: newLoginThrottle(), v1Rate: newRateLimiter(),
		trustedNets: trustedNets, captchaSvc: captcha.New()}
	// The failed-login ceiling and its window are settings (limits.go); handing the throttle the
	// reader rather than the numbers is what makes a change on the settings page apply to the next
	// attempt instead of at the next restart.
	s.loginThr.limits = s.loginLimits
	// The lockout is the same shape: a reader, so turning it on or off takes effect on the next
	// attempt rather than at the next restart.
	s.loginThr.lockout = s.loginLockout
	s.names = LoadNames(config.DirOf(cfg.DBPath), st)
	s.geo = newGeoService(config.DirOf(cfg.DBPath))
	s.geo.st = st
	s.geoUp = newGeoUpdater(s.geo, st, s.ssoClient)
	go s.geoUp.autoLoop() // refreshes on a timer; MaxMind's licence requires a refresh every 30 days
	s.names.ensureFull()  // if the full list is missing, do a best-effort background fetch once
	s.parseTemplates()

	if len(st.TypeConfigs()) == 0 { // on first run, seed our real report types so the Types page isn't empty
		n := seedDefaultTypes(st)
		log.Printf("seeded %d default report types", n)
	}

	if len(st.KindColors()) == 0 { // on first run, seed the shipped kind→color mapping (admin-editable afterward)
		n := seedDefaultKindColors(st)
		log.Printf("seeded %d default kind colors", n)
	}

	// Account names are folded from here on, but a database written before that may already hold a
	// pair. They share one read principal, so each sees what the other was granted — say it out loud
	// rather than let an admin find out through the reports. Not fatal: refusing to start over data
	// the portal itself created would be the worse failure.
	if dupes := st.CaseVariantUsernames(); len(dupes) > 0 {
		log.Printf("WARNING: %d username(s) exist in more than one capitalisation (%s). "+
			"Accounts differing only by case share one read principal and can see each other's "+
			"reports — rename or delete all but one of each.", len(dupes), strings.Join(dupes, ", "))
	}

	// Bundled Dify adapter (docs/adr/0006-dify-native.md): a marker plugin every Dify
	// target references, so admins configure a workflow by pasting its API key — no
	// manifest import needed.
	if _, ok := st.GetPlugin(difyPluginSlug); !ok {
		st.UpsertPlugin(difyPluginSlug, "Dify Workflow", "1.0.0", "{}", "bundled")
	}

	s.resumeBatchJobs()  // requeue items left in-flight by a restart and relaunch running jobs
	go s.scheduleLoop()  // release one-shot 定时 jobs when their run_at passes (ADR 0007)
	go s.cleanupLoop()   // run the admin-configured storage-retention pass on its cadence (ADR 0017)
	go s.recurringLoop() // fire recurring tasks into the run queue on their cadence (ADR 0018)
	go s.authSweepLoop() // drop expired pending logins + SAML replay entries (ADR 0023)

	mux := http.NewServeMux()
	s.wireRoutes(mux)

	log.Printf("report-portal %s | listen %s | db %s | reports:%d", version.String(), cfg.Listen, cfg.DBDriver, st.CountNew())
	// Explicit timeouts so a slow/idle client can't pin a goroutine+FD indefinitely (Slowloris).
	// ReadHeaderTimeout bounds header dribble; IdleTimeout reaps idle keep-alives. ReadTimeout and
	// WriteTimeout are deliberately left 0 (unbounded): SSE run/chat streams and PDF/zip exports are
	// long-lived on the write side, and app-zip/asset uploads can be slow on the read side — each is
	// bounded by its own MaxBytesReader / context deadline instead.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.logMiddleware(gzipMiddleware(securityHeadersMiddleware(mux))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serve(srv, st)
}

// shutdownGrace bounds how long a stop waits for requests already in flight. Deliberately shorter
// than the compose file's stop_grace_period, so the portal's own ending is the one that happens
// rather than a SIGKILL arriving in the middle of it.
//
// Long enough for a PDF export or a report upsert to finish; not long enough to wait out an SSE
// stream, which by construction never ends on its own. Those are closed at the deadline.
const shutdownGrace = 10 * time.Second

// serve runs the HTTP server until SIGINT or SIGTERM, then stops taking new connections and lets the
// requests already in flight finish.
//
// Without this, `docker compose restart`, a rolling update and Ctrl-C all cut every open response
// mid-body: a report upsert answered with a truncated JSON body the caller has to guess about, an
// export half-written, an audit entry that may or may not have been recorded.
//
// The background loops (scheduling, cleanup, recurring tasks, the auth sweep) are NOT drained. They
// are tickers doing whole units of work, and a batch run interrupted by a restart is already the
// case ADR 0015 exists for — the next boot reconciles it from the queue rather than trusting that
// the previous process finished anything. Waiting on them would trade that guarantee for a slower
// stop and no more safety.
func serve(srv *http.Server, st *Store) {
	// Registered BEFORE the listener starts. The other order has a window — short, but real — in
	// which the process is already serving and a SIGTERM still has its default disposition, which is
	// to die on the spot: the exact ungraceful stop this function exists to prevent.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		// The listener failed to start (a port already taken is the usual one), which is fatal and
		// has nothing to do with shutting down.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		return
	case sig := <-stop:
		log.Printf("shutting down on %v; finishing in-flight requests (up to %s)", sig, shutdownGrace)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		// The deadline passed with connections still open — an SSE stream, or a client that stopped
		// reading. Close them: the alternative is hanging until the orchestrator's own patience runs
		// out and it sends SIGKILL, which is the ungraceful stop this exists to avoid.
		log.Printf("shutdown: %v; closing remaining connections", err)
		srv.Close()
	}
	// Last, and after the handlers are done: closing it earlier would fail the very requests the
	// grace period was granted for.
	if err := st.Close(); err != nil {
		log.Printf("closing the database: %v", err)
	}
	log.Printf("stopped")
}

// securityHeadersMiddleware supplies browser defenses consistently for JSON, downloads,
// static assets, and error responses. Route-specific CSP headers remain authoritative because
// handlers can overwrite the default-free header set below.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// Everything under /api/ and /report/ is answered against a session cookie and is
		// specific to that user. A response carrying no Cache-Control leaves the decision to
		// whatever cache is in the path — a browser's heuristics, or the reverse proxy an
		// operator has put in front of this binary — so say it outright rather than rely on
		// nobody having enabled one. Set before the handler runs, so a handler with its own
		// considered answer (the SSE stream, a token response) still overrides it.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/report/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// validateSessionSecret rejects missing, example, and undersized keys before the server starts.
// A known or guessable key lets an attacker forge an administrator session because the cookie is
// intentionally stateless. The generated default is 32 random bytes encoded as 64 hex digits.
func validateSessionSecret(secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return fmt.Errorf("secret_key is empty; remove a missing config file to generate a random one")
	}
	if secret == "replace-with-a-long-random-string" {
		return fmt.Errorf("secret_key still has the public example value; generate one with `openssl rand -hex 32`")
	}
	if len(secret) < 32 {
		return fmt.Errorf("secret_key must contain at least 32 characters of random data")
	}
	return nil
}

// handleHealthz is the public, unauthenticated liveness probe. It returns nothing but liveness —
// no data counts (business volume) and no build identity (version/commit), both of which would
// help an anonymous scanner fingerprint the instance. Ops read the build from /api/version (which
// requires a session) or the server logs.
// handleVersion returns build identity for the signed-in app footer. It is session-gated
// (registered behind requireUserJSON) precisely so version/commit are NOT exposed to anonymous
// callers: commit especially pins the exact public source, making CVE fingerprinting trivial.
//
// It also carries the effective update-prompt policy, so the poll that already tells an open tab a
// deploy landed also tells it how hard the portal is insisting — an admin escalating the policy
// reaches a tab nobody has reloaded.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request, user string) {
	policy := s.updatePromptPolicy()
	clientPolicy := policy
	automatic := policy == policyAutomatic
	// Bundles released before automatic mode cannot refresh themselves. Give them the persistent
	// banner fallback; current bundles read the explicit flag and refresh themselves.
	if automatic {
		clientPolicy = policyPersistent
	}
	writeJSON(w, map[string]any{
		"version":            version.Version,
		"commit":             version.Commit,
		"buildDate":          version.BuildDate,
		"updatePromptPolicy": clientPolicy,
		"automaticUpdate":    automatic,
	})
}

// AddUser creates or updates an account from the CLI (lockout fallback).
func AddUser(cfgPath, name, pw string, admin bool) error {
	if err := validateNewPassword(pw); err != nil {
		return err
	}
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return err
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
	role := "user"
	if admin {
		role = "admin"
	}
	// This is the lockout fallback, so it must not refuse — but it still folds, or an admin
	// recovering access as "Admin" would create a second account beside the real one.
	return st.UpsertUser(User{Username: normalizeUsername(name), PasswordHash: string(h), Role: role})
}

// RecomputeKinds opens the store and re-derives every report's top-level kind with
// the current taxonomy rules (returns rows updated). Behind the `recompute-kinds` CLI.
func RecomputeKinds(cfgPath string) (int, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, err
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.RecomputeKinds()
}

// FreezeReportNames snapshots the current name onto every un-named report row so a later
// rename never rewrites history (run once after backfilling stock names; idempotent).
func FreezeReportNames(cfgPath string) (int64, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, err
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.FreezeReportNames()
}

// FetchNames fetches the full A-share name list to <data-dir>/names.json and
// returns the count and the written path.
func FetchNames(cfgPath string) (int, string, error) {
	dir := "data"
	if c, err := config.EnsureConfig(cfgPath); err == nil {
		dir = config.DirOf(c.DBPath)
	}
	n, err := FetchNamesToFile(dir)
	return n, dir + "/names.json", err
}

// randPassword generates a random password (excluding easily confused characters 0/O/1/l/I).
func randPassword(n int) string {
	const cs = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = cs[i%len(cs)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = cs[int(b[i])%len(cs)]
	}
	return string(b)
}

// randToken generates an ingest API token (48 hex digits).
func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Templates ----------

// parseTemplates parses only the PDF export template (pages are handled by the React SPA).
func (s *Server) parseTemplates() {
	funcs := template.FuncMap{
		"safe": func(s string) template.HTML { return template.HTML(s) },
		"trunc10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
	s.pdf = template.Must(template.New("pdf.html").Funcs(funcs).ParseFS(tplFS, "templates/pdf.html"))
}

// ---------- Session / auth ----------

// defaultSessionTTL is how long a portal session lasts on a portal that has not said otherwise.
// The lifetime in force is s.sessionTTL() (limits.go), which reads the setting and falls back to
// this; an SSO provider may shorten it further for its own users (session_hours), which is why
// signing takes a duration at all.
const defaultSessionTTL = 7 * 24 * time.Hour

// setSessionCookie is the ONE place a portal session cookie is minted. Four call sites used to spell
// the flags out by hand — password login, the 2FA second leg, the passkey second leg and SSO — and a
// single one of them forgetting HttpOnly or Secure is a session-theft bug that no test would notice.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, u User) {
	s.setSessionCookieFor(w, r, u, s.sessionTTL())
}

func (s *Server) setSessionCookieFor(w http.ResponseWriter, r *http.Request, u User, ttl time.Duration) {
	if ttl <= 0 {
		ttl = s.sessionTTL()
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.signUserFor(u, ttl), Path: "/",
		HttpOnly: true, Secure: requestIsHTTPS(r, s.trustedNets),
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func encodeSessionMessage(msg string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(msg))
}

func (s *Server) signUserFor(u User, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = s.sessionTTL()
	}
	exp := time.Now().Add(ttl).Unix()
	msg := fmt.Sprintf("v1|%s|%d|%d", u.Username, u.SessionRev, exp)
	sig := s.hmac(msg)
	return encodeSessionMessage(msg) + "." + sig
}

func (s *Server) hmac(msg string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) verify(cookie string) (string, int64) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return "", 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0
	}
	msg := string(raw)
	if !hmac.Equal([]byte(s.hmac(msg)), []byte(parts[1])) {
		return "", 0
	}
	expSep := strings.LastIndex(msg, "|")
	if expSep < 0 {
		return "", 0
	}
	exp, err := strconv.ParseInt(msg[expSep+1:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", 0
	}
	// Cookies issued before session revisions used "username|expiry". Keep them valid at
	// revision zero so this additive change does not force a logout or rewrite stored state.
	// A later password change increments session_rev and invalidates them normally.
	if !strings.HasPrefix(msg, "v1|") {
		return msg[:expSep], 0
	}
	revSep := strings.LastIndex(msg[:expSep], "|")
	if revSep < len("v1|") {
		return "", 0
	}
	rev, err := strconv.ParseInt(msg[revSep+1:expSep], 10, 64)
	if err != nil || rev < 0 {
		return "", 0
	}
	return msg[len("v1|"):revSep], rev
}

// ownerTokenPrefix tags the report-attribution token so it can never be confused with a session
// cookie ("v1|") signed by the same secret.
const ownerTokenPrefix = "ot1"

// mintOwnerToken produces a signed, opaque attribution token naming WHO asked for a run: the person
// and, when they have one, their OU. It is injected into a scoped user's Dify run inputs and echoed
// back at ingest, so a report's attribution is decided by the portal's secret_key and never by a
// client-supplied field (ADR 0022 R1).
//
// It carries the person, not only the OU, because under per-person visibility (ADR 0024) the OU
// alone cannot identify a reader — and without the person, the requester could not read the very
// report their own run produced.
func (s *Server) mintOwnerToken(ou int64, user string) string {
	if ou == 0 && user == "" {
		return ""
	}
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	msg := fmt.Sprintf("%s|%d|%s|%d", ownerTokenPrefix, ou, user, exp)
	return encodeSessionMessage(msg) + "." + s.hmac(msg)
}

// ownerFromToken verifies an attribution token and returns the OU and person it carries. ok=false
// for an empty, malformed, tampered, expired, or foreign (wrong-prefix) token — the caller then
// leaves the report unattributed, which fails closed for scoped viewers.
func (s *Server) ownerFromToken(tok string) (ou int64, user string, ok bool) {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, "", false
	}
	msg := string(raw)
	if !hmac.Equal([]byte(s.hmac(msg)), []byte(parts[1])) {
		return 0, "", false
	}
	f := strings.Split(msg, "|")
	if len(f) != 4 || f[0] != ownerTokenPrefix {
		return 0, "", false
	}
	exp, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, "", false
	}
	ou, err = strconv.ParseInt(f[1], 10, 64)
	if err != nil || ou < 0 {
		return 0, "", false
	}
	user = f[2]
	if ou == 0 && user == "" {
		return 0, "", false
	}
	return ou, user, true
}

// currentActiveUser returns the logged-in user only if the account still exists and
// is enabled, so disabling an account takes effect immediately — even for a session
// whose cookie is still valid.
func (s *Server) currentActiveUser(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	user, rev := s.verify(c.Value)
	if user == "" {
		return ""
	}
	usr := s.st.GetUser(user)
	if usr == nil || !usr.Active || s.accountExpired(usr) || usr.SessionRev != rev {
		return ""
	}
	return user
}

// accountExpired reports whether a user's validity cutoff has passed. The cutoff is a panel-tz
// civil date and the account stays valid THROUGH that whole day, so it is expired only once the
// panel-tz civil date is strictly after it (ISO dates compare lexicographically = chronologically).
// "" = never expires. Enforced at login (apiLogin) and on every request here (ADR 0022 R4).
func (s *Server) accountExpired(u *User) bool {
	if u == nil || u.ExpiresAt == "" {
		return false
	}
	today := time.Now().In(s.panelLocation()).Format("2006-01-02")
	return today > u.ExpiresAt
}

func (s *Server) isAdmin(user string) bool {
	u := s.st.GetUser(user)
	return u != nil && can(u.Role, PermManage)
}

// hasPerm reports whether the logged-in user's role holds a permission.
func (s *Server) hasPerm(user, perm string) bool {
	u := s.st.GetUser(user)
	return u != nil && can(u.EffRole(), perm)
}

type handler func(http.ResponseWriter, *http.Request, string)

func (s *Server) requireUser(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentActiveUser(r)
		if u == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// The report pages are inside the same wall as the API: a mandated account is sent to the
		// account page rather than shown a report it should not be reading yet.
		if !s.gateEnrolmentPage(w, r, u) {
			return
		}
		h(w, r, u)
	}
}

// ---------- Login ----------

// ---------- List ----------

func (s *Server) filtersFrom(r *http.Request) (Filters, string, int, int) {
	q := r.URL.Query()
	f := Filters{
		Q: strings.TrimSpace(q.Get("q")), Scope: q.Get("scope"), Symbol: q.Get("symbol"),
		RType: q.Get("rtype"), Kind: q.Get("kind"), Version: strings.TrimSpace(q.Get("version")),
		DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"),
		Sort: q.Get("sort"),
	}
	src := q.Get("src")
	if src == "" {
		src = "all"
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size != 15 && size != 30 && size != 50 {
		size = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	return f, src, size, page
}

// ---------- run detail ----------

func (s *Server) runMembers(r *http.Request, user, key string) []Rep {
	var members []Rep
	if !strings.Contains(key, "|") {
		// A "|"-less key is a thematic report's group key, which gkey() renders as its bare id.
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return nil
		}
		if rep := s.loadRep(r, user, id); rep != nil {
			members = []Rep{*rep}
		}
	} else {
		parts := strings.SplitN(key, "|", 2)
		symbol, date := parts[0], parts[1]
		nn, _ := s.st.SearchNew(Filters{DateFrom: date, DateTo: date, Sort: "date_asc"}, s.viewerScope(user))
		for _, m := range nn {
			if m.Symbol == symbol {
				members = append(members, m)
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].Time < members[j].Time })
	}
	for i := range members {
		members[i].Label = tabLabel(members[i])
	}
	return members
}

// orderAndDefault sorts members and picks the default page (the type marked "汇总") according to the type config (which admins can edit).
// Fallback when unconfigured: detect summary by keyword → otherwise the last item. Tab labels can be renamed via config.
// defaultTypeOrd: the built-in default tab order for unconfigured types (conclusion first, the rest following the analysis flow).
// Dragging in "Type Management" writes to type_config.ord, which takes precedence over this.
var defaultTypeOrd = map[string]int{
	"投资决策建议": 0, "综合深度研究": 0,
	"事件监测": 10, "投资机会": 10, "研报分析": 10,
	"舆情分析": 20, "重组基本面分析": 20,
	"行业分析": 30, "重组分析": 30,
	"财务分析": 40, "资本运作分析": 40,
	"估值分析":   50,
	"股权分析":   60,
	"管理能力分析": 70,
	"调研纪要":   80,
}

// defaultSeedTypes is the set of report types pre-registered on a fresh DB so the
// Type Management page ships with our real categories instead of being empty.
// Admins can rename/reassign category/reorder/add in the UI afterward.
//
// These are the actual categories our Dify workflow modules emit (dept-1 single-
// stock analysis → 投资决策/深度研究; the 3-x 重组 series → 重组决策; DeepResearch →
// 深度研究), cross-checked against the categories present in ingested data.
var defaultSeedTypes = []struct {
	Name    string
	Kind    string
	Ord     int
	Summary bool
}{
	// 投资决策 (dept-1 single-stock analysis; 投资决策建议 is the decision summary).
	// 舆情分析 (dept-1 1-2) and 管理能力分析 (dept-1 1-5) are investment inputs, not 重组/深度研究.
	{"汇总", "投资决策", 0, true},
	{"投资决策建议", "投资决策", 10, true},
	{"研报分析", "投资决策", 20, false},
	{"行业分析", "投资决策", 30, false},
	{"舆情分析", "投资决策", 35, false},
	{"估值分析", "投资决策", 40, false},
	{"财务分析", "投资决策", 50, false},
	{"股权分析", "投资决策", 60, false},
	{"管理能力分析", "投资决策", 65, false},
	{"投资机会", "投资决策", 70, false},
	// 深度研究 (DeepResearch-DS emits 综合深度研究 / 重组深度研究 for single-stock queries,
	// and 专题研究 for non-single-stock queries — macro/industry/strategy/M&A/multi-company
	// comparison, identified by title instead of a stock symbol; 调研纪要 is manual)
	{"综合深度研究", "深度研究", 0, true},
	{"重组深度研究", "深度研究", 10, false},
	{"专题研究", "深度研究", 15, false},
	{"调研纪要", "深度研究", 20, false},
	// 重组决策 (the 3-x 重组 series; 综合决策 is the 3-5 summary; the rest are its sub-models)
	{"综合决策", "重组决策", 0, true},
	{"重组基本面分析", "重组决策", 10, false},
	{"交易分析", "重组决策", 20, false},
	{"重组舆情分析", "重组决策", 30, false},
	{"资本运作分析", "重组决策", 40, false},
	{"事件监测", "重组决策", 50, false},
	{"信号监测", "重组决策", 60, false},
	// 技术分析 (Daily_Quote 缠论 / 技术分析)
	{"技术分析", "技术分析", 0, false},
	{"缠论分析", "技术分析", 10, false},
	// 每日金股 (daily event-driven picks — one card per market segment: 盘前/盘中/盘后)
	{"盘前", "每日金股", 0, true},
	{"盘中", "每日金股", 10, false},
	{"盘后", "每日金股", 20, false},
	// 未分类 (uncategorized / thematic — its own bucket)
	{"未分类", "未分类", 0, false},
}

// seedDefaultTypes registers the default report types (first run only) and returns the count.
func seedDefaultTypes(st *Store) int {
	for _, t := range defaultSeedTypes {
		st.UpsertTypeConfig(t.Name, t.Kind, "", t.Ord, t.Summary)
	}
	return len(defaultSeedTypes)
}

// defaultKindColors is the shipped kind→antd-Tag-color mapping (first run only),
// matching the pipeline kinds in kindOrder. Admins can change any of these
// afterward on the Types Management page.
var defaultKindColors = []struct {
	Kind  string
	Color string
}{
	{"重组决策", "volcano"},
	{"投资决策", "green"},
	{"深度研究", "geekblue"},
	{"技术分析", "purple"},
	{"每日金股", "cyan"},
	{"事件监测", "gold"},
}

// seedDefaultKindColors registers the default kind colors (first run only) and returns the count.
func seedDefaultKindColors(st *Store) int {
	for _, c := range defaultKindColors {
		st.SetKindColor(c.Kind, c.Color)
	}
	return len(defaultKindColors)
}

func (s *Server) orderAndDefault(members []Rep) ([]Rep, int64) {
	cfg := s.st.TypeConfigs()
	ord := func(r Rep) int {
		if c, ok := cfg[r.RType]; ok {
			return c.Ord
		}
		if o, ok := defaultTypeOrd[r.RType]; ok {
			return o
		}
		return 1000
	}
	sum := func(r Rep) bool {
		if c, ok := cfg[r.RType]; ok && c.IsSummary {
			return true
		}
		return isSummary(r)
	}
	out := make([]Rep, len(members))
	copy(out, members)
	for i := range out {
		if c, ok := cfg[out[i].RType]; ok && c.Label != "" {
			out[i].Label = typeTabLabel(c.Label, out[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := sum(out[i]), sum(out[j]); si != sj {
			return si // summary / comprehensive / decision come first
		}
		if oi, oj := ord(out[i]), ord(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Time < out[j].Time
	})
	// Two tabs can share a label for two different reasons, and they need different answers.
	//
	// Several DIFFERENT reports of one type — title is part of a report's identity, so one
	// code+date+subtype legitimately carries more than one — are told apart by a number, as they
	// always have been.
	//
	// Two WRITTEN FORMS of one analysis (ADR 0024) are told apart by naming the form. A number says
	// nothing about which one a reader is looking at, and until v0.4.42 that barely mattered because
	// no workflow sent a version: the case was rare. A single hand-written correction (ADR 0026) now
	// produces it every time, so a corrected report showed up as "深度分析 2" with no way to tell it
	// from the workflow's own.
	//
	// The default version keeps the bare label, so a portal that has never used versions reads
	// exactly as before.
	versioned := map[string]map[string]bool{} // label -> the set of versions carrying it
	for i := range out {
		v := out[i].Version
		if v == "" {
			v = defaultVersionName
		}
		if versioned[out[i].Label] == nil {
			versioned[out[i].Label] = map[string]bool{}
		}
		versioned[out[i].Label][v] = true
	}
	verLabel := map[string]string{}
	for _, v := range s.st.Versions() {
		verLabel[v.Name] = firstNonEmpty(v.Label, v.Name)
	}
	// The number counts DISTINCT REPORTS sharing a label, not rows, so every written form of one
	// report carries the same number: "重组交易分析 2" and "重组交易分析 2 · 人工" are the two forms of the
	// same thing, and the number is what says which thing.
	//
	// Reports are told apart by title here, because that is what makes them different reports. A
	// hand-written form that has since been retitled therefore gets a number of its own — the portal
	// genuinely cannot know which report it was written from once the title stops matching, and
	// guessing would put a reader on the wrong document. The form suffix still names it.
	num := map[string]int{}
	next := map[string]int{}
	for i := range out {
		v := out[i].Version
		if v == "" {
			v = defaultVersionName
		}
		base := out[i].Label
		key := base + "\x00" + out[i].Title
		if _, ok := num[key]; !ok {
			next[base]++
			num[key] = next[base]
		}
		if n := num[key]; n > 1 {
			out[i].Label = out[i].Label + " " + strconv.Itoa(n)
		}
		if len(versioned[base]) > 1 && v != defaultVersionName {
			out[i].Label = out[i].Label + " · " + firstNonEmpty(verLabel[v], v)
		}
	}
	var def int64
	bestOrd := 1 << 30
	for _, m := range out {
		if c, ok := cfg[m.RType]; ok && c.IsSummary && c.Ord < bestOrd {
			bestOrd, def = c.Ord, m.ID
		}
	}
	if def == 0 {
		for _, m := range out {
			if isSummary(m) {
				def = m.ID
				break
			}
		}
	}
	if def == 0 && len(out) > 0 {
		def = out[len(out)-1].ID
	}
	return out, def
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func containsStr(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

// repKind returns a report's top-level category: new reports use the Kind field, otherwise it is inferred from the type.
func repKind(r Rep) string {
	if r.Kind != "" {
		return foldKind(r.Kind)
	}
	return runKind([]string{r.RType})
}

// tokenOK validates the Bearer token in the request; need = the required scope (ingest|query), and a token with scope=all passes everything.
// Besides persistent api_tokens it also accepts an ephemeral, scoped app-bridge
// token (ADR 0003) — these are query-only, so ingest paths still fall through to
// the DB check and reject them.
func (s *Server) tokenOK(r *http.Request, need string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if s.appTok != nil && s.appTok.valid(got, need, time.Now()) {
		return true
	}
	return s.st.TokenValid(got, need)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// writeJSONStatus is writeJSON at a chosen status. For a refusal that has to carry more than a
// reason — jsonErrorCode's {error, code} shape covers almost all of them, but a conflict whose
// answer is "here is the row you collided with" needs the row's id too, and an id squeezed into a
// message is not something a caller can act on.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeJSONIfChanged answers a polled endpoint with 304 when the body is byte-identical to what
// this caller already has.
//
// It is for the console's pollers, which ask every few seconds for something that changes far less
// often than they ask. A queue with nothing happening in it is the normal case, and answering that
// case with an empty 304 costs the network nothing and — because the client keeps the object it
// already had — costs React nothing either: no parse, no setState, no re-render.
//
// The tag is a hash of the body we were going to send. That does not save the server the work of
// producing it, which would need a cheap version stamp the queue does not have; what it saves is
// everything after that, which is where the cost was. Correct by construction: if the bytes are the
// same the answer is the same.
//
// Note that /api/ is Cache-Control: no-store, so no cache along the way keeps this — the CLIENT
// holds the tag and sends it back deliberately (see lib/conditionalGet.ts). That is the point: the
// revalidation is between this handler and that poller, not something a proxy may join in on.
func writeJSONIfChanged(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeJSON(w, v)
		return
	}
	sum := sha256.Sum256(body)
	tag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if match := r.Header.Get("If-None-Match"); match != "" && match == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

// apiSymbols lists stocks that have reports / autocomplete. GET /api/symbols?q=300&limit=50
func (s *Server) apiSymbols(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Gated only when a session is what the caller used: this route also serves the machine surface,
	// and a Dify query token is not a reader to be held at an enrolment wall.
	if u := s.currentActiveUser(r); u != "" && !s.gateEnrolment(w, r, u) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	// A browser session is owner-scoped to the viewer; a machine Bearer(query) caller has no session
	// so currentActiveUser is "" → viewerScope nil → unscoped (internal machine surface).
	list := s.st.ListSymbols(strings.TrimSpace(q.Get("q")), limit, s.viewerScope(s.currentActiveUser(r)))
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		name := si.Name // name from the DB (stocks); fall back to the in-memory map if absent
		if name == "" {
			name = s.names.Get(si.Symbol)
		}
		out = append(out, map[string]any{"symbol": si.Symbol, "name": name, "count": si.Count, "latest": si.Latest})
	}
	writeJSON(w, map[string]any{"count": len(out), "symbols": out})
}

func repInList(reps []Rep, id int64) bool {
	for _, r := range reps {
		if r.ID == id {
			return true
		}
	}
	return false
}

// loadRep fetches one report for a person by id, SCOPED to what they may read — an out-of-scope id
// returns nil and the callers' nil→404 then fails closed — and records the read.
//
// Every path that serves ONE body to a human goes through here: the stock page, the run page, the
// day export, the Markdown and PDF exports, and the version switcher. Recording at this one point
// rather than in each of them is what makes the answer to "who read this report" complete, and
// keeps a path added later from silently escaping the log.
//
// The comparison view is the one path that does not, because it reads a PAIR and needs both bodies
// before it can answer at all; it scopes them itself and records through recordReportRead, which is
// the same writer. Anything that serves a body must call one of the two.
func (s *Server) loadRep(r *http.Request, user string, id int64) *Rep {
	if id <= 0 {
		return nil
	}
	rep, _ := s.st.GetNew(id, s.viewerScope(user))
	if rep != nil {
		s.recordReportRead(r, user, rep)
	}
	return rep
}

// recordReportRead logs that `user` was served this report's body.
//
// Only successful reads. A refusal is a different question — someone probing versus someone
// following a stale link — and logging those would fill the table from any 404.
// The request is carried this far for one field: a read is the action most likely to be the
// subject of "who saw this, and from where", and it was the only one recorded without an address.
func (s *Server) recordReportRead(r *http.Request, user string, rep *Rep) {
	if rep == nil {
		return
	}
	s.st.WriteAudit(AuditEntry{
		Actor: user, ActorOU: s.st.PrimaryGroupOf(user), Action: AuditReportRead,
		TargetType: "report", TargetID: strconv.FormatInt(rep.ID, 10),
		Detail: auditJSON(map[string]any{"symbol": rep.Symbol, "date": rep.Date, "title": rep.Title}),
		IP:     s.auditIP(r),
	})
}

// ---------- Export ----------

func (s *Server) reportMD(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r, user, pathID(r, "id"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	fn := safeFile(s.repDisplayTitle(rep), strconv.FormatInt(rep.ID, 10)) + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write([]byte(rep.MD))
}

// renderPDFHTML executes the PDF template for rep, deriving HTML from MD (htmlOf) when
// the HTML column wasn't persisted — md-only reports don't store a redundant copy.
func (s *Server) renderPDFHTML(rep *Rep, user string) (string, error) {
	data := *rep
	data.Title = s.repDisplayTitle(rep) // fold the company name into the <h1>
	if data.MD != "" {
		data.HTML = s.renderPDFMarkdown(user, data.MD)
	} else {
		data.HTML = sanitizePDFBody(htmlOf(data))
	}
	var buf strings.Builder
	if err := s.pdf.ExecuteTemplate(&buf, "pdf.html", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) reportPDF(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r, user, pathID(r, "id"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	renderedHTML, err := s.renderPDFHTML(rep, user)
	if err != nil {
		http.Error(w, "render", 500)
		return
	}
	pdf, err := htmlToPDFContext(r.Context(), renderedHTML)
	if err == ErrNoWkhtmltopdf {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(503)
		fmt.Fprint(w, `<div style="font-family:sans-serif;max-width:520px;margin:12vh auto;text-align:center;color:#334">`+
			`<h2 style="color:#0c447c">PDF 暂不可用</h2>`+
			`<p>本机未安装 <code>wkhtmltopdf</code>，无法在本地生成 PDF。</p>`+
			`<p><b>Docker 部署已内置</b>。Homebrew 已不再提供 wkhtmltopdf，本机可改用 Docker 部署。</p>`+
			`<p>也可先用 <b>⬇ MD</b> 导出。</p>`+
			`<p><a href="#" onclick="window.close();return false;">关闭此页</a> · <a href="/">返回首页</a></p></div>`)
		return
	}
	if err != nil {
		http.Error(w, "PDF 生成失败: "+err.Error(), 500)
		return
	}
	fn := safeFile(s.repDisplayTitle(rep), strconv.FormatInt(rep.ID, 10)) + ".pdf"
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write(pdf)
}

func safeFile(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return title
}

// ---------- Entry-button management ----------

// ---------- Report-type management ----------

var kindOrder = []string{"重组决策", "投资决策", "深度研究", "技术分析", "每日金股", "未分类"}

// ---------- Account management ----------

// ---------- System settings ----------
// Old-portal credentials are stored in the DB and set via System Settings. Nothing
// reads them anymore: the live read-through and the one-shot importer that used them
// are both gone, and the old portal's reports already live in the reports table. The
// settings survive only so an admin can still see what was configured.

func uniqSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
