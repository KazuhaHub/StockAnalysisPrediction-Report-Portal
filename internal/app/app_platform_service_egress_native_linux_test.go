//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Native end-to-end of background broker egress (doc 174 sections 3 and 7: items
// 3, 5, 8 and 14 of the test plan). It runs only inside the disposable root
// acceptance (scripts/acceptance/project_quota_probe.py), after the writable
// installation is active and the isolated HTTPS fixture runs. Portal's real
// coordinator opens a real session on the real runner service; the application's
// side is a process running as the application's user (65532), exactly as the
// container does, speaking portal.broker/1 on the real broker socket.
//
// It emits PORTAL_SERVICE_EGRESS_HTTPS, PORTAL_SERVICE_EGRESS_PRE_IO_REVOKED,
// PORTAL_SERVICE_EGRESS_POST_IO_UNCERTAIN and PORTAL_SERVICE_EGRESS_NOTICE.

// The application half of this test runs in the acceptance PARENT. The Portal
// child is not root and the runner's IPC tree is closed to it, so it cannot reach
// the broker socket (not even to open a descriptor on its directory). It asks the
// parent instead, over two extra pipes (child fds 6 and 7): a request names the
// installation, generation and broker call; the parent (root) runs the uid-65532
// client against that generation's broker socket and answers with what the client
// saw. The child asserts on the answers and prints the PORTAL_SERVICE_EGRESS_*
// markers, which the parent collects from the child's output as for every other
// marker.
const (
	platformServiceEgressChannelEnv     = "PORTAL_SERVICE_EGRESS_CHANNEL"
	platformServiceEgressChannelTimeout = 90 * time.Second
	platformServiceEgressRequestLimit   = 256 << 10
)

type platformServiceEgressRequest struct {
	Installation string `json:"installation"`
	Generation   int64  `json:"generation"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Body         string `json:"body"`
}

type platformServiceEgressResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	Error  string `json:"error,omitempty"`
}

// platformServiceEgressApp is the child's view of the application: one broker
// request, answered by whoever can reach the socket.
type platformServiceEgressApp interface {
	call(r platformServiceEgressRequest) (platformServiceEgressResponse, error)
}

// platformServiceEgressChannel is the child's end of the two pipes.
type platformServiceEgressChannel struct {
	mu      sync.Mutex
	request io.Writer
	reply   *json.Decoder
	timeout time.Duration
}

func newPlatformServiceEgressChannel(request io.Writer, reply io.Reader, timeout time.Duration) *platformServiceEgressChannel {
	return &platformServiceEgressChannel{request: request, reply: json.NewDecoder(io.LimitReader(reply, 64<<20)), timeout: timeout}
}

func (c *platformServiceEgressChannel) call(r platformServiceEgressRequest) (platformServiceEgressResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := json.NewEncoder(c.request).Encode(r); err != nil {
		return platformServiceEgressResponse{}, fmt.Errorf("sending the request to the acceptance parent: %w", err)
	}
	type result struct {
		r   platformServiceEgressResponse
		err error
	}
	done := make(chan result, 1)
	go func() {
		var out platformServiceEgressResponse
		err := c.reply.Decode(&out)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return platformServiceEgressResponse{}, fmt.Errorf("reading the parent's answer: %w", res.err)
		}
		if res.r.Error != "" {
			return res.r, errors.New("the acceptance parent could not run the client: " + res.r.Error)
		}
		return res.r, nil
	case <-time.After(c.timeout):
		return platformServiceEgressResponse{}, fmt.Errorf("the acceptance parent did not answer %s %s within %v", r.Method, r.Path, c.timeout)
	}
}

// platformServiceEgressAppFromChild is the child's side of the plumbing: the
// channel on fds 6 and 7 when the parent opened it, nil otherwise.
func platformServiceEgressAppFromChild() platformServiceEgressApp {
	if os.Getenv(platformServiceEgressChannelEnv) != "1" {
		return nil
	}
	return newPlatformServiceEgressChannel(os.NewFile(6, "egress-request"), os.NewFile(7, "egress-reply"), platformServiceEgressChannelTimeout)
}

var platformServiceEgressAllowedPaths = map[string]string{
	"/v1/status": "GET", "/v1/network/http": "POST", "/v1/notifications": "POST",
}

// servePlatformServiceEgressApp is the parent's side: it answers requests until
// the child closes its end. run is what actually reaches the broker (the real one
// needs root); the requests are validated first, so nothing but the three broker
// routes of a well-formed installation and generation ever reaches it.
func servePlatformServiceEgressApp(requests io.Reader, replies io.Writer, run func(platformServiceEgressRequest) (platformServiceEgressResponse, error)) error {
	decoder := json.NewDecoder(io.LimitReader(requests, 1<<30))
	decoder.DisallowUnknownFields()
	encoder := json.NewEncoder(replies)
	for {
		var r platformServiceEgressRequest
		if err := decoder.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var out platformServiceEgressResponse
		switch {
		case !platformValidUUID(r.Installation) || r.Generation < 1:
			out.Error = "invalid installation or generation"
		case platformServiceEgressAllowedPaths[r.Path] != r.Method || len(r.Body) > platformServiceEgressRequestLimit:
			out.Error = "unsupported request"
		default:
			var err error
			if out, err = run(r); err != nil {
				out = platformServiceEgressResponse{Error: err.Error()}
			}
		}
		if err := encoder.Encode(out); err != nil {
			return err
		}
	}
}

// platformServiceEgressParentRunner is the real run for the parent: the broker
// directory of the generation, checked for search permission by other users (the
// container's uid 65532 has none other), then the client process.
func platformServiceEgressParentRunner(ipcRoot, clientBinary string) func(platformServiceEgressRequest) (platformServiceEgressResponse, error) {
	return func(r platformServiceEgressRequest) (platformServiceEgressResponse, error) {
		dir := platformServiceEgressBrokerDir(ipcRoot, r.Installation, r.Generation)
		info, err := os.Stat(dir)
		if err != nil {
			return platformServiceEgressResponse{}, err
		}
		if !platformServiceEgressClientCanSearch(info.Mode()) {
			return platformServiceEgressResponse{}, fmt.Errorf("uid 65532 may not search the broker directory %s (mode %v); the container's user could not either", dir, info.Mode())
		}
		status, body, err := platformServiceEgressRunClient(clientBinary, dir, r.Method, r.Path, r.Body)
		if err != nil {
			return platformServiceEgressResponse{}, err
		}
		return platformServiceEgressResponse{Status: status, Body: body}, nil
	}
}

func platformServiceEgressBrokerDir(ipcRoot, installation string, generation int64) string {
	return filepath.Join(ipcRoot, installation, strconv.FormatInt(generation, 10), "broker")
}

// The application reaches the broker socket through its container's read-only
// bind mount of the broker directory; the broker checks the peer uid (65532),
// and the host directories above it are closed to that user. This test is a
// TEST-ONLY stand-in for the mount: the Portal child (which can open root-only
// paths and switch user, but cannot mount) opens the generation's broker
// directory as an O_PATH descriptor and hands it to the uid-65532 client as fd 3,
// which dials /proc/self/fd/3/broker.sock. Resolving through that magic link
// skips the search permission of the parent directories (the same technique the
// driver uses to bind through /proc/self/fd/N), so only the broker directory's
// own permissions and the socket's matter, exactly as through the mount.
const platformServiceEgressClientDirSocket = "/proc/self/fd/3/broker.sock"

// platformServiceEgressClientCanSearch says whether other users (uid 65532 is
// neither owner nor in the group) may search the broker directory.
func platformServiceEgressClientCanSearch(mode os.FileMode) bool {
	return mode.IsDir() && mode&0o001 != 0
}

// The foreground acceptance reads up to 10 hits of the HTTPS fixture; the egress
// calls that follow it add 3 more (the fixture is shared, so its counter is not
// reset), which only this test is allowed to see.
const platformServiceEgressFixtureHitCap = 16

const (
	platformServiceEgressClientSocket = "PORTAL_SERVICE_EGRESS_CLIENT_SOCKET"
	platformServiceEgressClientMethod = "PORTAL_SERVICE_EGRESS_CLIENT_METHOD"
	platformServiceEgressClientPath   = "PORTAL_SERVICE_EGRESS_CLIENT_PATH"
	platformServiceEgressClientBody   = "PORTAL_SERVICE_EGRESS_CLIENT_BODY"
)

// TestPlatformServiceEgressNativeClientProcess is the application's side of the
// native acceptance: one request on the broker socket, its status and body
// printed. In an ordinary run the environment is empty and it does nothing.
func TestPlatformServiceEgressNativeClientProcess(t *testing.T) {
	socket := os.Getenv(platformServiceEgressClientSocket)
	if socket == "" {
		return
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 40 * time.Second}
	request, err := http.NewRequest(os.Getenv(platformServiceEgressClientMethod), "http://broker"+os.Getenv(platformServiceEgressClientPath), strings.NewReader(os.Getenv(platformServiceEgressClientBody)))
	if err != nil {
		t.Fatal(err)
	}
	if request.Body != nil && os.Getenv(platformServiceEgressClientBody) != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		fmt.Println("RESULT 0 " + err.Error())
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	fmt.Printf("RESULT %d %s\n", response.StatusCode, bytes.TrimSpace(body))
}

// platformServiceEgressRunClient runs one broker request as uid 65532 and
// returns the status and body the application saw.
func platformServiceEgressRunClient(binary, brokerDir, method, path, body string) (int, string, error) {
	fd, err := unix.Open(brokerDir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, "", fmt.Errorf("opening the broker directory for the application: %w", err)
	}
	dir := os.NewFile(uintptr(fd), brokerDir)
	defer dir.Close()
	child := exec.Command(binary, "-test.run=^TestPlatformServiceEgressNativeClientProcess$")
	// Uid 65532 must be able to traverse the working directory too, and the
	// process needs no scratch space or home of its own.
	child.Dir = "/"
	child.ExtraFiles = []*os.File{dir} // fd 3 in the client
	child.Env = append(os.Environ(),
		platformServiceEgressClientSocket+"="+platformServiceEgressClientDirSocket, platformServiceEgressClientMethod+"="+method,
		platformServiceEgressClientPath+"="+path, platformServiceEgressClientBody+"="+body)
	if os.Geteuid() == 0 { // the acceptance; an unprivileged dry run keeps its own uid
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	}
	out, err := child.CombinedOutput()
	if err != nil {
		return 0, "", fmt.Errorf("application process: %w\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "RESULT "); ok {
			status, text, _ := strings.Cut(rest, " ")
			code, err := strconv.Atoi(status)
			if err != nil {
				return 0, "", errors.New("application process answered " + line)
			}
			return code, text, nil
		}
	}
	return 0, "", fmt.Errorf("application process printed no result:\n%s", out)
}

// platformServiceEgressClientBinary installs a copy of this test binary where
// uid 65532 can execute it (root-owned, 0555, in a 0755 directory under /tmp):
// the one the acceptance runs may sit in a directory only root can enter.
func platformServiceEgressClientBinary(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "portal-egress-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(dir, "client.test")
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func platformServiceEgressErrorCode(body string) string {
	var v struct {
		Error     string `json:"error"`
		Retryable bool   `json:"retryable"`
	}
	if json.Unmarshal([]byte(body), &v) != nil || v.Retryable {
		return ""
	}
	return v.Error
}

func platformServiceEgressEventIDs(t *testing.T, s *Server) []string {
	t.Helper()
	rows, err := s.st.query(`SELECT id FROM notification_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func testNativePlatformServiceEgress(t *testing.T, s *Server, installation string, endpoint platformRuntimeEndpoint, app platformServiceEgressApp) {
	t.Helper()
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") != "1" {
		t.Fatal("the actual HTTPS fixture is mandatory: PORTAL_NETWORK_NAMESPACE is not 1")
	}
	if app == nil {
		t.Fatal("the acceptance parent's application channel is mandatory: " + platformServiceEgressChannelEnv + " is not 1 in this process")
	}
	ctx := context.Background()
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	appCall := func(method, path, body string) (int, string) {
		t.Helper()
		out, err := app.call(platformServiceEgressRequest{Installation: installation, Generation: generation, Method: method, Path: path, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return out.Status, out.Body
	}
	// The installation declares no background.run (the foreground fixture does not
	// need it); give it the row the reviewed selection would, for this test only.
	// The steps that follow this one (ancestor OU access, policy changes, more
	// launches) check the launch capability snapshot, which an extra granted
	// capability would break, so everything the test changes is put back right at
	// the end of this function, and again by a cleanup if the test ends early.
	state := platformServiceEgressCaptureState(t, s, installation)
	var withdrawal <-chan error
	t.Cleanup(func() {
		if err := state.restore(s, installation, withdrawal); err != nil {
			t.Error("restoring the installation after the egress test:", err)
		}
	})
	state.addBackgroundRun(t, s, installation)
	// The fixture app (notes-writer-notify-demo) declares secrets.use but requires
	// no setup binding, so nothing is missing; if that ever changes this says so
	// here instead of as an opaque "no session" later.
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	if required := platformRequiredSetupBindings(appID); len(required) != 0 {
		t.Fatal("the fixture app requires setup bindings the test does not provide", appID, required)
	}
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{}); err != nil || !a.hasNetwork() {
		t.Fatal("the fixture installation has no loadable egress authority", err)
	} else {
		a.Close()
	}

	// Before any session the socket is not there to be connected to, or answers
	// unavailable: the application never sees an error it could mistake for upstream.
	coordinator := newPlatformServiceEgressCoordinator(s, newPlatformServiceEgressRunner())
	coordinator.endpoint = func() (platformRuntimeEndpoint, bool) { return endpoint, true }
	defer coordinator.shutdown()
	coordinator.tick(ctx)
	live := coordinator.current(installation)
	if live == nil || live.ended() {
		t.Fatal("the coordinator opened no session on the real runner")
	}
	if status, body := appCall("GET", "/v1/status", ""); status != 200 || !strings.Contains(body, `"egress":"available"`) {
		t.Fatal("broker status", status, body)
	}
	before := platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap)
	request := func(path string) (int, string) {
		raw, _ := json.Marshal(map[string]string{"requestId": "native-" + strconv.FormatInt(time.Now().UnixNano(), 36), "method": "GET", "url": "https://api.example.com" + path, "accept": "application/json", "contentType": "", "bodyBase64": ""})
		return appCall("POST", "/v1/network/http", string(raw))
	}
	status, body := request("/v1/success")
	if status != 200 || !strings.Contains(body, `"status":200`) || platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap) != before+1 {
		t.Fatal("background HTTPS through the broker", status, body)
	}
	t.Log("PORTAL_SERVICE_EGRESS_HTTPS")

	// Revoked before any IO: the application sees a refusal and upstream sees nothing.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	status, body = request("/v1/success")
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if code := platformServiceEgressErrorCode(body); status == 200 || (code != "denied" && code != "unavailable") || platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap) != before+1 {
		t.Fatal("a withdrawn grant reached upstream", status, body)
	}
	t.Log("PORTAL_SERVICE_EGRESS_PRE_IO_REVOKED")
	// Revoked after the request reached upstream: uncertain, never a body.
	cancel, done := beginPlatformNetworkFixtureWithdrawal(t, s, installation)
	withdrawal = done
	defer cancel()
	status, body = request("/v1/revoke")
	cancel()
	withdrawalErr := <-done
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if withdrawalErr != nil || status != 502 || platformServiceEgressErrorCode(body) != "uncertain" || strings.Contains(body, `"ok"`) || platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap) != before+2 {
		t.Fatal("post-IO withdrawal released a response or was retried", withdrawalErr, status, body)
	}
	t.Log("PORTAL_SERVICE_EGRESS_POST_IO_UNCERTAIN")

	// A notice is one frame to Portal, captured once, idempotent, and a changed
	// replay is a conflict.
	if cur := coordinator.current(installation); cur == nil || cur.ended() {
		t.Fatal("no session for the notice")
	}
	events := scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`)
	notice := func(title string) (int, string) {
		raw, _ := json.Marshal(map[string]string{"occurrenceId": "native-egress-1", "title": title, "message": "Through the broker", "severity": "info"})
		return appCall("POST", "/v1/notifications", string(raw))
	}
	status, body = notice("Native notice")
	if status != 200 || !strings.Contains(body, `"created":true`) {
		t.Fatal("notice through the broker", status, body)
	}
	if status, body = notice("Native notice"); status != 200 || !strings.Contains(body, `"created":false`) {
		t.Fatal("notice replay", status, body)
	}
	if status, body = notice("Changed notice"); status != 409 || platformServiceEgressErrorCode(body) != "conflict" {
		t.Fatal("notice conflict", status, body)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events`) != events+1 {
		t.Fatal("a replayed or conflicting notice captured an event")
	}
	t.Log("PORTAL_SERVICE_EGRESS_NOTICE")

	// Closing the session leaves the socket answering unavailable.
	coordinator.shutdown()
	if status, body := request("/v1/success"); status == 200 || platformServiceEgressErrorCode(body) != "unavailable" {
		t.Fatal("a closed session still served", status, body)
	}
	if opened := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=?`, platformServiceEgressAuditOpened, installation); opened < 1 {
		t.Fatal("no opened audit")
	}
	// Put everything back now, and prove the grants are exactly as they were: the
	// launch capability snapshot compares them with the reviewed selection.
	if err := state.restore(s, installation, withdrawal); err != nil {
		t.Fatal("restoring the installation after the egress test:", err)
	}
}

// platformServiceEgressRestoreState is what the egress test changes in the database and
// how to undo it. restore is idempotent: the first call undoes, later ones only
// re-check.
type platformServiceEgressRestoreState struct {
	grants        []string
	events        map[string]bool
	insertedBgRun bool
	restored      bool
}

func platformServiceEgressGrantRows(t *testing.T, s *Server, installation string) []string {
	t.Helper()
	rows, err := s.st.query(`SELECT capability||'|'||granted||'|'||policy_json||'|'||policy_revision||'|'||review_plan_id||'|'||created_ms||'|'||updated_ms FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func platformServiceEgressCaptureState(t *testing.T, s *Server, installation string) *platformServiceEgressRestoreState {
	t.Helper()
	st := &platformServiceEgressRestoreState{grants: platformServiceEgressGrantRows(t, s, installation), events: map[string]bool{}}
	for _, id := range platformServiceEgressEventIDs(t, s) {
		st.events[id] = true
	}
	return st
}

// addBackgroundRun gives the installation the background.run row the reviewed
// selection would, copied from its network.http grant; restore removes it again.
func (st *platformServiceEgressRestoreState) addBackgroundRun(t *testing.T, s *Server, installation string) {
	t.Helper()
	review := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	policy := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	stamp := scalar[int64](t, s.st, `SELECT created_ms FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,'background.run',1,'{}',?,?,?,?)`, installation, policy, review, stamp, stamp)
	st.insertedBgRun = true
}

// restore waits for the withdrawal goroutine, puts network.http back as it was,
// removes the background.run row it added and the notification rows it caused,
// then compares every grant row of the installation with the captured ones.
func (st *platformServiceEgressRestoreState) restore(s *Server, installation string, withdrawal <-chan error) error {
	if !st.restored {
		if withdrawal != nil {
			select {
			case <-withdrawal:
			case <-time.After(5 * time.Second):
			}
		}
		var was int
		for _, row := range st.grants {
			if fields := strings.Split(row, "|"); fields[0] == "network.http" {
				was, _ = strconv.Atoi(fields[1])
			}
		}
		steps := []struct {
			q    string
			args []any
		}{{`UPDATE app_platform_capability_grants SET granted=? WHERE installation_id=? AND capability='network.http'`, []any{was, installation}}}
		if st.insertedBgRun {
			steps = append(steps, struct {
				q    string
				args []any
			}{`DELETE FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run'`, []any{installation}})
		}
		ids, err := platformServiceEgressEventIDsErr(s)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if st.events[id] {
				continue
			}
			for _, q := range []string{
				`DELETE FROM notification_attempts WHERE delivery_id IN (SELECT id FROM notification_deliveries WHERE event_id=?)`,
				`DELETE FROM notification_deliveries WHERE event_id=?`,
				`DELETE FROM notification_recipients WHERE event_id=?`,
				`DELETE FROM notification_events WHERE id=?`,
			} {
				steps = append(steps, struct {
					q    string
					args []any
				}{q, []any{id}})
			}
		}
		for _, step := range steps {
			if _, err := s.st.exec(step.q, step.args...); err != nil {
				return err
			}
		}
		st.restored = true
	}
	rows, err := s.st.query(`SELECT capability||'|'||granted||'|'||policy_json||'|'||policy_revision||'|'||review_plan_id||'|'||created_ms||'|'||updated_ms FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, installation)
	if err != nil {
		return err
	}
	defer rows.Close()
	var now []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			return err
		}
		now = append(now, row)
	}
	if !slices.Equal(now, st.grants) {
		return fmt.Errorf("grant rows differ from before the test:\nbefore %q\nafter  %q", st.grants, now)
	}
	return nil
}

func platformServiceEgressEventIDsErr(s *Server) ([]string, error) {
	rows, err := s.st.query(`SELECT id FROM notification_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// The request and reply plumbing between the Portal child and the acceptance
// parent, over pipes and without root: a request validated and answered by the
// parent's run function, a rejected request, a run failure, the parent never
// answering (a bounded, clear failure), and the end of the loop at EOF.
func TestPlatformServiceEgressNativeChannelPlumbing(t *testing.T) {
	reqR, reqW, _ := os.Pipe()
	repR, repW, _ := os.Pipe()
	var seen []platformServiceEgressRequest
	served := make(chan error, 1)
	go func() {
		served <- servePlatformServiceEgressApp(reqR, repW, func(r platformServiceEgressRequest) (platformServiceEgressResponse, error) {
			seen = append(seen, r)
			if strings.Contains(r.Body, "boom") {
				return platformServiceEgressResponse{}, errors.New("client failed")
			}
			return platformServiceEgressResponse{Status: 200, Body: `{"ok":true}`}, nil
		})
	}()
	channel := newPlatformServiceEgressChannel(reqW, repR, 5*time.Second)
	id := "11111111-2222-4333-8444-555555555555"
	good := platformServiceEgressRequest{Installation: id, Generation: 1, Method: "POST", Path: "/v1/network/http", Body: "{}"}
	if out, err := channel.call(good); err != nil || out.Status != 200 || out.Body != `{"ok":true}` {
		t.Fatal("answered request", out, err)
	}
	for name, bad := range map[string]platformServiceEgressRequest{
		"installation": {Installation: "../x", Generation: 1, Method: "GET", Path: "/v1/status"},
		"generation":   {Installation: id, Generation: 0, Method: "GET", Path: "/v1/status"},
		"path":         {Installation: id, Generation: 1, Method: "GET", Path: "/v1/other"},
		"method":       {Installation: id, Generation: 1, Method: "GET", Path: "/v1/network/http"},
	} {
		if _, err := channel.call(bad); err == nil {
			t.Fatal("an invalid request was run:", name)
		}
	}
	boom := good
	boom.Body = "boom"
	if _, err := channel.call(boom); err == nil || !strings.Contains(err.Error(), "client failed") {
		t.Fatal("a failing client was not reported", err)
	}
	if len(seen) != 2 {
		t.Fatal("only the valid requests reach the run function", len(seen))
	}
	reqW.Close()
	if err := <-served; err != nil {
		t.Fatal("the serve loop did not end cleanly at EOF", err)
	}
	// A parent that never answers fails the child within its timeout.
	silentReqR, silentReqW, _ := os.Pipe()
	silentRepR, silentRepW, _ := os.Pipe()
	defer silentReqR.Close()
	defer silentRepW.Close()
	silent := newPlatformServiceEgressChannel(silentReqW, silentRepR, 200*time.Millisecond)
	if _, err := silent.call(good); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatal("a silent parent was not reported", err)
	}
	if got := platformServiceEgressBrokerDir("/run/portal-test/ipc", "inst", 3); got != "/run/portal-test/ipc/inst/3/broker" {
		t.Fatal("broker dir", got)
	}
}

// A dry run of the application's side without root or a runner: the copied test
// binary is started as the application process and talks to a stand-in broker
// on a unix socket, so the process plumbing, the result parsing and the error
// code reader are exercised before the acceptance depends on them.
func TestPlatformServiceEgressNativeClientDryRun(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "egress-dry-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"egress":"available"}`) })
	mux.HandleFunc("/v1/network/http", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = io.WriteString(w, `{"error":"uncertain","retryable":false}`)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	if !platformServiceEgressClientCanSearch(os.ModeDir|0711) || platformServiceEgressClientCanSearch(os.ModeDir|0710) || platformServiceEgressClientCanSearch(0644) {
		t.Fatal("search permission check")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	binary := platformServiceEgressClientBinary(t)
	if status, body, err := platformServiceEgressRunClient(binary, dir, "GET", "/v1/status", ""); err != nil || status != 200 || !strings.Contains(body, `"egress":"available"`) {
		t.Fatal("status through the client process", status, body)
	}
	status, body, err := platformServiceEgressRunClient(binary, dir, "POST", "/v1/network/http", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if status != 502 || platformServiceEgressErrorCode(body) != "uncertain" {
		t.Fatal("error through the client process", status, body)
	}
}

// The restore helper puts back exactly what the egress test changes, leaves what
// it did not add, is idempotent, and notices a difference it cannot undo.
func TestPlatformServiceEgressNativeStateRestore(t *testing.T) {
	s, installation := platformServiceEgressFixture(t, false, true)
	// The unit fixture already grants background.run; take it away so the helper
	// adds it the way the native fixture needs.
	mustExec(t, s.st, `DELETE FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run'`, installation)
	state := platformServiceEgressCaptureState(t, s, installation)
	state.addBackgroundRun(t, s, installation)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	now := time.Now().UnixMilli()
	mustExec(t, s.st, `INSERT INTO notification_events(id,source_key,event_type,payload,created_at,expires_at) VALUES('egress-restore-1','egress-restore-1','app.notice','{}',?,?)`, now, now+1000)
	if err := state.restore(s, installation, nil); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM notification_events WHERE id='egress-restore-1'`) != 0 {
		t.Fatal("the captured notification event was kept")
	}
	if err := state.restore(s, installation, nil); err != nil { // idempotent
		t.Fatal("second restore", err)
	}
	// Something the helper never touched is a difference it reports.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=policy_revision+1 WHERE installation_id=? AND capability='notifications.publish'`, installation)
	if err := state.restore(s, installation, nil); err == nil {
		t.Fatal("an unrestorable difference was not reported")
	}
}
