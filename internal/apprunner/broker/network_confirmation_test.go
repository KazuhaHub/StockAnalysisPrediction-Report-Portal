package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func confirmationFixture(t *testing.T, raw []byte) (control.NetworkHTTPRequestCommand, ed25519.PrivateKey, control.Authority) {
	t.Helper()
	var c control.NetworkHTTPRequestCommand
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("request fixture")
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(public)
	return c, key, control.Authority{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(sum[:])}
}

func TestNetworkConfirmationUsesFreshNonceForEveryActualIOCheck(t *testing.T) {
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	original, _, _ := signedNetworkFixture(t, h, []byte(`{}`))
	c, key, a := confirmationFixture(t, original)
	raw, sig, err := control.SignNetworkHTTPRequest(c, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	nonces := map[string]bool{}
	var checks, loads atomic.Int32
	current, err := NewConfirmedNetworkAuthority(func(context.Context) (control.Authority, error) { loads.Add(1); return a, nil }, func(ctx context.Context, c control.NetworkHTTPRequestCommand, challenge string) ([]byte, []byte, error) {
		checks.Add(1)
		if len(challenge) != 64 || nonces[challenge] {
			t.Error("challenge reused")
		}
		nonces[challenge] = true
		return control.SignNetworkHTTPCheck(c, challenge, key, a, time.Now().UnixMilli())
	})
	if err != nil {
		t.Fatal(err)
	}
	call, err := NewNetworkCall(h, raw, sig, []byte(`{}`), a, current)
	if err != nil {
		t.Fatal(err)
	}
	got, err := call.Do(context.Background())
	if err != nil || string(got.Body) != "ok" {
		t.Fatalf("%+v %v", got, err)
	}
	if checks.Load() != 3 || loads.Load() != 6 {
		t.Fatalf("confirmations=%d enrollment loads=%d", checks.Load(), loads.Load())
	}
}

func TestNetworkConfirmationRejectsReplayRotationAndPrivateErrors(t *testing.T) {
	for _, mode := range []string{"replay", "wrong_request", "rotation", "private_error", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
			raw, _, _ := signedNetworkFixture(t, h, []byte(`{}`))
			c, key, a := confirmationFixture(t, raw)
			var loads atomic.Int32
			var previous, previousSig []byte
			current, err := NewConfirmedNetworkAuthority(func(context.Context) (control.Authority, error) {
				n := loads.Add(1)
				if mode == "rotation" && n == 2 {
					changed := a
					changed.ControlRevision++
					return changed, nil
				}
				return a, nil
			}, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
				if mode == "private_error" {
					return nil, nil, errors.New("credential database details")
				}
				if mode == "deadline" {
					<-ctx.Done()
					return nil, nil, ctx.Err()
				}
				if mode == "wrong_request" {
					c.ActorScope = strings.Repeat("f", 64)
				}
				if mode == "replay" && previous != nil {
					return previous, previousSig, nil
				}
				r, s, e := control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
				previous, previousSig = r, s
				return r, s, e
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			if mode == "replay" {
				if _, err := current(ctx, c); err != nil {
					t.Fatal("initial confirmation", err)
				}
			}
			if _, err := current(ctx, c); !errors.Is(err, ErrDenied) || strings.Contains(err.Error(), "credential") {
				t.Fatal("unsafe confirmation", err)
			}
		})
	}
	if _, err := NewConfirmedNetworkAuthority(nil, func(context.Context, control.NetworkHTTPRequestCommand, string) ([]byte, []byte, error) {
		return nil, nil, nil
	}); !errors.Is(err, ErrDenied) {
		t.Fatal("nil enrollment")
	}
	if _, err := NewConfirmedNetworkAuthority(func(context.Context) (control.Authority, error) { return control.Authority{}, nil }, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("nil confirmation")
	}
}

func TestNetworkConfirmationRefusesBeforeCallingController(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	raw, _, _ := signedNetworkFixture(t, h, []byte(`{}`))
	c, _, a := confirmationFixture(t, raw)
	var exchanges atomic.Int32
	confirm := func(context.Context, control.NetworkHTTPRequestCommand, string) ([]byte, []byte, error) {
		exchanges.Add(1)
		return nil, nil, nil
	}
	current, _ := NewConfirmedNetworkAuthority(func(context.Context) (control.Authority, error) { return a, nil }, confirm)
	invalid := c
	invalid.PolicyRaw = make([]byte, 8193)
	if _, err := current(context.Background(), invalid); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := current(ctx, c); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	missing, _ := NewConfirmedNetworkAuthority(func(context.Context) (control.Authority, error) { return control.Authority{}, nil }, confirm)
	if _, err := missing(context.Background(), c); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if exchanges.Load() != 0 {
		t.Fatal("untrusted request reached controller")
	}
}
