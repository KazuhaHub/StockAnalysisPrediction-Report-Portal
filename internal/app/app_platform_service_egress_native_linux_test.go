//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// platformNativeServiceIPCRoot is the socket root of the runner service the
// native acceptance started; the test that starts it sets it.
var platformNativeServiceIPCRoot string

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
func platformServiceEgressAppCall(t *testing.T, socket, method, path, body string) (int, string) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestPlatformServiceEgressNativeClientProcess$")
	child.Env = append(os.Environ(),
		platformServiceEgressClientSocket+"="+socket, platformServiceEgressClientMethod+"="+method,
		platformServiceEgressClientPath+"="+path, platformServiceEgressClientBody+"="+body)
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
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

func testNativePlatformServiceEgress(t *testing.T, s *Server, installation string, endpoint platformRuntimeEndpoint) {
	t.Helper()
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") != "1" || platformNativeServiceIPCRoot == "" {
		t.Fatal("actual HTTPS fixture and runner socket root are mandatory")
	}
	ctx := context.Background()
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	socket := filepath.Join(platformNativeServiceIPCRoot, installation, strconv.FormatInt(generation, 10), "broker", "broker.sock")
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
	if status, body := platformServiceEgressAppCall(t, socket, "GET", "/v1/status", ""); status != 200 || !strings.Contains(body, `"egress":"available"`) {
		t.Fatal("broker status", status, body)
	}
	before := platformNetworkFixtureCountUpTo(t, platformServiceEgressFixtureHitCap)
	request := func(path string) (int, string) {
		raw, _ := json.Marshal(map[string]string{"requestId": "native-" + strconv.FormatInt(time.Now().UnixNano(), 36), "method": "GET", "url": "https://api.example.com" + path, "accept": "application/json", "contentType": "", "bodyBase64": ""})
		return platformServiceEgressAppCall(t, socket, "POST", "/v1/network/http", string(raw))
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
		return platformServiceEgressAppCall(t, socket, "POST", "/v1/notifications", string(raw))
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
