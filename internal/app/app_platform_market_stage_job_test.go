package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type platformStageBody struct {
	State         string `json:"state"`
	Release       string `json:"release_sha256"`
	AppID         string `json:"app_id"`
	BytesDone     string `json:"bytes_done"`
	BytesTotal    string `json:"bytes_total"`
	ErrorCode     string `json:"error_code"`
	Code          string `json:"code"`
	Reused        bool   `json:"reused"`
	TrustRevision string `json:"trust_revision"`
}

type platformStageHarness struct {
	*platformCatalogueFixture
	mux    *http.ServeMux
	cookie string
	body   string
}

func newPlatformStageHarness(t *testing.T) *platformStageHarness {
	t.Helper()
	f := newPlatformCatalogueFixture(t)
	client := f.remote.client()
	mux := http.NewServeMux()
	f.s.platformAdminRoute(mux, "POST /api/admin/app-platform/market/stage", func(w http.ResponseWriter, r *http.Request, user string) {
		f.s.apiPlatformMarketStageClient(w, r, user, client)
	})
	f.s.platformAdminRoute(mux, "GET /api/admin/app-platform/market/stage", f.s.apiPlatformMarketStageStatus)
	cookie := f.s.managedCookie(*f.s.st.GetUser(f.actor.username), f.actor.sessionID, f.actor.expires.Unix())
	f.s.platformStageWaitFor = 150 * time.Millisecond
	t.Cleanup(func() { f.s.stopPlatformStageJobs(context.Background()) })
	body := `{"catalogue_url":"https://127.0.0.1/index.json","release_sha256":"` + platformDigest(f.parts.descriptor) + `"}`
	return &platformStageHarness{platformCatalogueFixture: f, mux: mux, cookie: cookie, body: body}
}

func (h *platformStageHarness) post(t *testing.T) (int, platformStageBody) {
	t.Helper()
	return h.do(t, "POST", "/api/admin/app-platform/market/stage", h.body)
}

func (h *platformStageHarness) status(t *testing.T) (int, platformStageBody) {
	t.Helper()
	return h.do(t, "GET", "/api/admin/app-platform/market/stage?release_sha256="+platformDigest(h.parts.descriptor), "")
}

func (h *platformStageHarness) do(t *testing.T, method, path, body string) (int, platformStageBody) {
	t.Helper()
	w := sessionTestRequest(h.mux, method, path, h.cookie, body)
	var out platformStageBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: %d %q", method, path, w.Code, w.Body.String())
	}
	return w.Code, out
}

func (h *platformStageHarness) until(t *testing.T, what string, done func(int, platformStageBody) bool) (int, platformStageBody) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := h.status(t)
		if done(code, body) {
			return code, body
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last %d %+v", what, code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *platformStageHarness) packets() int {
	h.remote.mu.Lock()
	defer h.remote.mu.Unlock()
	return h.remote.packets
}

func (h *platformStageHarness) releases(t *testing.T) int {
	return scalar[int](t, h.s.st, `SELECT COUNT(*) FROM app_platform_releases`)
}

// Fails if a slow package download holds the request open instead of answering 202, if progress is not reported,
// or if the finished download is not reachable as staged.
func TestMarketStageAnswersAcceptedThenPollingReachesStaged(t *testing.T) {
	h := newPlatformStageHarness(t)
	h.remote.gate = make(chan struct{})
	started := time.Now()
	code, body := h.post(t)
	if code != 202 || body.State != "downloading" || body.Release != platformDigest(h.parts.descriptor) {
		t.Fatal(code, body)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("the request waited for the download")
	}
	_, body = h.until(t, "progress", func(_ int, b platformStageBody) bool {
		return b.State == "downloading" && b.BytesDone != "" && b.BytesDone != "0"
	})
	if body.BytesTotal != strconv.Itoa(len(h.remote.pkg)) {
		t.Fatalf("total %q want %d", body.BytesTotal, len(h.remote.pkg))
	}
	if h.releases(t) != 0 {
		t.Fatal("a release exists before the download finished")
	}
	close(h.remote.gate)
	code, body = h.until(t, "staged", func(_ int, b platformStageBody) bool { return b.State == "staged" })
	if code != 200 || body.Release != platformDigest(h.parts.descriptor) || body.AppID == "" || body.TrustRevision == "" || h.releases(t) != 1 {
		t.Fatal(code, body)
	}
}

// Fails if a second click starts a second download instead of joining the running one.
func TestMarketStageJoinsARunningDownload(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.remote.gate = make(chan struct{})
	for range 2 {
		if code, body := h.post(t); code != 202 || body.State != "downloading" {
			t.Fatal(code, body)
		}
	}
	h.until(t, "progress", func(_ int, b platformStageBody) bool { return b.BytesDone != "" && b.BytesDone != "0" })
	if n := h.packets(); n != 1 {
		t.Fatalf("%d package requests for one release", n)
	}
	close(h.remote.gate)
	h.until(t, "staged", func(_ int, b platformStageBody) bool { return b.State == "staged" })
	if h.releases(t) != 1 {
		t.Fatal("two releases")
	}
}

// Fails if a failed background download is not readable by every poller with a public code, or if a later request
// cannot restart it from the partial.
func TestMarketStageFailureStaysReadableAndRestartable(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.remote.gate, h.remote.failAfterGate = make(chan struct{}), true
	if code, _ := h.post(t); code != 202 {
		t.Fatal(code)
	}
	close(h.remote.gate)
	_, body := h.until(t, "failure", func(_ int, b platformStageBody) bool { return b.State == "failed" })
	if body.ErrorCode != "app_platform_unavailable" {
		t.Fatal(body)
	}
	if code, again := h.status(t); code != 200 || again.State != "failed" || again.ErrorCode != body.ErrorCode {
		t.Fatal("a second poller did not see the failure", code, again)
	}
	h.remote.mu.Lock()
	h.remote.gate, h.remote.failAfterGate, h.remote.ranges = nil, false, nil
	h.remote.mu.Unlock()
	code, body := h.post(t)
	if code == 202 {
		// The request waits only briefly (150ms here) before answering 202; a slow or loaded host can take
		// longer than that for the rest of the package, which then finishes in the background.
		code, body = h.until(t, "the restarted download", func(_ int, b platformStageBody) bool { return b.State == "staged" })
	}
	if code != 200 || body.Release != platformDigest(h.parts.descriptor) {
		t.Fatal(code, body)
	}
	h.remote.mu.Lock()
	ranged := strings.Join(h.remote.ranges, ",")
	h.remote.mu.Unlock()
	if !strings.HasPrefix(ranged, "bytes=") {
		t.Fatalf("the restart did not resume the partial: %q", ranged)
	}
}

// Fails if staging after a long download trusts the authority checked when the click was made.
func TestMarketStageRechecksAuthorityWhenTheDownloadFinishes(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.remote.gate = make(chan struct{})
	if code, _ := h.post(t); code != 202 {
		t.Fatal(code)
	}
	mustExec(t, h.s.st, `UPDATE users SET role='user' WHERE username=?`, h.actor.username)
	close(h.remote.gate)
	deadline := time.Now().Add(5 * time.Second)
	for h.s.platformStageJobs.running() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mustExec(t, h.s.st, `UPDATE users SET role='admin' WHERE username=?`, h.actor.username)
	if h.releases(t) != 0 {
		t.Fatal("a release was staged for an actor who lost authority")
	}
	// Losing authority says nothing about the bytes: the finished download stays for the next request.
	store, _ := h.s.platformImages()
	if info, err := os.Stat(store.partialPath("catalogue:https://127.0.0.1/app.portalapp")); err != nil || info.Size() != int64(len(h.remote.pkg)) {
		t.Fatal("the complete partial was discarded after an authority failure", err)
	}
	_, body := h.status(t)
	if body.State != "failed" || body.ErrorCode != "app_platform_permission_required" {
		t.Fatal(body)
	}
}

// Fails if a quick package still goes through the 202 path.
func TestMarketStageFastPathAnswersOK(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.s.platformStageWaitFor = 15 * time.Second
	code, body := h.post(t)
	if code != 200 || body.Release != platformDigest(h.parts.descriptor) || body.AppID == "" || body.Reused || body.State != "" {
		t.Fatal(code, body)
	}
	code, body = h.status(t)
	if code != 200 || body.State != "staged" {
		t.Fatal(code, body)
	}
}

func TestMarketStageStatusUnknownAndInvalid(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	if code, body := h.status(t); code != 404 || body.Code != "app_platform_stage_unknown" {
		t.Fatal(code, body)
	}
	if code, _ := h.do(t, "GET", "/api/admin/app-platform/market/stage?release_sha256=zz", ""); code != 400 {
		t.Fatal(code)
	}
	if code, _ := h.do(t, "GET", "/api/admin/app-platform/market/stage", ""); code != 400 {
		t.Fatal(code)
	}
	if w := sessionTestRequest(h.mux, "GET", "/api/admin/app-platform/market/stage?release_sha256="+platformDigest(h.parts.descriptor), "", ""); w.Code != 401 {
		t.Fatal("anonymous status read allowed", w.Code)
	}
}

// Fails if a server shutdown leaves a download running or lets it stage afterwards.
func TestMarketStageJobsStopAtShutdown(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.remote.gate = make(chan struct{})
	if code, _ := h.post(t); code != 202 {
		t.Fatal(code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h.s.stopPlatformStageJobs(ctx)
	if h.s.platformStageJobs.running() != 0 || h.releases(t) != 0 {
		t.Fatal("a job outlived the shutdown")
	}
	close(h.remote.gate)
	if code, _ := h.post(t); code == 202 {
		t.Fatal("a job started after shutdown")
	}
}

// Fails if the finished download is thrown away because both document slots were busy when it was time to stage.
func TestMarketStageWaitsForADocumentSlotInsteadOfFailingBusy(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	h.remote.gate = make(chan struct{})
	if code, _ := h.post(t); code != 202 {
		t.Fatal(code)
	}
	var held []func()
	for {
		release, ok := h.s.platformDocuments.acquire()
		if !ok {
			break
		}
		held = append(held, release)
	}
	close(h.remote.gate)
	time.Sleep(400 * time.Millisecond)
	if _, body := h.status(t); body.State != "downloading" {
		t.Fatal("the job gave up while no slot was free", body)
	}
	for _, release := range held {
		release()
	}
	h.until(t, "staged", func(_ int, b platformStageBody) bool { return b.State == "staged" })
	if n := h.packets(); n != 1 {
		t.Fatalf("%d package requests: the download was repeated", n)
	}
}

// Fails if a joined request's authority is ignored when the first requester has lost theirs by the end.
func TestMarketStageFinishesAsAJoinedRequesterWhenTheFirstLostAuthority(t *testing.T) {
	t.Parallel()
	h := newPlatformStageHarness(t)
	second := platformSecondAdminActor(t, h.s, "second-admin")
	secondCookie := h.s.managedCookie(*h.s.st.GetUser(second.username), second.sessionID, second.expires.Unix())
	h.remote.gate = make(chan struct{})
	if code, _ := h.post(t); code != 202 {
		t.Fatal(code)
	}
	w := sessionTestRequest(h.mux, "POST", "/api/admin/app-platform/market/stage", secondCookie, h.body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	mustExec(t, h.s.st, `UPDATE users SET role='user' WHERE username=?`, h.actor.username)
	close(h.remote.gate)
	deadline := time.Now().Add(5 * time.Second)
	for h.s.platformStageJobs.running() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.releases(t) != 1 {
		t.Fatal("the joined, still authorised requester did not get the release staged")
	}
	if got := scalar[string](t, h.s.st, `SELECT actor FROM audit_log WHERE action='app_platform.release_staged'`); got != second.username {
		t.Fatalf("audited as %q", got)
	}
}

// Fails if more downloads than the bound run at once, or a join counts against it.
func TestMarketStageJobsBoundRunningDownloads(t *testing.T) {
	t.Parallel()
	var jobs platformMarketStageJobs
	block := make(chan struct{})
	run := func(ctx context.Context, _ *platformMarketStageJob) (platformStagedRelease, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return platformStagedRelease{}, nil
	}
	t.Cleanup(func() { close(block); jobs.stop(context.Background()) })
	for i := 0; i < platformMarketStageRunning; i++ {
		if _, err := jobs.start("k"+strconv.Itoa(i), strings.Repeat(strconv.Itoa(i), 64), platformActorSession{}, nil, run); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := jobs.start("k0", strings.Repeat("0", 64), platformActorSession{}, nil, run); err != nil {
		t.Fatal("joining a running download was refused", err)
	}
	if _, err := jobs.start("kx", strings.Repeat("f", 64), platformActorSession{}, nil, run); !errors.Is(err, errPlatformManagementBusy) {
		t.Fatal("a download over the bound started", err)
	}
}

// Fails if one session joining twice is remembered twice, which would crowd other requesters out of the bound.
func TestMarketStageJobsRememberEachSessionOnce(t *testing.T) {
	t.Parallel()
	var jobs platformMarketStageJobs
	block := make(chan struct{})
	t.Cleanup(func() { close(block); jobs.stop(context.Background()) })
	run := func(ctx context.Context, _ *platformMarketStageJob) (platformStagedRelease, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return platformStagedRelease{}, nil
	}
	a, b := platformActorSession{sessionID: "a"}, platformActorSession{sessionID: "b"}
	var job *platformMarketStageJob
	for _, who := range []platformActorSession{a, a, b, a, b} {
		var err error
		if job, err = jobs.start("k", strings.Repeat("1", 64), who, nil, run); err != nil {
			t.Fatal(err)
		}
	}
	if got := jobs.requesters(job); len(got) != 2 || got[0].sessionID != "a" || got[1].sessionID != "b" {
		t.Fatal(got)
	}
}
