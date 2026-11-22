package app

import (
	"context"
	"encoding/json"
	"net/http"
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
	old := platformMarketStageWait
	platformMarketStageWait = 150 * time.Millisecond
	t.Cleanup(func() {
		platformMarketStageWait = old
		f.s.stopPlatformStageJobs(context.Background())
	})
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

// Fails if a failed background download is not reported with a public code exactly once, or if a later request
// cannot restart it from the partial.
func TestMarketStageFailureIsReportedOnceAndRestartable(t *testing.T) {
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
	if code, _ := h.status(t); code != 404 {
		t.Fatal("a failure was reported twice", code)
	}
	h.remote.mu.Lock()
	h.remote.gate, h.remote.failAfterGate, h.remote.ranges = nil, false, nil
	h.remote.mu.Unlock()
	code, body := h.post(t)
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
	_, body := h.status(t)
	if body.State != "failed" || body.ErrorCode != "app_platform_permission_required" {
		t.Fatal(body)
	}
}

// Fails if a quick package still goes through the 202 path.
func TestMarketStageFastPathAnswersOK(t *testing.T) {
	h := newPlatformStageHarness(t)
	platformMarketStageWait = 15 * time.Second
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
