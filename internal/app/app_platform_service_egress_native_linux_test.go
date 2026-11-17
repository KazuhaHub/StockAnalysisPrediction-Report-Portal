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
	"syscall"
	"testing"
	"time"
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

// platformServiceIPCRootEnv carries the socket root of the runner service from
// the acceptance parent (which starts the service) to the Portal child process
// (which runs this test). A package variable would not cross that process
// boundary, so the value is passed explicitly: parent environment, child
// environment, then a parameter of every function down to the test.
const platformServiceIPCRootEnv = "PORTAL_SERVICE_IPC_ROOT"

// platformChildServiceEnv is what the parent adds to the Portal child's
// environment; without the service there is nothing to add.
func platformChildServiceEnv(useService bool, ipcRoot string) []string {
	if !useService {
		return nil
	}
	return []string{platformServiceIPCRootEnv + "=" + ipcRoot}
}

// platformServiceIPCRootFromEnv is the child's side of it: the root is absolute
// and clean, or there is none.
func platformServiceIPCRootFromEnv(getenv func(string) string) string {
	root := getenv(platformServiceIPCRootEnv)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ""
	}
	return root
}

// platformServiceEgressBrokerSocket is the application broker socket the runner
// serves for one installation generation.
func platformServiceEgressBrokerSocket(ipcRoot, installation string, generation int64) string {
	return filepath.Join(platformServiceEgressBrokerDir(ipcRoot, installation, generation), "broker.sock")
}

func platformServiceEgressBrokerDir(ipcRoot, installation string, generation int64) string {
	return filepath.Join(ipcRoot, installation, strconv.FormatInt(generation, 10), "broker")
}

// platformServiceEgressTraversalGroups is a TEST-ONLY stand-in for the
// container's read-only bind mount of the broker directory. In production the
// application reaches the socket only through that mount and the broker still
// checks the peer uid (65532); nothing about the host directories changes. The
// Portal child of the acceptance can switch user but cannot mount, so the client
// process instead joins the groups that grant it traversal of every directory on
// the way down to the socket (where other-execute is missing). stat is injected
// so the walk can be tested without root. It fails when a directory grants
// neither: then only the root parent could build a view.
func platformServiceEgressTraversalGroups(stat func(string) (os.FileMode, uint32, error), socket string) ([]uint32, error) {
	var groups []uint32
	dir := "/"
	parts := strings.Split(strings.Trim(filepath.Dir(socket), "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			dir = filepath.Join(dir, parts[i])
		}
		mode, gid, err := stat(dir)
		if err != nil {
			return nil, err
		}
		switch {
		case mode&0o001 != 0:
		case mode&0o010 != 0:
			if !slices.Contains(groups, gid) {
				groups = append(groups, gid)
			}
		default:
			return nil, fmt.Errorf("%s grants traversal to neither group nor other (mode %v)", dir, mode.Perm())
		}
	}
	return groups, nil
}

func platformServiceEgressStat(path string) (os.FileMode, uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("no ownership information for " + path)
	}
	return info.Mode(), st.Gid, nil
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

// platformServiceEgressAppCall runs one broker request as uid 65532 and returns
// the status and body the application saw.
func platformServiceEgressAppCall(t *testing.T, binary string, groups []uint32, socket, method, path, body string) (int, string) {
	t.Helper()
	child := exec.Command(binary, "-test.run=^TestPlatformServiceEgressNativeClientProcess$")
	// Uid 65532 must be able to traverse the working directory too, and the
	// process needs no scratch space or home of its own.
	child.Dir = "/"
	child.Env = append(os.Environ(),
		platformServiceEgressClientSocket+"="+socket, platformServiceEgressClientMethod+"="+method,
		platformServiceEgressClientPath+"="+path, platformServiceEgressClientBody+"="+body)
	if os.Geteuid() == 0 { // the acceptance; an unprivileged dry run keeps its own uid
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532, Groups: groups}}
	}
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("application process: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "RESULT "); ok {
			status, text, _ := strings.Cut(rest, " ")
			code, err := strconv.Atoi(status)
			if err != nil {
				t.Fatal("application process answered", line)
			}
			return code, text
		}
	}
	t.Fatalf("application process printed no result:\n%s", out)
	return 0, ""
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

func testNativePlatformServiceEgress(t *testing.T, s *Server, installation string, endpoint platformRuntimeEndpoint, ipcRoot string) {
	t.Helper()
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") != "1" {
		t.Fatal("the actual HTTPS fixture is mandatory: PORTAL_NETWORK_NAMESPACE is not 1")
	}
	if ipcRoot == "" {
		t.Fatal("the runner socket root is mandatory: " + platformServiceIPCRootEnv + " was not passed to this process")
	}
	ctx := context.Background()
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	clientBinary := platformServiceEgressClientBinary(t)
	// The installation declares no background.run (the foreground fixture does not
	// need it); give it the row the reviewed selection would, for this test only.
	// Everything the test changes is put back by a cleanup that runs however the
	// test ends: the original network.http state, the background.run row, and the
	// notification events it captured.
	grantWas := scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	eventsBefore := map[string]bool{}
	for _, id := range platformServiceEgressEventIDs(t, s) {
		eventsBefore[id] = true
	}
	var withdrawal <-chan error
	t.Cleanup(func() {
		if withdrawal != nil {
			select {
			case <-withdrawal:
			case <-time.After(5 * time.Second):
			}
		}
		mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=? WHERE installation_id=? AND capability='network.http'`, grantWas, installation)
		mustExec(t, s.st, `DELETE FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run'`, installation)
		for _, id := range platformServiceEgressEventIDs(t, s) {
			if eventsBefore[id] {
				continue
			}
			mustExec(t, s.st, `DELETE FROM notification_attempts WHERE delivery_id IN (SELECT id FROM notification_deliveries WHERE event_id=?)`, id)
			mustExec(t, s.st, `DELETE FROM notification_deliveries WHERE event_id=?`, id)
			mustExec(t, s.st, `DELETE FROM notification_recipients WHERE event_id=?`, id)
			mustExec(t, s.st, `DELETE FROM notification_events WHERE id=?`, id)
		}
	})
	review := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	policy := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	stamp := scalar[int64](t, s.st, `SELECT created_ms FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,'background.run',1,'{}',?,?,?,?)`, installation, policy, review, stamp, stamp)
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
	socket := platformServiceEgressBrokerSocket(ipcRoot, installation, generation)
	groups, err := platformServiceEgressTraversalGroups(platformServiceEgressStat, socket)
	if err != nil {
		t.Fatal("the application user cannot reach the broker socket even through groups:", err)
	}
	if status, body := platformServiceEgressAppCall(t, clientBinary, groups, socket, "GET", "/v1/status", ""); status != 200 || !strings.Contains(body, `"egress":"available"`) {
		t.Fatal("broker status", status, body)
	}
	before := platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap)
	request := func(path string) (int, string) {
		raw, _ := json.Marshal(map[string]string{"requestId": "native-" + strconv.FormatInt(time.Now().UnixNano(), 36), "method": "GET", "url": "https://api.example.com" + path, "accept": "application/json", "contentType": "", "bodyBase64": ""})
		return platformServiceEgressAppCall(t, clientBinary, groups, socket, "POST", "/v1/network/http", string(raw))
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
		return platformServiceEgressAppCall(t, clientBinary, groups, socket, "POST", "/v1/notifications", string(raw))
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
}

// The plumbing of the runner socket root from the acceptance parent to the
// Portal child is checked here, without root: what the parent puts in the
// child's environment is what the child reads back, and a missing or unsafe
// value reads as none (which the native test refuses with a message naming it).
func TestPlatformServiceEgressNativeRootPlumbing(t *testing.T) {
	env := platformChildServiceEnv(true, "/run/portal-test/ipc")
	if len(env) != 1 || env[0] != "PORTAL_SERVICE_IPC_ROOT=/run/portal-test/ipc" {
		t.Fatal("child environment", env)
	}
	if platformChildServiceEnv(false, "/run/portal-test/ipc") != nil {
		t.Fatal("a child without the service was given a root")
	}
	lookup := func(values ...string) func(string) string {
		child := map[string]string{}
		for _, kv := range values {
			k, v, _ := strings.Cut(kv, "=")
			child[k] = v
		}
		return func(k string) string { return child[k] }
	}
	if got := platformServiceIPCRootFromEnv(lookup(env...)); got != "/run/portal-test/ipc" {
		t.Fatal("the child did not read back the root", got)
	}
	for _, bad := range []string{"", "relative/ipc", "/run/portal-test/../ipc", "/run/portal-test/ipc/"} {
		if got := platformServiceIPCRootFromEnv(lookup("PORTAL_SERVICE_IPC_ROOT=" + bad)); got != "" {
			t.Fatal("unsafe root accepted", bad, got)
		}
	}
	if got := platformServiceEgressBrokerSocket("/run/portal-test/ipc", "inst", 3); got != "/run/portal-test/ipc/inst/3/broker/broker.sock" {
		t.Fatal("broker socket", got)
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
	binary := platformServiceEgressClientBinary(t)
	if status, body := platformServiceEgressAppCall(t, binary, nil, socket, "GET", "/v1/status", ""); status != 200 || !strings.Contains(body, `"egress":"available"`) {
		t.Fatal("status through the client process", status, body)
	}
	status, body := platformServiceEgressAppCall(t, binary, nil, socket, "POST", "/v1/network/http", `{}`)
	if status != 502 || platformServiceEgressErrorCode(body) != "uncertain" {
		t.Fatal("error through the client process", status, body)
	}
}

// The group walk, with a fake stat: groups are collected only where other-execute
// is missing, deduplicated, and a directory that grants neither is an error.
func TestPlatformServiceEgressTraversalGroups(t *testing.T) {
	dirs := map[string]struct {
		mode os.FileMode
		gid  uint32
	}{
		"/": {os.ModeDir | 0755, 0}, "/run": {os.ModeDir | 0755, 0},
		"/run/svc": {os.ModeDir | 0750, 41001}, "/run/svc/ipc": {os.ModeDir | 0750, 41001},
		"/run/svc/ipc/i": {os.ModeDir | 0710, 41002}, "/run/svc/ipc/i/1": {os.ModeDir | 0755, 0},
	}
	stat := func(p string) (os.FileMode, uint32, error) {
		d, ok := dirs[p]
		if !ok {
			return 0, 0, os.ErrNotExist
		}
		return d.mode, d.gid, nil
	}
	got, err := platformServiceEgressTraversalGroups(stat, "/run/svc/ipc/i/1/broker.sock")
	if err != nil || len(got) != 2 || got[0] != 41001 || got[1] != 41002 {
		t.Fatal("groups", got, err)
	}
	dirs["/run/svc/ipc"] = struct {
		mode os.FileMode
		gid  uint32
	}{os.ModeDir | 0700, 0}
	if _, err := platformServiceEgressTraversalGroups(stat, "/run/svc/ipc/i/1/broker.sock"); err == nil {
		t.Fatal("an owner-only directory was accepted")
	}
	if _, err := platformServiceEgressTraversalGroups(stat, "/run/svc/ipc/i/missing/broker.sock"); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}
