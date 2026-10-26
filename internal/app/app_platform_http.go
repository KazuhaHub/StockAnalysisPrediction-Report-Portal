package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformDocumentTimeout = 5 * time.Second
const platformDocumentErrorCSP = "sandbox; default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type platformDocumentBudget struct {
	once  sync.Once
	slots chan struct{}
}

func (b *platformDocumentBudget) acquire() (func(), bool) {
	b.once.Do(func() { b.slots = make(chan struct{}, 2) })
	select {
	case b.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-b.slots }) }, true
	default:
		return nil, false
	}
}
func platformDocumentHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", platformDocumentErrorCSP)
		next(w, r)
	}
}
func (s *Server) platformHTTPReader(r *http.Request, user string) (platformActorSession, error) {
	var zero platformActorSession
	cookie, err := r.Cookie(cookieName)
	if err != nil || !s.hasSigningKey() {
		return zero, errAppPlatformLaunchUnavailable
	}
	current, revision := s.verify(cookie.Value)
	if current == "" || current != user {
		return zero, errAppPlatformLaunchUnavailable
	}
	account := s.st.GetUser(user)
	if account == nil || !account.Active || account.SessionRev != revision {
		return zero, errAppPlatformLaunchUnavailable
	}
	id := sessionID(r)
	// Preserve the signed cookie lifetime, not a later mutable session-row expiry.
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(cookie.Value, ".", 2)[0])
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	end := strings.LastIndexByte(string(raw), '|')
	if end < 0 {
		return zero, errAppPlatformLaunchUnavailable
	}
	signedExpiry, err := strconv.ParseInt(string(raw[end+1:]), 10, 64)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	var expires int64
	// Persisted managed sessions, including already migrated legacy identities.
	// This read never creates or upgrades a browser credential.
	if err := s.st.queryRow(`SELECT expires_at FROM user_sessions WHERE id=? AND username=? AND revision=? AND expires_at=? AND revoked=0`, id, user, revision, signedExpiry).Scan(&expires); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	generation, err := notificationAccountGeneration(s.st.poolExec(), user)
	if err != nil {
		return zero, err
	}
	return platformActorSession{username: user, created: account.CreatedAt, sessionID: id, sessionRevision: revision, generation: generation, expires: time.Unix(expires, 0)}, nil
}
func platformDocumentSource(binding platformLaunchAccess) string {
	return "/app-documents/" + binding.installationID + "/" + binding.generation + "/" + binding.policyRevision + "/" + binding.releaseSHA + "/" + binding.frontendSHA + "/" + binding.entry
}
func platformHTTPPath(r *http.Request) bool {
	return len(r.URL.Path) <= 1024 && r.URL.RawQuery == "" && !r.URL.ForceQuery && r.URL.EscapedPath() == r.URL.Path
}
func platformPositiveCounter(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}
func platformDocumentFailure(w http.ResponseWriter) {
	http.Error(w, "application unavailable", http.StatusNotFound)
}
func (s *Server) platformHTTPAdmission(w http.ResponseWriter, r *http.Request, user string) (platformActorSession, func(), bool) {
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return platformActorSession{}, nil, false
	}
	release, ok := s.platformDocuments.acquire()
	if !ok {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "application busy", http.StatusTooManyRequests)
		return platformActorSession{}, nil, false
	}
	return session, release, true
}
func (s *Server) apiPlatformLaunch(w http.ResponseWriter, r *http.Request, user string) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		platformDocumentFailure(w)
		return
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	// Validate the complete current document profile before advertising its URL.
	var applicationID, layout, timeZone string
	var display *appplatform.AppDisplay
	binding, err := s.withPlatformLaunchReadContext(ctx, session, r.PathValue("installation"), func(e migExec, current platformLaunchAccess, m appplatform.Manifest) error {
		var archive []byte
		if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`, current.frontendSHA).Scan(&archive); err != nil {
			return err
		}
		applicationID = m.ID
		display = m.Presentation.Display
		layout = m.Presentation.Routes[0].Layout
		var zone sql.NullString
		err := e.queryRow(`SELECT v FROM meta WHERE k='timezone'`).Scan(&zone)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		timeZone = zone.String
		if timeZone == "" {
			timeZone = time.Local.String()
		}
		// Local is an opaque process location, not a browser-recognized IANA name.
		// An empty result stays explicitly unavailable to the host presentation layer.
		if timeZone == "Local" {
			timeZone = ""
		}
		_, err = appplatform.BuildFrontendDocument(m, archive)
		return err
	})
	if err != nil || ctx.Err() != nil {
		platformDocumentFailure(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"source": platformDocumentSource(binding), "binding": map[string]string{"installationId": binding.installationID, "generation": binding.generation, "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}, "capabilities": []string{}, "applicationId": applicationID, "display": display, "layout": layout, "timeZone": timeZone})
}
func (s *Server) platformAppDocument(w http.ResponseWriter, r *http.Request, user string) {
	expected := platformLaunchAccess{installationID: r.PathValue("installation"), generation: r.PathValue("generation"), policyRevision: r.PathValue("policy"), releaseSHA: r.PathValue("release"), frontendSHA: r.PathValue("frontend"), entry: r.PathValue("entry")}
	if !platformHTTPPath(r) || !platformValidUUID(expected.installationID) || !platformPositiveCounter(expected.generation) || !platformPositiveCounter(expected.policyRevision) || !platformValidSHA(expected.releaseSHA) || !platformValidSHA(expected.frontendSHA) || r.URL.Path != platformDocumentSource(expected) || strings.Contains(expected.entry, "..") {
		platformDocumentFailure(w)
		return
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	document, err := s.readPlatformLaunchDocumentContext(ctx, session, expected)
	if err != nil || ctx.Err() != nil {
		platformDocumentFailure(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", document.CSP)
	w.Header().Set("Content-Length", strconv.Itoa(len(document.Content)))
	if r.Method != http.MethodHead {
		w.Write(document.Content)
	}
}

// Bind every statement, including lifecycle/trust/resource lock acquisition, to
// the request context. The DB transaction alone does not make Tx.Query cancelable.
type platformContextTx struct {
	*sql.Tx
	ctx context.Context
}

func (t platformContextTx) Exec(q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(t.ctx, q, args...)
}
func (t platformContextTx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(t.ctx, q, args...)
}
func (t platformContextTx) QueryRow(q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(t.ctx, q, args...)
}

type platformContextPool struct {
	*sql.DB
	ctx context.Context
}

func (p platformContextPool) Exec(q string, args ...any) (sql.Result, error) {
	return p.DB.ExecContext(p.ctx, q, args...)
}
func (p platformContextPool) Query(q string, args ...any) (*sql.Rows, error) {
	return p.DB.QueryContext(p.ctx, q, args...)
}
func (p platformContextPool) QueryRow(q string, args ...any) *sql.Row {
	return p.DB.QueryRowContext(p.ctx, q, args...)
}
