package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Staging a package from the online catalogue takes as long as the download, which can be minutes, while the
// proxy in front of the portal cuts any answer at 100 seconds. So the request starts (or joins) a background
// download and waits only a short while for it; a slower one is answered 202 and read by polling.

// platformMarketStageWaitDefault is how long a request waits for the background download before answering 202.
const platformMarketStageWaitDefault = 15 * time.Second

func (s *Server) platformStageWait() time.Duration {
	if s.platformStageWaitFor > 0 {
		return s.platformStageWaitFor
	}
	return platformMarketStageWaitDefault
}

const (
	// platformMarketStageRunning bounds the downloads in flight at once; each holds staging space and a socket.
	platformMarketStageRunning = 2
	// platformMarketStageActors bounds the requesters a job remembers.
	platformMarketStageActors = 8
	// platformMarketStageJobsMax bounds the table: running downloads plus finished ones not yet read.
	platformMarketStageJobsMax = 32
	// platformMarketStageKeep is how long a finished job stays readable.
	platformMarketStageKeep = 10 * time.Minute
	// platformMarketStageJobLimit is the whole budget of one job: the two download attempts the package step may
	// make (the second starts from nothing after a damaged partial), plus a minute to verify and store.
	platformMarketStageJobLimit = 2*platformPackageDownloadTimeout + time.Minute
)

type platformMarketStageJob struct {
	key, sha string
	store    *platformImageStore
	progress platformDownloadProgress
	done     chan struct{}
	// actors are the sessions of the requests that started or joined the job, first one first; guarded by the
	// table's lock. The final step runs as the first whose authority still holds.
	actors []platformActorSession
	// result, err and finished are written once, before done is closed, under the table's lock.
	result   platformStagedRelease
	err      error
	finished time.Time
}

// platformMarketStageView is a job as one reader saw it, copied under the table's lock.
type platformMarketStageView struct {
	job     *platformMarketStageJob
	running bool
	result  platformStagedRelease
	err     error
}

// platformMarketStageJobs is the in-memory table of background package downloads. The zero value is ready. It
// holds nothing the database needs: a restart loses the jobs and keeps the partial files, so the next request
// resumes.
type platformMarketStageJobs struct {
	mu     sync.Mutex
	jobs   map[string]*platformMarketStageJob
	root   context.Context
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

func (t *platformMarketStageJobs) running() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, j := range t.jobs {
		if j.finished.IsZero() {
			n++
		}
	}
	return n
}

// start runs the download for key in the background, or joins the one already running for it. Concurrent
// requests for one package share one download; a finished job is replaced, so a later request restarts a failed
// one (the partial resumes).
func (t *platformMarketStageJobs) start(key, sha string, actor platformActorSession, store *platformImageStore, run func(context.Context, *platformMarketStageJob) (platformStagedRelease, error)) (*platformMarketStageJob, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errPlatformManagementBusy
	}
	now := time.Now()
	for k, j := range t.jobs {
		if !j.finished.IsZero() && now.Sub(j.finished) > platformMarketStageKeep {
			delete(t.jobs, k)
		}
	}
	for k, j := range t.jobs {
		if j.sha != sha && k != key {
			continue
		}
		if j.finished.IsZero() {
			if j.sha == sha {
				known := false
				for _, a := range j.actors {
					known = known || a.sessionID == actor.sessionID
				}
				if !known && len(j.actors) < platformMarketStageActors {
					j.actors = append(j.actors, actor)
				}
				return j, nil
			}
			// One package, two different releases: the running one owns the partial file.
			return nil, errAppPlatformOperationConflict
		}
		delete(t.jobs, k)
	}
	running := 0
	for _, j := range t.jobs {
		if j.finished.IsZero() {
			running++
		}
	}
	if running >= platformMarketStageRunning {
		return nil, errPlatformManagementBusy
	}
	if len(t.jobs) >= platformMarketStageJobsMax {
		var oldest string
		for k, j := range t.jobs {
			if !j.finished.IsZero() && (oldest == "" || j.finished.Before(t.jobs[oldest].finished)) {
				oldest = k
			}
		}
		if oldest == "" {
			return nil, errPlatformManagementBusy
		}
		delete(t.jobs, oldest)
	}
	if t.root == nil {
		t.root, t.cancel = context.WithCancel(context.Background())
	}
	job := &platformMarketStageJob{key: key, sha: sha, store: store, done: make(chan struct{}), actors: []platformActorSession{actor}}
	if t.jobs == nil {
		t.jobs = make(map[string]*platformMarketStageJob)
	}
	t.jobs[key] = job
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		ctx, cancel := context.WithTimeout(t.root, platformMarketStageJobLimit)
		defer cancel()
		result, err := run(context.WithValue(ctx, platformDownloadProgressKey{}, &job.progress), job)
		t.mu.Lock()
		job.result, job.err, job.finished = result, err, time.Now()
		t.mu.Unlock()
		close(job.done)
	}()
	return job, nil
}

// stop cancels every running download and waits for them, up to ctx. Nothing starts afterwards.
func (t *platformMarketStageJobs) stop(ctx context.Context) {
	t.mu.Lock()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Unlock()
	ended := make(chan struct{})
	go func() { t.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-ctx.Done():
	}
}

// requesters returns the sessions of the requests behind a job.
func (t *platformMarketStageJobs) requesters(job *platformMarketStageJob) []platformActorSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]platformActorSession(nil), job.actors...)
}

// view returns the job of a release. A finished job, failed or not, stays readable until it expires or a new
// request replaces it, so every poller sees the same answer.
func (t *platformMarketStageJobs) view(sha string) (platformMarketStageView, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, j := range t.jobs {
		if j.sha != sha {
			continue
		}
		if !j.finished.IsZero() && time.Since(j.finished) > platformMarketStageKeep {
			delete(t.jobs, k)
			return platformMarketStageView{}, false
		}
		return platformMarketStageView{job: j, running: j.finished.IsZero(), result: j.result, err: j.err}, true
	}
	return platformMarketStageView{}, false
}

// stopPlatformStageJobs cancels the downloads at once and waits for them for what is left of the shutdown's
// time (ctx), so a job is gone before the store closes and the whole stop stays inside the orchestrator's grace.
// Without a deadline it waits at most 5 seconds.
func (s *Server) stopPlatformStageJobs(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	s.platformStageJobs.stop(ctx)
}

func platformStagedBody(staged platformStagedRelease) map[string]any {
	return map[string]any{"release_sha256": staged.sha256, "app_id": staged.appID, "publisher_id": staged.publisherID, "trust_revision": strconv.FormatInt(staged.trustRevision, 10), "reused": staged.reused}
}

// downloading is the status of a job that has not finished: bytes held so far out of the announced size, which
// is empty until the server has announced one.
func (j *platformMarketStageJob) downloading() map[string]any {
	var done int64
	if info, err := os.Stat(j.store.partialPath(j.key)); err == nil {
		done = info.Size()
	}
	total := ""
	if n := j.progress.total.Load(); n > 0 {
		total = strconv.FormatInt(n, 10)
	}
	return map[string]any{"state": "downloading", "release_sha256": j.sha, "bytes_done": strconv.FormatInt(done, 10), "bytes_total": total}
}

func platformStageFailure(sha string, err error) map[string]any {
	_, code := platformManagementStatus(err)
	return map[string]any{"state": "failed", "release_sha256": sha, "error_code": code}
}

func (s *Server) apiPlatformMarketStageStatus(w http.ResponseWriter, r *http.Request, user string) {
	query := r.URL.Query()
	digest := query.Get("release_sha256")
	if len(query) != 1 || len(query["release_sha256"]) != 1 || !platformValidSHA(digest) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if v, ok := s.platformStageJobs.view(digest); ok {
		switch {
		case v.running:
			writeJSON(w, v.job.downloading())
		case v.err != nil:
			writeJSON(w, platformStageFailure(digest, v.err))
		default:
			body := platformStagedBody(v.result)
			body["state"] = "staged"
			writeJSON(w, body)
		}
		return
	}
	// No job in memory: the release is staged or unknown. Reading it needs the actor's current authority.
	staged := platformStagedRelease{sha256: digest, reused: true}
	err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, _ platformActorFacts, _ platformActorControl, _ appplatform.PublisherTrust) error {
		return e.queryRow(`SELECT app_id,publisher_id,trust_revision FROM app_platform_releases WHERE sha256=?`, digest).Scan(&staged.appID, &staged.publisherID, &staged.trustRevision)
	})
	if errors.Is(err, sql.ErrNoRows) {
		jsonErrorCode(w, http.StatusNotFound, "app_platform_stage_unknown", "no staging is known for this release")
		return
	}
	if err != nil {
		platformManagementError(w, err)
		return
	}
	body := platformStagedBody(staged)
	body["state"] = "staged"
	writeJSON(w, body)
}
