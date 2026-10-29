//go:build linux

package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Reuses only the disposable root identity fixture and executable. The real pool
// must ask its non-root controller before DNS; refusal produces no network use.
func testNetworkControllerIdentityBoundary(t *testing.T, root, binary, endpoint string) {
	t.Helper()
	raw, sig, body, a, _ := networkSessionFixture(t)
	dir := filepath.Join(root, "network-journal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, _, err := store.EnrollController(context.Background(), journal.ControllerEnrollment{DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: a.ControlRevision, PublicKey: a.PublicKey, Fingerprint: a.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := filepath.Join(endpoint, "network.sock")
	server, err := ListenNetworkController(ctx, path, controllerTestUID, controllerTestGID, store, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		server.network.mu.Lock()
		defer server.network.mu.Unlock()
		if len(server.network.sessions) != 0 {
			t.Error("network session survived shutdown")
		}
	}()
	authority, _ := json.Marshal(a)
	run := func(uid, gid uint32, mode string) {
		t.Helper()
		cmd := exec.Command(binary, "-test.run=^TestNetworkControllerIdentityChild$", "-test.timeout=5s")
		cmd.Env = []string{"GOMAXPROCS=2", "NETWORK_IDENTITY_CHILD=" + mode, "NETWORK_SOCKET=" + path, "NETWORK_PACKET=" + base64.RawStdEncoding.EncodeToString(raw), "NETWORK_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(sig), "NETWORK_BODY=" + base64.RawStdEncoding.EncodeToString(body), "NETWORK_AUTHORITY=" + base64.RawStdEncoding.EncodeToString(authority)}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("network identity child %s (%d:%d): %v\n%s", mode, uid, gid, err, out)
		}
	}
	run(41002, controllerTestGID, "peer_refused")
	run(controllerTestUID, 41003, "dac_refused")
	run(controllerTestUID, controllerTestGID, "controller_denied")
	after, err := store.ControllerDeployment(ctx)
	if err != nil || after.Revision != before.Revision || after.Fingerprint != before.Fingerprint {
		t.Fatal("network request mutated enrollment", err)
	}
	t.Log("NETWORK_CONTROLLER_PEER_DAC_AND_PRE_DNS_DENIAL_VERIFIED")
}

func TestNetworkControllerIdentityChild(t *testing.T) {
	mode := os.Getenv("NETWORK_IDENTITY_CHILD")
	if mode == "" {
		t.Skip("disposable controller identity subprocess only")
	}
	decode := func(name string) []byte {
		value, err := base64.RawStdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			t.Fatal("public fixture decode")
		}
		return value
	}
	var a control.Authority
	if json.Unmarshal(decode("NETWORK_AUTHORITY"), &a) != nil {
		t.Fatal("authority fixture")
	}
	calls := 0
	observed, err := RequestControllerNetwork(context.Background(), os.Getenv("NETWORK_SOCKET"), uint32(os.Geteuid()), controllerTestGID, decode("NETWORK_PACKET"), decode("NETWORK_SIGNATURE"), decode("NETWORK_BODY"), a, func(context.Context, control.NetworkHTTPRequestCommand, string) ([]byte, []byte, error) {
		calls++
		return nil, nil, errors.New("fixture current grant denied")
	})
	if err == nil || !observed.ReceivedAt().IsZero() || len(observed.Response().Body) != 0 {
		t.Fatal("denied network produced an observation")
	}
	if mode == "controller_denied" {
		if calls != 1 || !errors.Is(err, broker.ErrUncertain) {
			t.Fatalf("controller denial did not cross root channel: calls=%d err=%v", calls, err)
		}
	} else if calls != 0 {
		t.Fatal("foreign peer reached controller checks")
	}
}
