package app

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The session-free task route must never let one caller, anonymous or from another installation, make a
// backend's own submissions fail or wait: no slot is held across a body read, no slot is taken before
// the identity is verified, and the slots that exist belong to one verified installation each.

type taskRouteFixture struct {
	*taskRefusalFixture
	server *httptest.Server
	// returned is signalled each time a handler returns.
	returned chan struct{}
}

func newTaskRouteFixture(t *testing.T) *taskRouteFixture {
	t.Helper()
	f := &taskRouteFixture{taskRefusalFixture: newTaskRefusalFixture(t, false)}
	mux := http.NewServeMux()
	f.s.wireRoutes(mux)
	f.returned = make(chan struct{}, 64)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		select {
		case f.returned <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func taskRoutePath(installation string) string {
	return "/api/app-platform/installations/" + installation + "/tasks/background"
}

// stall opens a connection that names the installation with the given identity header and sends only
// the first bytes of a longer body, then leaves it open.
func (f *taskRouteFixture) stall(t *testing.T, installation, identity string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: portal\r\nContent-Type: application/json\r\nX-Portal-Service-Identity: %s\r\nContent-Length: 400\r\n\r\n{\"occurrenceId\":\"sta",
		taskRoutePath(installation), identity)
	return conn
}

func (f *taskRouteFixture) legit(t *testing.T, occurrence string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"occurrenceId":%q,"handler":"app.backend.task","payload":{"v":1}}`, occurrence)
	request, _ := http.NewRequest(http.MethodPost, f.server.URL+taskRoutePath(f.installation), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Portal-Service-Identity", base64.RawURLEncoding.EncodeToString(f.identity))
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("legitimate submission: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

// Stalled partial-body connections hold no slot, whether the caller is anonymous or holds a valid
// credential of the very installation that then submits.
func TestPlatformServiceTaskStalledBodiesNeverMakeABackendBusy(t *testing.T) {
	t.Parallel()
	f := newTaskRouteFixture(t)
	junk := base64.RawURLEncoding.EncodeToString([]byte(`{"not":"verifiable"}`))
	valid := base64.RawURLEncoding.EncodeToString(f.identity)
	for i := 0; i < 6; i++ {
		f.stall(t, f.installation, junk)
		f.stall(t, f.installation, valid)
	}
	time.Sleep(200 * time.Millisecond)
	if code, body := f.legit(t, "while-stalled"); code != 200 {
		t.Fatalf("a verified backend got %d %s while twelve bodies were stalled", code, body)
	}
}

// A stalled body is ended by the read deadline instead of living forever.
func TestPlatformServiceTaskReadDeadlineEndsAStalledBody(t *testing.T) {
	old := platformTaskBodyTimeout
	platformTaskBodyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { platformTaskBodyTimeout = old })
	f := newTaskRouteFixture(t)
	f.stall(t, f.installation, base64.RawURLEncoding.EncodeToString(f.identity))
	select {
	case <-f.returned:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler was still waiting for a stalled body after 3 s")
	}
}

// The slots belong to one verified installation each: filling another installation's slots, or the
// slots of an installation nobody has verified, never makes this installation's submissions busy.
func TestPlatformServiceTaskBudgetIsScopedToOneVerifiedInstallation(t *testing.T) {
	t.Parallel()
	f := newTaskRefusalFixture(t, false)
	for _, other := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		for i := 0; i < platformServiceTaskSlots; i++ {
			release, ok := f.s.platformServiceTasks.acquire(other)
			if !ok {
				t.Fatalf("%s: slot %d refused", other, i)
			}
			t.Cleanup(release)
		}
		if _, ok := f.s.platformServiceTasks.acquire(other); ok {
			t.Fatalf("%s: more than %d slots", other, platformServiceTaskSlots)
		}
	}
	if code, body := f.submit(t, "other-installations-full"); code != 200 {
		t.Fatalf("another installation's flood made this one answer %d %s", code, body)
	}
	// Its own slots full: a verified submission is refused busy, an anonymous caller naming it is not told
	// anything but the unauthenticated answer, and neither holds a slot of its own.
	var releases []func()
	for i := 0; i < platformServiceTaskSlots; i++ {
		release, ok := f.s.platformServiceTasks.acquire(f.installation)
		if !ok {
			t.Fatal("own slot refused")
		}
		releases = append(releases, release)
	}
	if code, body := f.submit(t, "own-full"); code != 429 || !strings.Contains(body, "app_platform_busy") {
		t.Fatalf("own slots full: %d %s", code, body)
	}
	anonymous := base64.RawURLEncoding.EncodeToString([]byte(`{"not":"verifiable"}`))
	request := httptest.NewRequest(http.MethodPost, taskRoutePath(f.installation), strings.NewReader(`{"occurrenceId":"anon","handler":"app.backend.task","payload":{}}`))
	request.SetPathValue("installation", f.installation)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Portal-Service-Identity", anonymous)
	recorder := httptest.NewRecorder()
	f.s.apiPlatformServiceTaskSchedule(recorder, request)
	if recorder.Code != 404 {
		t.Fatalf("an anonymous caller naming a busy installation got %d %s", recorder.Code, recorder.Body.String())
	}
	for _, release := range releases {
		release()
	}
	if code, _ := f.submit(t, "own-free"); code != 200 {
		t.Fatalf("slots were not given back: %d", code)
	}
}

func TestPlatformServiceTaskBudgetMapStaysBoundedAndEmptiesWhenIdle(t *testing.T) {
	t.Parallel()
	var b platformInstallationBudgets
	var releases []func()
	for i := 0; i < 1000; i++ {
		release, ok := b.acquire(fmt.Sprintf("installation-%d", i))
		if !ok {
			t.Fatal("a distinct installation was refused")
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
		release() // a second release is a no-op
	}
	if n := b.size(); n != 0 {
		t.Fatalf("%d entries left after every release", n)
	}
	if release, ok := b.acquire("x"); !ok {
		t.Fatal("refused after the map emptied")
	} else {
		release()
	}
}

// What a remembered refusal answers must be what the full path would answer for the same request.
func TestPlatformServiceTaskRememberedRefusalAgreesWithTheFullPath(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body   string
		change string
	}{
		"unresolvable handler": {`{"occurrenceId":"p:1","handler":"app.unknown.task","payload":{"v":1}}`, ``},
		"background.run revoked": {`{"occurrenceId":"p:2","handler":"app.backend.task","payload":{"v":1}}`,
			`UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`},
		"event handler without events.subscribe": {`{"occurrenceId":"p:3","handler":"app.event.delivery","payload":{"v":1}}`, ``},
		"event handler with a malformed delivery": {`{"occurrenceId":"p:4","handler":"app.event.delivery","payload":{"bad":true}}`,
			`UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='events.subscribe'`},
		"plain backend task": {`{"occurrenceId":"p:5","handler":"app.backend.task","payload":{"v":1}}`, ``},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTaskRefusalFixture(t, true)
			if tc.change != "" {
				mustExec(t, f.s.st, tc.change, f.installation)
			}
			submit := func() int {
				return platformTaskHTTPWithIdentity(t, f.s, f.installation, tc.body, f.identity).Code
			}
			f.s.platformTaskRefusals.reset()
			cold := submit()
			f.submit(t, "prime:1") // a plain refusal remembers the credential
			primed := submit()
			if cold != primed {
				t.Fatalf("full path %d, remembered %d", cold, primed)
			}
		})
	}
}

// An empty or missing body is answered the same whether or not the credential was just remembered, and
// a credential retired in the last moments is told nothing a stranger would not be told.
func TestPlatformServiceTaskEmptyBodyAnswerDoesNotDependOnTheMemory(t *testing.T) {
	t.Parallel()
	f := newTaskRefusalFixture(t, true)
	empty := func() int { return platformTaskHTTPWithIdentity(t, f.s, f.installation, ``, f.identity).Code }
	f.s.platformTaskRefusals.reset()
	cold := empty()
	f.submit(t, "prime:1")
	if primed := empty(); primed != cold || cold != 400 {
		t.Fatalf("valid credential, empty body: cold %d remembered %d, want 400", cold, primed)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET policy_revision=policy_revision+1 WHERE id=?`, f.installation)
	if code := empty(); code != 404 {
		t.Fatalf("a retired credential with an empty body got %d, want 404", code)
	}
}

// The credential's own expiry is compared in the database's clock.
func TestPlatformServiceTaskRememberedRefusalChecksCredentialExpiryInTheDatabaseClock(t *testing.T) {
	t.Parallel()
	f := newTaskRefusalFixture(t, true)
	f.submit(t, "prime:1")
	entry, ok := f.s.platformTaskRefusals.lookup(platformTaskRefusalKey(f.identity), f.installation)
	if !ok {
		t.Fatal("not remembered")
	}
	now, _ := platformDatabaseNow(f.s.st.poolExec())
	body := []byte(`{"occurrenceId":"expiry:1","handler":"app.backend.task","payload":{"v":1}}`)
	entry.expiresMS = now + 60_000
	if !f.s.platformTaskBodyQuotaRefused(entry, body) {
		t.Fatal("a live credential was not refused")
	}
	entry.expiresMS = now - 1
	if f.s.platformTaskBodyQuotaRefused(entry, body) {
		t.Fatal("an expired credential was refused from the memory")
	}
}
