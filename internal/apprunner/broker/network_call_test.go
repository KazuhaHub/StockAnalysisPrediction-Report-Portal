package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func signedNetworkFixture(t *testing.T, h *HTTP, body []byte) ([]byte, []byte, control.Authority) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(public)
	a := control.Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(fingerprint[:])}
	policy, err := appplatform.CanonicalHTTPPolicy(h.policy)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParseHTTPPolicy(policy)
	digest := sha256.Sum256(body)
	now := time.Now().UnixMilli()
	c := control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, Method: "POST", URL: h.policy.Origin + "/v1/check", BodyBytes: strconv.Itoa(len(body)), BodySHA256: hex.EncodeToString(digest[:]), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	raw, sig, err := control.SignNetworkHTTPRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, a
}

func TestSignedNetworkCallRealTLSAndSingleUse(t *testing.T) {
	var remote, checks atomic.Int32
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		remote.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{}` || r.Method != "POST" || r.URL.Path != "/v1/check" {
			t.Error("signed payload changed before dispatch")
		}
		_, _ = w.Write([]byte("ok"))
	})
	body := []byte(`{}`)
	raw, sig, a := signedNetworkFixture(t, h, body)
	call, err := NewNetworkCall(h, raw, sig, body, a, func(_ context.Context, c control.NetworkHTTPRequestCommand) (control.Authority, error) {
		checks.Add(1)
		if c.ActorScope != strings.Repeat("b", 64) || c.Generation != "1" {
			t.Fatal("lost binding")
		}
		return a, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body[0] = 'x'
	raw[0] ^= 1
	sig[0] ^= 1
	got, err := call.Do(context.Background())
	if err != nil || string(got.Body) != "ok" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = call.Do(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("reused", err)
	}
	if remote.Load() != 1 || checks.Load() != 3 {
		t.Fatalf("remote=%d checks=%d", remote.Load(), checks.Load())
	}
}

func TestSignedNetworkCallRechecksCurrentAuthorityAndExpiry(t *testing.T) {
	for _, mode := range []string{"before_dns", "before_dispatch", "after_io", "rotation", "expired_after_io"} {
		t.Run(mode, func(t *testing.T) {
			var remote, checks atomic.Int32
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { remote.Add(1); _, _ = w.Write([]byte("private")) })
			raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
			call, err := NewNetworkCall(h, raw, sig, []byte(`{}`), a, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
				n := checks.Add(1)
				if mode == "before_dns" && n == 1 || mode == "before_dispatch" && n == 2 || mode == "after_io" && n == 3 {
					return control.Authority{}, errors.New("private authority error")
				}
				if mode == "rotation" {
					changed := a
					changed.ControlRevision++
					return changed, nil
				}
				return a, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired_after_io" {
				call.now = func() time.Time {
					now := time.Now()
					if checks.Load() == 3 {
						return now.Add(31 * time.Second)
					}
					return now
				}
			}
			got, err := call.Do(context.Background())
			expected := ErrDenied
			expectedRemote := int32(0)
			if mode == "after_io" || mode == "expired_after_io" {
				expected = ErrUncertain
				expectedRemote = 1
			}
			if !errors.Is(err, expected) || got.Status != 0 || len(got.Body) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("%+v %v", got, err)
			}
			if remote.Load() != expectedRemote {
				t.Fatal("unexpected dispatch", remote.Load())
			}
			if _, err = call.Do(context.Background()); !errors.Is(err, ErrDenied) || remote.Load() != expectedRemote {
				t.Fatal("retry allowed")
			}
		})
	}
}

func TestSignedNetworkCallCannotBroadenExecutorOrChangePayload(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	current := func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil }
	if _, err := NewNetworkCall(h, raw, sig, []byte(`[]`), a, current); !errors.Is(err, ErrDenied) {
		t.Fatal("changed body")
	}
	p := testPolicy()
	p.ResponseBytes--
	different, _ := NewHTTP(p, func(context.Context) error { return nil })
	if _, err := NewNetworkCall(different, raw, sig, []byte(`{}`), a, current); !errors.Is(err, ErrDenied) {
		t.Fatal("different executor policy")
	}
	if _, err := NewNetworkCall(h, raw, sig, []byte(`{}`), a, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("missing current authority")
	}
	if _, err := (&NetworkCall{}).Do(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("zero call")
	}
}

func TestSignedNetworkCallConcurrentUseDispatchesOnce(t *testing.T) {
	var remote atomic.Int32
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { remote.Add(1); _, _ = w.Write([]byte("ok")) })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	call, err := NewNetworkCall(h, raw, sig, []byte(`{}`), a, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, err := call.Do(context.Background()); done <- err }()
	}
	close(start)
	passed, denied := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		if err == nil {
			passed++
		} else if errors.Is(err, ErrDenied) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || denied != 1 || remote.Load() != 1 {
		t.Fatalf("pass=%d denied=%d remote=%d", passed, denied, remote.Load())
	}
}
