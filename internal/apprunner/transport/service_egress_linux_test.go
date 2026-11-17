//go:build linux

package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// privateDir is a 0700 directory, which is what a private endpoint requires of
// its parent (t.TempDir creates 0755 ones).
func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "egr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// egressEndpoint serves a private control endpoint for the current user with
// the fixture's network provider and, when egress is true, its registry.
func egressEndpoint(t *testing.T, f *egrFixture, egress bool) (string, *Server) {
	t.Helper()
	path := filepath.Join(privateDir(t), "control.sock")
	server, err := Listen(path, uint32(os.Geteuid()), &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	server.network = f.provider
	if egress {
		server.egress = f.registry
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	return path, server
}

func (f *egrFixture) signedSession(t *testing.T) ([]byte, []byte) {
	t.Helper()
	command := f.sessionCommand(egrOptions{})
	raw, sig, err := control.SignServiceEgressSession(command, f.key, f.a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func TestServiceEgressDetachedSessionDoesNotHoldServeSlot(t *testing.T) {
	f := newEgrFixture(t)
	path, server := egressEndpoint(t, f, true)
	raw, sig := f.signedSession(t)
	mux, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, f.a, f.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mux.Run(context.Background()) }()
	t.Cleanup(func() { mux.Close(EgressCloseShutdown) })
	// The session is live: the broker answers on its socket.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r, err := f.request("1", "GET", "/v1/status", "", ""); err == nil && r.status == http.StatusOK && r.body != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never became live")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// And it holds no control-endpoint slot: the endpoint serves the same eight
	// connections it did before the session, however long the session lives.
	deadline = time.Now().Add(5 * time.Second)
	for server.slotsInUse.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a detached session holds %d endpoint slots", server.slotsInUse.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.registry.mu.Lock()
	live := f.registry.live
	f.registry.mu.Unlock()
	if live != 1 {
		t.Fatalf("live sessions %d", live)
	}
	// Eight idle connections fill the endpoint; a ninth is dropped, but the
	// session keeps working throughout, and a profile query still goes through
	// once they close.
	idle := make([]net.Conn, 0, 8)
	for i := 0; i < 8; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		idle = append(idle, c)
	}
	if r, err := f.request("1", "GET", "/v1/status", "", ""); err != nil || r.status != http.StatusOK {
		t.Fatalf("a full endpoint disturbed a live session: %+v %v", r, err)
	}
	for _, c := range idle {
		c.Close()
	}
	// A second open on the same installation replaces the first through the
	// endpoint too, and the replaced session's connection is closed by the registry.
	raw2, sig2 := f.signedSession(t)
	second, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw2, sig2, f.a, f.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = second.Run(context.Background()) }()
	t.Cleanup(func() { second.Close(EgressCloseShutdown) })
	select {
	case <-mux.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced session was not closed")
	}
	// A session the runner refuses never starts: a tampered signature.
	bad := append([]byte(nil), sig2...)
	bad[0] ^= 1
	if _, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw2, bad, f.a, f.descriptor); err == nil {
		t.Fatal("a tampered session opened")
	}
}

func TestServiceEgressProfileQuery(t *testing.T) {
	for active, want := range map[int]int{1: 0, 2: 1, 3: 1, 4: 2, 8: 4} {
		f := newEgrFixture(t)
		provider, err := newNetworkProvider(f.enrollment, active, 2)
		if err != nil {
			t.Fatal(err)
		}
		f.provider = provider
		path, _ := egressEndpoint(t, f, true)
		profile, err := queryServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())))
		if err != nil {
			t.Fatal(active, err)
		}
		if profile != NewServiceEgressProfile(active) || profile.BackgroundActive != want || profile.RatePerMinute != 120 || profile.Burst != 20 ||
			profile.Format != "portal.runner.service-egress-profile/1-dev.1" || profile.SessionFormat != "portal.service-egress-session/1-dev.1" || profile.BrokerFormat != "portal.broker/1" {
			t.Fatalf("active=%d: %+v", active, profile)
		}
		if profile.RetryAfterReply {
			t.Fatal("the runner claims foreground Retry-After frames it does not implement")
		}
	}
	// The codec is closed.
	raw, _ := encodeServiceEgressProfile(NewServiceEgressProfile(4))
	if _, err := ParseServiceEgressProfile(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{``, `{}`, string(raw) + ` `, string(raw[:len(raw)-1]) + `,"extra":1}`, `{"format":"x"}`} {
		if _, err := ParseServiceEgressProfile([]byte(bad)); !errors.Is(err, ErrServiceEgressUnsupported) {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := encodeServiceEgressProfile(ServiceEgressProfile{Format: ServiceEgressProfileFormat, BackgroundActive: 9}); err == nil {
		t.Fatal("an invalid profile was encoded")
	}
}

func TestServiceEgressDisabledOnNarrowConstructors(t *testing.T) {
	f := newEgrFixture(t)
	// A runner with a network pool but no egress registry (every constructor but
	// ListenNetworkManagedController) behaves like a runner that predates egress:
	// it does not answer the capability query or the open frame, it closes.
	path, server := egressEndpoint(t, f, false)
	if server.egress != nil {
		t.Fatal("egress registry present")
	}
	if _, err := queryServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrServiceEgressUnsupported) {
		t.Fatalf("query on a narrow runner: %v", err)
	}
	raw, sig := f.signedSession(t)
	if _, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, f.a, f.descriptor); !errors.Is(err, ErrRefused) {
		t.Fatalf("open on a narrow runner: %v", err)
	}
	if f.binds.Load() != 0 || f.listens.Load() != 0 {
		t.Fatal("a narrow runner bound a socket")
	}
	// The plain endpoint, with no network provider at all, is the same.
	plain := filepath.Join(privateDir(t), "plain.sock")
	plainServer, err := Listen(plain, uint32(os.Geteuid()), &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = plainServer.Serve(ctx) }()
	if _, err := queryServiceEgress(context.Background(), plain, privateProfile(uint32(os.Geteuid()))); !errors.Is(err, ErrServiceEgressUnsupported) {
		t.Fatalf("query on a plain endpoint: %v", err)
	}
}

// The root acceptance: a real root-owned controller endpoint and a real broker
// socket in a root-owned directory, with the client running as the application's
// user (65532), as in the container. It needs root; it runs only in the
// disposable acceptance environment (PORTAL_APP_RUNNER_EGRESS_ACCEPTANCE=1 on a
// GitHub-hosted runner) and skips everywhere else.
func TestServiceEgressEndpointAcceptance(t *testing.T) {
	if path := os.Getenv("PORTAL_SERVICE_EGRESS_DIAL"); path != "" {
		// Child process, running as uid 65532.
		client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}, Timeout: 10 * time.Second}
		resp, err := client.Get("http://broker/v1/status")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		return
	}
	if os.Getenv("PORTAL_APP_RUNNER_EGRESS_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable egress acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("egress acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	f := newEgrFixture(t)
	// The fixture's binder is replaced by one that binds a real socket in a
	// root-owned directory and serves only uid 65532.
	dir, err := os.MkdirTemp("/run", "portal-egress-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "broker.sock")
	f.registry.bind = func(ctx context.Context, v control.VerifiedServiceEgressSession, open EgressOpen, listen bool) (EgressListener, error) {
		if !listen {
			return EgressListener{}, nil
		}
		l, err := net.Listen("unix", socket)
		if err != nil {
			return EgressListener{}, err
		}
		if err := os.Chmod(socket, 0666); err != nil {
			l.Close()
			return EgressListener{}, err
		}
		return EgressListener{Listener: l, PeerOK: egressPeerUID(EgressAppUID)}, nil
	}
	path, _ := egressEndpoint(t, f, true)
	raw, sig := f.signedSession(t)
	mux, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, f.a, f.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mux.Run(context.Background()) }()
	defer mux.Close(EgressCloseShutdown)
	run := func(uid uint32) error {
		child := exec.Command(os.Args[0], "-test.run=^TestServiceEgressEndpointAcceptance$")
		child.Env = append(os.Environ(), "PORTAL_SERVICE_EGRESS_DIAL="+socket)
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		out, err := child.CombinedOutput()
		if err != nil {
			return errors.New(string(out))
		}
		return nil
	}
	if err := run(65532); err != nil {
		t.Fatalf("the application's user was refused: %v", err)
	}
	if err := run(65534); err == nil {
		t.Fatal("another user was served on the broker socket")
	}
}
