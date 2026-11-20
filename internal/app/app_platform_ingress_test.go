package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The authenticated inbound ingress contract (doc 180 section 7).

type platformIngressForward struct {
	light    platformIngressLight
	body     []byte
	nonce    string
	received time.Time
}

type platformIngressEnv struct {
	s      *Server
	mux    *http.ServeMux
	actor  platformActorSession
	cookie string
	id     string

	mu       sync.Mutex
	forwards []platformIngressForward
	response driver.BackendResponse
	secrets  map[string][]byte
	seq      int
}

func testIngressBlock() *appplatform.Ingress {
	return &appplatform.Ingress{Routes: []appplatform.IngressRoute{
		{ID: "heartbeat", Path: "/v1/ingress/heartbeat", BodyBytes: 1024, PerEndpointPerMinute: 10},
		{ID: "webhook", Path: "/v1/ingress/hook", BodyBytes: 4096, PerEndpointPerMinute: 60},
	}}
}

// platformIngressFixture is a ready backend installation that declares the ingress capability and
// whose backend answers through a recording hook (the runner path is covered separately).
func platformIngressFixture(t *testing.T, pg bool) *platformIngressEnv {
	t.Helper()
	platformManagedFixtureOptional = []string{"ingress.webhook"}
	platformManagedFixtureIngress = testIngressBlock()
	t.Cleanup(func() { platformManagedFixtureOptional, platformManagedFixtureIngress = nil, nil })
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	t.Cleanup(func() { mustExec(t, s.st, `DELETE FROM audit_log WHERE action LIKE 'app_platform.ingress_%'`) })
	e := &platformIngressEnv{s: s, actor: actor, id: lease.installationID, mux: http.NewServeMux(), secrets: map[string][]byte{}, response: driver.BackendResponse{Status: 200, Body: []byte(`{"ok":true}`)}}
	e.cookie = s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	s.wireRoutes(e.mux)
	s.platformIngressForwardHook = func(ctx context.Context, l platformIngressLight, body []byte, nonce string, received time.Time) (driver.BackendResponse, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.forwards = append(e.forwards, platformIngressForward{l, append([]byte(nil), body...), nonce, received})
		return e.response, nil
	}
	return e
}

func (e *platformIngressEnv) forwarded() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.forwards)
}

func (e *platformIngressEnv) admin(t *testing.T, method, suffix, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/admin/app-platform/installations/"+e.id+"/"+suffix, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: e.cookie})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *platformIngressEnv) preview(t *testing.T) platformIngressPreview {
	t.Helper()
	w := e.admin(t, "GET", "ingress", "")
	var out platformIngressPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("preview", w.Code, w.Body.String())
	}
	return out
}

func (e *platformIngressEnv) enable(t *testing.T) {
	t.Helper()
	p := e.preview(t)
	body, _ := json.Marshal(map[string]any{"enabled": true, "policyRevision": p.PolicyRevision, "disclosureSha256": p.DisclosureSHA256})
	if w := e.admin(t, "POST", "ingress", string(body)); w.Code != 200 {
		t.Fatal("enable", w.Code, w.Body.String())
	}
}

func (e *platformIngressEnv) disable(t *testing.T) {
	t.Helper()
	p := e.preview(t)
	if w := e.admin(t, "POST", "ingress", `{"enabled":false,"policyRevision":"`+p.PolicyRevision+`","disclosureSha256":null}`); w.Code != 200 {
		t.Fatal("disable", w.Code, w.Body.String())
	}
}

func ingressSecret(seed string) []byte {
	return []byte(strings.Repeat(seed, 40)[:40])
}

// bind creates a credential holding secret, reviews and commits an endpoint binding named name.
func (e *platformIngressEnv) bind(t *testing.T, name, route string, secret []byte) platformSecretBindingMetadata {
	t.Helper()
	e.seq++
	credential, err := e.s.changePlatformCredential(e.actor, platformCredentialChange{kind: "create", label: fmt.Sprintf("Heartbeat %d", e.seq), value: secret})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.s.createPlatformSecretBindingReviewFull(e.actor, e.id, name, credential.ID, "ingress_hmac_sha256", "", nil, route)
	if err != nil {
		t.Fatal("review:", err)
	}
	if bytes.Contains([]byte(plan.JSON), secret) {
		t.Fatal("the binding review contains the secret")
	}
	binding, err := e.s.consumePlatformSecretBindingReview(e.actor, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal("commit:", err)
	}
	e.secrets[name] = secret
	return binding
}

type ingressSend struct {
	installation, route, endpoint string
	body                          string
	signedBody                    *string
	secret                        []byte
	timestamp                     int64
	nonce                         string
	signInstallation              string
	signRoute, signEndpoint       string
	contentType                   string
	remote                        string
	unsigned                      bool
	method                        string
}

func (e *platformIngressEnv) send(t *testing.T, mods ...func(*ingressSend)) *httptest.ResponseRecorder {
	t.Helper()
	e.seq++
	m := &ingressSend{installation: e.id, route: "heartbeat", endpoint: "api", body: `{"ok":1}`, contentType: "application/json", method: "POST",
		nonce: fmt.Sprintf("nonce-%012d", e.seq), remote: fmt.Sprintf("10.%d.%d.1:1", e.seq/250, e.seq%250)}
	for _, mod := range mods {
		mod(m)
	}
	if m.secret == nil {
		m.secret = e.secrets[m.endpoint]
	}
	if m.timestamp == 0 {
		m.timestamp = time.Now().Unix()
	}
	signed := []byte(m.body)
	if m.signedBody != nil {
		signed = []byte(*m.signedBody)
	}
	signInstallation, signRoute, signEndpoint := m.installation, m.route, m.endpoint
	if m.signInstallation != "" {
		signInstallation = m.signInstallation
	}
	if m.signRoute != "" {
		signRoute = m.signRoute
	}
	if m.signEndpoint != "" {
		signEndpoint = m.signEndpoint
	}
	r := httptest.NewRequest(m.method, "/ingress/apps/"+m.installation+"/"+m.route+"/"+m.endpoint, strings.NewReader(m.body))
	r.RemoteAddr = m.remote
	if m.contentType != "" {
		r.Header.Set("Content-Type", m.contentType)
	}
	if !m.unsigned {
		ts := strconv.FormatInt(m.timestamp, 10)
		r.Header.Set(appplatform.IngressTimestampHeader, ts)
		r.Header.Set(appplatform.IngressNonceHeader, m.nonce)
		r.Header.Set(appplatform.IngressSignatureHeader, appplatform.IngressSign(m.secret, signInstallation, signRoute, signEndpoint, ts, m.nonce, signed))
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

const ingressDenied = `{"ok":false}`

func withBody(b string) func(*ingressSend)      { return func(m *ingressSend) { m.body = b } }
func withNonce(n string) func(*ingressSend)     { return func(m *ingressSend) { m.nonce = n } }
func withEndpoint(n string) func(*ingressSend)  { return func(m *ingressSend) { m.endpoint = n } }
func withRemote(r string) func(*ingressSend)    { return func(m *ingressSend) { m.remote = r } }
func withTimestamp(ts int64) func(*ingressSend) { return func(m *ingressSend) { m.timestamp = ts } }

// Doc 180 test 1: a POST to /ingress/apps/... matched no route before; it is 401 unsigned and 202
// signed, and it is 401 until the capability is granted and an endpoint exists.
func TestIngressAnswersUnsignedAndSignedRequests(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		secret := ingressSecret("a")
		unsigned := e.send(t, func(m *ingressSend) { m.unsigned = true })
		if unsigned.Code != 401 || unsigned.Body.String() != ingressDenied {
			t.Fatal("unsigned before the grant", unsigned.Code, unsigned.Body.String())
		}
		// A perfectly signed request is the same 401 while the capability is off and while no endpoint exists.
		if w := e.send(t, func(m *ingressSend) { m.secret = secret }); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("signed before the grant", w.Code, w.Body.String())
		}
		e.enable(t)
		if w := e.send(t, func(m *ingressSend) { m.secret = secret }); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("signed with no endpoint", w.Code)
		}
		e.bind(t, "api", "heartbeat", secret)
		if w := e.send(t, func(m *ingressSend) { m.unsigned = true }); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("unsigned after the grant", w.Code)
		}
		if e.forwarded() != 0 {
			t.Fatal("the backend was reached before a valid request")
		}
		w := e.send(t, withBody(`{"timestamp":1,"nonce":"x"}`))
		if w.Code != 202 || w.Body.String() != `{"ok":true}` || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("signed request", w.Code, w.Body.String())
		}
		if e.forwarded() != 1 {
			t.Fatal("forwarded", e.forwarded())
		}
		f := e.forwards[0]
		if string(f.body) != `{"timestamp":1,"nonce":"x"}` || f.light.endpoint != "api" || f.light.route != "heartbeat" || f.light.decl.Path != "/v1/ingress/heartbeat" || f.light.installation != e.id {
			t.Fatalf("forwarded %+v", f)
		}
		// A read of the route is not a heartbeat.
		r := httptest.NewRequest("GET", "/ingress/apps/"+e.id+"/heartbeat/api", nil)
		gw := httptest.NewRecorder()
		e.mux.ServeHTTP(gw, r)
		if gw.Code == 202 || e.forwarded() != 1 {
			t.Fatal("a GET reached the backend", gw.Code)
		}
	})
}

// Doc 180 test 2: every refusal is the same 401, costs no backend call and no database write.
func TestIngressRefusalsAreUniformAndWriteNothing(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		e.bind(t, "other", "webhook", ingressSecret("b"))
		other := "00000000-0000-4000-8000-000000000000"
		stale := time.Now().Add(-31 * time.Second).Unix()
		future := time.Now().Add(30 * time.Second).Unix()
		tampered := `{"ok":2}`
		cases := map[string]func(*ingressSend){
			"wrong secret":              func(m *ingressSend) { m.secret = ingressSecret("z") },
			"wrong installation signed": func(m *ingressSend) { m.signInstallation = other },
			"wrong route signed":        func(m *ingressSend) { m.signRoute = "webhook" },
			"wrong endpoint signed":     func(m *ingressSend) { m.signEndpoint = "other" },
			"too old":                   withTimestamp(stale),
			"too new":                   withTimestamp(future),
			"modified body":             func(m *ingressSend) { m.signedBody = &tampered },
			"other endpoints secret":    func(m *ingressSend) { m.secret = e.secrets["other"] },
			"endpoint of another route": withEndpoint("other"),
			"unknown installation":      func(m *ingressSend) { m.installation, m.signInstallation = other, other },
			"not a uuid":                func(m *ingressSend) { m.installation = "not-a-uuid" },
			"unknown endpoint":          withEndpoint("nope"),
			"undeclared route":          func(m *ingressSend) { m.route = "nope" },
			"no content type":           func(m *ingressSend) { m.contentType = "" },
			"text content type":         func(m *ingressSend) { m.contentType = "text/plain" },
			"short nonce":               withNonce("short"),
			"body is not json":          withBody("not json"),
			"empty body":                withBody(""),
			"unsigned":                  func(m *ingressSend) { m.unsigned = true },
		}
		before := stripHeader(dumpOf(t, e.s.st))
		opens := platformCredentialOpenCount.Load()
		var reference *httptest.ResponseRecorder
		for name, mod := range cases {
			w := e.send(t, mod)
			if w.Code != 401 || w.Body.String() != ingressDenied {
				t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
			}
			if reference == nil {
				reference = w
			}
			for _, h := range []string{"Content-Type", "Cache-Control", "Retry-After", "Set-Cookie"} {
				if w.Header().Get(h) != reference.Header().Get(h) {
					t.Fatalf("%s: header %s differs", name, h)
				}
			}
		}
		if e.forwarded() != 0 {
			t.Fatal("a refused request reached the backend")
		}
		if !bytes.Equal(before, stripHeader(dumpOf(t, e.s.st))) {
			t.Fatal("a refused request wrote to the database")
		}
		// A request that fails before the endpoint is known never opens a credential; one that fails the
		// signature does (it has to), and still writes nothing.
		if platformCredentialOpenCount.Load() == opens {
			t.Fatal("signature checks did not open the credential")
		}
	})
}

// Doc 180 test 3: a repeated nonce inside the window is refused; after a restart the backend gets the
// same digest and deduplicates.
func TestIngressNonceReplayAndRestart(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		if w := e.send(t, withNonce("replay-nonce-0001")); w.Code != 202 {
			t.Fatal("first", w.Code)
		}
		if w := e.send(t, withNonce("replay-nonce-0001")); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("replay", w.Code)
		}
		if e.forwarded() != 1 {
			t.Fatal("a replay reached the backend")
		}
		if w := e.send(t, withNonce("replay-nonce-0002")); w.Code != 202 {
			t.Fatal("a new nonce", w.Code)
		}
		// The same nonce on another endpoint is another key.
		e.bind(t, "second", "heartbeat", ingressSecret("c"))
		if w := e.send(t, withNonce("replay-nonce-0001"), withEndpoint("second")); w.Code != 202 {
			t.Fatal("same nonce, other endpoint", w.Code)
		}
		// A restart forgets the cache: the sender's timestamp window still holds, and the backend receives the
		// same nonce digest it has already seen, which is what it deduplicates on.
		e.s.platformIngress.mu.Lock()
		e.s.platformIngress.nonces = nil
		e.s.platformIngress.mu.Unlock()
		if w := e.send(t, withNonce("replay-nonce-0001")); w.Code != 202 {
			t.Fatal("after a restart", w.Code)
		}
		first, again := e.forwards[0], e.forwards[3]
		if first.nonce != again.nonce || appplatform.IngressDigest([]byte(first.nonce)) != appplatform.IngressDigest([]byte(again.nonce)) {
			t.Fatal("the backend did not get the same nonce")
		}
		// An old timestamp is refused whatever the cache holds.
		if w := e.send(t, withNonce("replay-nonce-0009"), withTimestamp(time.Now().Add(-time.Minute).Unix())); w.Code != 401 {
			t.Fatal("stale after restart", w.Code)
		}
	})
}

func TestIngressNonceCacheIsBoundedAndExpires(t *testing.T) {
	var p platformIngressState
	start := time.Unix(1_800_000_000, 0)
	for i := 0; i < platformIngressNonceEntries+500; i++ {
		if !p.claimNonce("i", "api", fmt.Sprint(i), start.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatal("a fresh nonce was refused", i)
		}
	}
	now := start.Add(time.Second)
	if p.nonceCount("i") > platformIngressNonceEntries {
		t.Fatal("the cache grew past its bound:", p.nonceCount("i"))
	}
	if p.claimNonce("i", "api", fmt.Sprint(platformIngressNonceEntries+499), now) {
		t.Fatal("the newest nonce was forgotten")
	}
	if !p.claimNonce("i", "api", "0", now) {
		t.Fatal("the oldest entry was not the one evicted")
	}
	if p.claimNonce("i", "api", "0", now.Add(time.Second)) {
		t.Fatal("a nonce was accepted twice inside the window")
	}
	if !p.claimNonce("i", "api", "0", now.Add(appplatform.IngressNonceWindow+time.Second)) {
		t.Fatal("a nonce outlived the window")
	}
	// Everything claimed above is older than the window by now, and the two claims made after that are all that is left.
	if !p.claimNonce("i", "api", "fresh", now.Add(appplatform.IngressNonceWindow+10*time.Second)) || p.nonceCount("i") != 2 {
		t.Fatal("expired entries were kept:", p.nonceCount("i"))
	}
}

// Doc 180 test 4: the 11th heartbeat in a minute for one endpoint is 429; other endpoints are not affected.
func TestIngressEndpointLimit(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		e.bind(t, "second", "heartbeat", ingressSecret("b"))
		for i := 0; i < 10; i++ {
			if w := e.send(t); w.Code != 202 {
				t.Fatal(i, w.Code)
			}
		}
		w := e.send(t)
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatal("the 11th", w.Code)
		}
		if e.forwarded() != 10 {
			t.Fatal("a limited request reached the backend")
		}
		if w := e.send(t, withEndpoint("second")); w.Code != 202 {
			t.Fatal("another endpoint was limited", w.Code)
		}
	})
}

// Doc 180 test 5: revoking the binding, withdrawing the capability, disabling the app and a restore
// pause are each honoured on the next request.
func TestIngressChangesAreHonouredOnTheNextRequest(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		binding := e.bind(t, "api", "heartbeat", ingressSecret("a"))
		if w := e.send(t); w.Code != 202 {
			t.Fatal("first", w.Code)
		}
		// Restore pause: the endpoint is real but cannot be served.
		mustExec(t, e.s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		if w := e.send(t); w.Code != 503 || w.Header().Get("Retry-After") != "30" {
			t.Fatal("restore pause", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
		if w := e.send(t); w.Code != 202 {
			t.Fatal("after the pause", w.Code)
		}
		// Disable the app.
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
		if w := e.send(t); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("disabled app", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, e.id)
		// Withdraw the capability.
		e.disable(t)
		if w := e.send(t); w.Code != 401 {
			t.Fatal("capability withdrawn", w.Code)
		}
		e.enable(t)
		if w := e.send(t); w.Code != 202 {
			t.Fatal("capability restored", w.Code)
		}
		// Revoke the binding: the endpoint is gone at once.
		if err := e.s.revokePlatformSecretBinding(e.actor, binding.ID, binding.Revision); err != nil {
			t.Fatal(err)
		}
		if w := e.send(t); w.Code != 401 || w.Body.String() != ingressDenied {
			t.Fatal("revoked binding", w.Code)
		}
		if e.forwarded() != 3 {
			t.Fatal("forwards", e.forwarded())
		}
	})
}

// Doc 180 test 6: the backend gets the verified fact and the nonce digest, never the secret. The
// command and assertion are signed through the real relay path and every byte is searched.
func TestIngressRelayRequestCarriesNoSecret(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		secret := ingressSecret("k")
		e.bind(t, "api", "heartbeat", secret)
		body := `{"timestamp":1,"nonce":"3f9c0000000000000000"}`
		if w := e.send(t, withBody(body), withNonce("3f9c0000000000000000")); w.Code != 202 {
			t.Fatal(w.Code)
		}
		f := e.forwards[0]
		anon := platformActorSession{anonymousIngress: "api"}
		var raw, sig []byte
		var token string
		var command control.WritableBackendRequestCommand
		a, err := e.s.withPlatformWritableBackendAccess(context.Background(), anon, e.id, func(a platformWritableBackendAccess, seed []byte) error {
			var err error
			raw, sig, token, command, err = signPlatformWritableBackendRequest(a, seed, "POST", f.light.decl.Path, f.body, "fixture-boot", platformIngressAssertion(f.light, f.body, f.nonce, f.received))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.viewer.Subject != "" || a.account != (platformActorAccount{}) || a.publicPolicy != f.light.grantKey() {
			t.Fatalf("an ingress access carries identity or another grant: %+v", a)
		}
		for name, bytesSeen := range map[string][]byte{"command": raw, "signature": sig, "assertion": []byte(token), "body": f.body, "path": []byte(f.light.decl.Path), "key": []byte(f.light.grantKey())} {
			if bytes.Contains(bytesSeen, secret) {
				t.Fatalf("the secret is in the %s", name)
			}
		}
		if bytes.Contains(raw, []byte(f.nonce)) || strings.Contains(token, f.nonce) {
			t.Fatal("the nonce travels in the command or assertion, not its digest")
		}
		if !appplatform.IsIngressAssertion(token) || command.AssertionSHA256 != platformDigest([]byte(token)) || command.Method != "POST" || command.Path != "/v1/ingress/heartbeat" || command.BodySHA256 != platformDigest(f.body) {
			t.Fatal("the command does not bind the ingress token", command)
		}
		v, err := control.VerifyWritableBackendRequest(raw, sig, a.intent.start.writer.authority, time.Now().UnixMilli())
		if err != nil || control.ValidateWritableBackendRequestData(v, f.body, token) != nil {
			t.Fatal("the runner refused the ingress request", err)
		}
		signer, err := a.intent.registration.Signer("fixture-boot")
		if err != nil {
			t.Fatal(err)
		}
		claims, err := appplatform.VerifyIngressAssertion(signer, token, "/v1/ingress/heartbeat", f.body, time.Now())
		if err != nil || claims.Endpoint != "api" || claims.Route != "heartbeat" || claims.Generation != a.intent.generation || claims.Installation != e.id ||
			claims.NonceDigest != appplatform.IngressDigest([]byte(f.nonce)) || claims.BodyDigest != appplatform.IngressDigest(f.body) || claims.ReceivedMS != f.received.UnixMilli() {
			t.Fatal(claims, err)
		}
		// The token cannot ride another body through the runner's checks.
		if control.ValidateWritableBackendRequestData(v, []byte(`{"x":1}`), token) == nil {
			t.Fatal("another body rode the ingress token")
		}
		// An assertion is never signed for a grant or binding the access does not show.
		stale := f.light
		stale.bindingRevision++
		if _, err := platformIngressAssertion(stale, f.body, f.nonce, f.received)(signer, a, time.Now()); err == nil {
			t.Fatal("an assertion was signed for a binding the access does not show")
		}
		// Without the grant or the binding the relay's own read refuses.
		if err := e.s.revokePlatformSecretBinding(e.actor, f.light.bindingID, f.light.bindingRevision); err != nil {
			t.Fatal(err)
		}
		if b, err := e.s.preparePlatformWritableBackendAccess(context.Background(), anon, e.id); err == nil {
			b.Close()
			t.Fatal("the relay admitted an endpoint whose binding is revoked")
		}
		if b, err := e.s.preparePlatformWritableBackendAccess(context.Background(), platformActorSession{anonymousIngress: "nope"}, e.id); err == nil {
			b.Close()
			t.Fatal("the relay admitted an endpoint that does not exist")
		}
	})
}

// Doc 180 test 7: a body over the route's bound is 413 before any authentication work (no credential
// is opened), and the static and ceiling refusals never reach the custody either.
func TestIngressBodyBoundAndRefusalsBeforeAuthentication(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		big := `{"p":"` + strings.Repeat("a", 1100) + `"}`
		opens := platformCredentialOpenCount.Load()
		if w := e.send(t, withBody(big)); w.Code != 413 {
			t.Fatal("over the route bound", w.Code)
		}
		huge := `{"p":"` + strings.Repeat("a", 5000) + `"}`
		for _, mod := range [][]func(*ingressSend){
			{withBody(huge)},
			{withBody(huge), func(m *ingressSend) { m.installation = "00000000-0000-4000-8000-000000000000" }},
			{withBody(huge), func(m *ingressSend) { m.unsigned = true }},
			{withBody(big), withEndpoint("unbound")},
		} {
			if w := e.send(t, mod...); w.Code != 413 {
				t.Fatal("over a bound", w.Code)
			}
		}
		if platformCredentialOpenCount.Load() != opens {
			t.Fatal("a credential was opened for an oversize request")
		}
		if e.forwarded() != 0 {
			t.Fatal("an oversize request reached the backend")
		}
		// The bound is the route's: a body between the two routes' bounds is fine on the larger one.
		e.bind(t, "hook", "webhook", ingressSecret("h"))
		mid := func(m *ingressSend) { m.route, m.endpoint, m.body = "webhook", "hook", big }
		if w := e.send(t, mid); w.Code != 202 {
			t.Fatal("within the larger route bound", w.Code)
		}
		// Static failures and the ceilings refuse before the custody is touched.
		opens = platformCredentialOpenCount.Load()
		for _, mod := range []func(*ingressSend){withBody("nope"), withNonce("short"), withTimestamp(1), func(m *ingressSend) { m.contentType = "text/plain" }} {
			if w := e.send(t, mod); w.Code != 401 {
				t.Fatal("static", w.Code)
			}
		}
		if platformCredentialOpenCount.Load() != opens {
			t.Fatal("a static failure opened a credential")
		}
	})
}

// The ceilings of step 2 apply before anything is looked up.
func TestIngressClientAndInstallationCeilings(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "hook", "webhook", ingressSecret("h"))
		e.bind(t, "hook-two", "webhook", ingressSecret("i"))
		e.bind(t, "hook-three", "webhook", ingressSecret("j"))
		opens := platformCredentialOpenCount.Load()
		// Two endpoints of 60 a minute each, so only the source ceiling of 120 can be what stops this.
		var ok, limited int
		for i := 0; i < platformIngressClientPerMinute+5; i++ {
			endpoint := []string{"hook", "hook-two"}[i%2]
			switch w := e.send(t, withEndpoint(endpoint), func(m *ingressSend) { m.route, m.remote = "webhook", "10.200.0.1:1" }); w.Code {
			case 202:
				ok++
			case 429:
				limited++
				if w.Header().Get("Retry-After") == "" {
					t.Fatal("no Retry-After")
				}
			default:
				t.Fatal(w.Code)
			}
		}
		if ok != platformIngressClientPerMinute || limited != 5 {
			t.Fatal("per-client ceiling:", ok, limited)
		}
		if platformCredentialOpenCount.Load()-opens != int64(ok) {
			t.Fatal("a limited request opened a credential")
		}
		// Another source is unaffected.
		if w := e.send(t, withEndpoint("hook-three"), func(m *ingressSend) { m.route, m.remote = "webhook", "10.201.0.1:1" }); w.Code != 202 {
			t.Fatal("another source", w.Code)
		}
		// The installation ceiling holds across sources for verified requests: exhaust it as 600 would, then
		// the next verified one is refused and never reaches the backend.
		for i := 0; i < platformIngressInstallationPerMinute; i++ {
			e.s.platformIngress.installations.allow("installation:"+e.id, platformIngressInstallationPerMinute, time.Minute, time.Now())
		}
		forwards := e.forwarded()
		w := e.send(t, withEndpoint("hook-three"), func(m *ingressSend) { m.route, m.remote = "webhook", "10.202.0.1:1" })
		if w.Code != 429 || w.Header().Get("Retry-After") == "" || e.forwarded() != forwards {
			t.Fatal("per-installation ceiling", w.Code)
		}
	})
}

// Secrets are entered by the administrator, never generated or shown (owner decision 3), and an
// endpoint is a binding with the ingress operation and the route in its policy.
func TestIngressBindingReviewAndPolicy(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		short, err := e.s.changePlatformCredential(e.actor, platformCredentialChange{kind: "create", label: "Short", value: []byte(strings.Repeat("s", 31))})
		if err != nil {
			t.Fatal(err)
		}
		long, err := e.s.changePlatformCredential(e.actor, platformCredentialChange{kind: "create", label: "Long", value: []byte(strings.Repeat("l", 257))})
		if err != nil {
			t.Fatal(err)
		}
		good, err := e.s.changePlatformCredential(e.actor, platformCredentialChange{kind: "create", label: "Good", value: ingressSecret("g")})
		if err != nil {
			t.Fatal(err)
		}
		review := func(credential, name, operation, principals, route string) error {
			_, err := e.s.createPlatformSecretBindingReviewFull(e.actor, e.id, name, credential, operation, principals, nil, route)
			return err
		}
		// Not before the capability is granted.
		if review(good.ID, "api", "ingress_hmac_sha256", "", "heartbeat") == nil {
			t.Fatal("a review before the capability was granted")
		}
		e.enable(t)
		for name, err := range map[string]error{
			"31 bytes":            review(short.ID, "api", "ingress_hmac_sha256", "", "heartbeat"),
			"257 bytes":           review(long.ID, "api", "ingress_hmac_sha256", "", "heartbeat"),
			"undeclared route":    review(good.ID, "api", "ingress_hmac_sha256", "", "nope"),
			"no route":            review(good.ID, "api", "ingress_hmac_sha256", "", ""),
			"principals":          review(good.ID, "api", "ingress_hmac_sha256", platformPrincipalsServiceOnly, "heartbeat"),
			"route on bearer":     review(good.ID, "api", "http_bearer", "", "heartbeat"),
			"bad endpoint name":   review(good.ID, "Api", "ingress_hmac_sha256", "", "heartbeat"),
			"endpoint name digit": review(good.ID, "1api", "ingress_hmac_sha256", "", "heartbeat"),
		} {
			if err == nil {
				t.Fatalf("%s was reviewed", name)
			}
		}
		plan, err := e.s.createPlatformSecretBindingReviewFull(e.actor, e.id, "api", good.ID, "ingress_hmac_sha256", "", nil, "heartbeat")
		if err != nil {
			t.Fatal(err)
		}
		var shown platformSecretBindingReview
		if json.Unmarshal([]byte(plan.JSON), &shown) != nil || shown.NetworkPolicySHA256 != "" || shown.IngressRoute != "heartbeat" || len(shown.Principals) != 0 || strings.Contains(plan.JSON, string(ingressSecret("g"))) {
			t.Fatal("review", plan.JSON)
		}
		binding, err := e.s.consumePlatformSecretBindingReview(e.actor, plan.ID, plan.SHA256)
		if err != nil || binding.Operation != "ingress_hmac_sha256" || binding.IngressRoute != "heartbeat" || len(binding.Principals) != 0 {
			t.Fatal(binding, err)
		}
		stored := scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, binding.ID)
		if stored != `{"format":"portal.secret-binding-policy/2-dev.1","operation":"ingress_hmac_sha256","ingress_route":"heartbeat"}` {
			t.Fatal("stored policy", stored)
		}
		// An endpoint is not a credential anybody can use as a header.
		facts, err := loadPlatformSecretBindingFacts(e.s.st.poolExec(), e.id, false)
		if err == nil {
			if _, _, err := loadPlatformSecretBindingUseFor(e.s.st.poolExec(), e.id, "api", facts, platformPrincipalService); err == nil {
				t.Fatal("an ingress binding was usable as an outbound credential")
			}
		}
		list, err := e.s.listPlatformSecretBindings(e.actor, e.id)
		if err != nil || len(list) != 1 || list[0].IngressRoute != "heartbeat" {
			t.Fatal(list, err)
		}
		// The policy parser accepts only the canonical bytes.
		for _, raw := range []string{
			stored + " ",
			strings.Replace(stored, `"ingress_route":"heartbeat"`, `"ingress_route":"heartbeat","principals":["service"]`, 1),
			strings.Replace(stored, `"ingress_route":"heartbeat"`, `"ingress_route":"heartbeat","network_policy_sha256":"`+strings.Repeat("a", 64)+`"`, 1),
			strings.Replace(stored, `heartbeat`, `Heartbeat`, 1),
			strings.Replace(stored, `,"ingress_route":"heartbeat"`, ``, 1),
		} {
			if _, err := parsePlatformSecretBindingPolicy([]byte(raw)); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		}
	})
}

// Traffic is aggregated, never an address, and only the timer writes it.
func TestIngressSummaryIsAggregatedWithoutAnAddress(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		for i := 0; i < 3; i++ {
			e.send(t, withRemote("203.0.113.9:1234"))
		}
		e.send(t, withRemote("203.0.113.9:1234"), func(m *ingressSend) { m.secret = ingressSecret("z") })
		if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.ingress_summary'`); n != 0 {
			t.Fatal("a request wrote the summary", n)
		}
		if c := e.s.platformIngress.counters(e.id, "api"); c == nil || c.Accepted != 3 || c.Refused != 1 {
			t.Fatalf("counters %+v", c)
		}
		list, err := e.s.listPlatformSecretBindings(e.actor, e.id)
		if err != nil || len(list) != 1 || list[0].Ingress == nil || list[0].Ingress.Accepted != 3 {
			t.Fatal("console detail", list, err)
		}
		e.s.flushPlatformIngressSummary()
		var detail string
		var ip string
		row := e.s.st.queryRow(`SELECT detail,ip FROM audit_log WHERE action='app_platform.ingress_summary' AND target_id=?`, e.id)
		if err := row.Scan(&detail, &ip); err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(detail), &parsed) != nil || parsed["accepted"] != float64(3) || parsed["forwarded"] != float64(3) || ip != "" || strings.Contains(detail, "203.0.113") {
			t.Fatal("summary", detail, ip)
		}
		if refusals, _ := parsed["refusals"].(map[string]any); refusals["auth"] != float64(1) {
			t.Fatal("refusals", detail)
		}
		if endpoints, _ := parsed["endpoints"].(map[string]any); endpoints["api"] == nil {
			t.Fatal("per-endpoint counts", detail)
		}
	})
}

// A backend that refuses or fails is reported as the contract says (step 8).
func TestIngressBackendOutcomes(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		for status, want := range map[int]int{200: 202, 204: 202, 400: 400, 404: 400, 409: 400, 429: 400, 500: 503, 502: 503} {
			e.mu.Lock()
			e.response = driver.BackendResponse{Status: status, Body: []byte(`{"ok":true}`)}
			e.mu.Unlock()
			w := e.send(t)
			if w.Code != want {
				t.Fatalf("backend %d: %d", status, w.Code)
			}
			if want == 503 && w.Header().Get("Retry-After") != "30" {
				t.Fatal("no Retry-After")
			}
		}
		// The runner or backend being unavailable is a 503 with Retry-After.
		e.s.platformIngressForwardHook = func(context.Context, platformIngressLight, []byte, string, time.Time) (driver.BackendResponse, error) {
			return driver.BackendResponse{}, errAppPlatformBackendRequestUncertain
		}
		// The per-endpoint ceiling of this route is 10 a minute; use another endpoint for the failure case.
		e.bind(t, "two", "webhook", ingressSecret("b"))
		if w := e.send(t, func(m *ingressSend) { m.route, m.endpoint = "webhook", "two" }); w.Code != 503 || w.Header().Get("Retry-After") != "30" {
			t.Fatal("unavailable backend", w.Code)
		}
	})
}
