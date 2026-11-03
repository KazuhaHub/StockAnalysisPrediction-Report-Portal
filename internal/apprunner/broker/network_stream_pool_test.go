package broker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func signedNetworkStreamPoolFixture(t *testing.T, policy appplatform.HTTPStreamPolicy) ([]byte, []byte, []byte, control.Authority) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(public)
	a := control.Authority{
		DeploymentID:    "10000000-0000-4000-8000-000000000001",
		ControllerEpoch: "20000000-0000-4000-8000-000000000001",
		ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(fingerprint[:]),
	}
	rawPolicy, err := appplatform.CanonicalHTTPStreamPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseHTTPStreamPolicy(rawPolicy)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	digest := sha256.Sum256(body)
	now := time.Now().UnixMilli()
	command := control.NetworkHTTPRequestCommand{
		Format:       control.NetworkHTTPStreamRequestFormat,
		DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1",
		RequestID:      "30000000-0000-4000-8000-000000000001",
		InstallationID: "40000000-0000-4000-8000-000000000001",
		ReviewPlanID:   "50000000-0000-4000-8000-000000000001",
		Generation:     "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64),
		ActorScope: strings.Repeat("b", 64), PolicyRaw: rawPolicy, PolicySHA256: parsed.SHA256,
		Method: "GET", URL: "https://stream.example.com/v1/stream", Accept: "application/x-ndjson",
		BodyBytes: "2", BodySHA256: hex.EncodeToString(digest[:]),
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10),
	}
	raw, signature, err := control.SignNetworkHTTPStreamRequest(command, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature, body, a
}

func TestNetworkStreamPoolUsesSharedBudgetAndCurrentAuthority(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var remote, created atomic.Int32
	executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		remote.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.(http.Flusher).Flush()
		close(entered)
		<-release
		_, _ = w.Write([]byte("done\n"))
	})
	raw, signature, body, a := signedNetworkStreamPoolFixture(t, streamPolicyFixture())
	var checks atomic.Int32
	pool, err := NewNetworkStreamPool(2, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		checks.Add(1)
		return a, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.create = func(appplatform.HTTPStreamPolicy) (*HTTPStream, error) {
		created.Add(1)
		return executor, nil
	}
	callbacks := HTTPStreamCallbacks{
		Start: func(int, string) error { return nil },
		Chunk: func([]byte) error { return nil },
	}
	done := make(chan error, 1)
	go func() {
		_, err := pool.Do(context.Background(), raw, signature, body, a, nil, callbacks)
		done <- err
	}()
	<-entered
	if _, err := pool.Do(context.Background(), raw, signature, body, a, nil, callbacks); err == nil {
		t.Fatal("per-installation stream budget was bypassed")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if remote.Load() != 1 || created.Load() != 1 || checks.Load() < 4 {
		t.Fatalf("remote=%d created=%d checks=%d", remote.Load(), created.Load(), checks.Load())
	}
}

func TestNetworkStreamPoolRevokesBeforeAllocatingAndAfterChunks(t *testing.T) {
	executor, _, _ := streamFixture(t, streamPolicyFixture(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("private\n"))
	})
	raw, signature, body, a := signedNetworkStreamPoolFixture(t, streamPolicyFixture())
	revoked, err := NewNetworkStreamPool(1, 1, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		return control.Authority{}, context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	revoked.create = func(appplatform.HTTPStreamPolicy) (*HTTPStream, error) {
		t.Fatal("revoked stream allocated executor")
		return nil, nil
	}
	if _, err := revoked.Do(context.Background(), raw, signature, body, a, nil, HTTPStreamCallbacks{Start: func(int, string) error { return nil }}); err == nil || len(revoked.entries) != 0 {
		t.Fatal("pre-dispatch revocation allocated an executor")
	}
	var checks atomic.Int32
	streaming, err := NewNetworkStreamPool(1, 1, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		if checks.Add(1) == 4 {
			return control.Authority{}, context.Canceled
		}
		return a, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	streaming.create = func(appplatform.HTTPStreamPolicy) (*HTTPStream, error) { return executor, nil }
	var emitted bytes.Buffer
	_, err = streaming.Do(context.Background(), raw, signature, body, a, nil, HTTPStreamCallbacks{
		Start: func(int, string) error { return nil },
		Chunk: func(data []byte) error {
			emitted.Write(data)
			return nil
		},
	})
	if !errors.Is(err, ErrUncertain) || strings.Contains(emitted.String(), "private") {
		t.Fatalf("post-dispatch revocation released data: %q %v", emitted.String(), err)
	}
}

func TestNetworkStreamPoolRefusesMismatchedBodyBeforeExecutor(t *testing.T) {
	raw, signature, body, a := signedNetworkStreamPoolFixture(t, streamPolicyFixture())
	_ = body
	pool, err := NewNetworkStreamPool(1, 1, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	pool.create = func(appplatform.HTTPStreamPolicy) (*HTTPStream, error) {
		t.Fatal("mismatched body allocated executor")
		return nil, nil
	}
	if _, err := pool.Do(context.Background(), raw, signature, []byte(`[]`), a, nil, HTTPStreamCallbacks{Start: func(int, string) error { return nil }}); err == nil {
		t.Fatal("mismatched body accepted")
	}
	if len(pool.entries) != 0 || len(pool.slots) != 0 {
		t.Fatal("refused stream retained resources")
	}
}
