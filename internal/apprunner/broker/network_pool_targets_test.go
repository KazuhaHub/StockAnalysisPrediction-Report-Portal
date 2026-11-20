package broker

import (
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

func signedPolicyFixture(t *testing.T, policy []byte, privateSHA, url string, body []byte) ([]byte, []byte, control.Authority) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(public)
	a := control.Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(fingerprint[:])}
	parsed, err := appplatform.ParseHTTPPolicyAny(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	now := time.Now().UnixMilli()
	c := control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, PrivateNetworksSHA256: privateSHA, Method: "POST", URL: url, BodyBytes: strconv.Itoa(len(body)), BodySHA256: hex.EncodeToString(digest[:]), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	raw, sig, err := control.SignNetworkHTTPRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, a
}

func policyBytes(t *testing.T, p appplatform.HTTPPolicyV2) []byte {
	t.Helper()
	raw, err := appplatform.CanonicalHTTPPolicyV2(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func allowSHA(t *testing.T, n []appplatform.PrivateNetwork) string {
	t.Helper()
	sum, err := appplatform.PrivateNetworksSHA256(n)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func allowAll(a control.Authority) CurrentNetworkAuthority {
	return func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil }
}

func TestNetworkPoolServesAPrivateV2Policy(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}
	policy := targetPolicy(privateTarget("lan", "https://10.20.0.5:8443"), publicTarget("pub", "https://api.example.com"))
	policy.Concurrency = 1
	raw := policyBytes(t, policy)
	f := newTargetFixture(t, policy, nets, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte("ok"))
	})
	cmd, sig, a := signedPolicyFixture(t, raw, allowSHA(t, nets), "https://10.20.0.5:8443/v1/check", []byte(`{}`))
	pool, err := NewNetworkPool(2, 2, allowAll(a), WithPrivateNetworks(nets))
	if err != nil {
		t.Fatal(err)
	}
	var created atomic.Int32
	pool.createPolicy = func(got []byte, gotNets []appplatform.PrivateNetwork) (*HTTP, error) {
		created.Add(1)
		if string(got) != string(raw) || len(gotNets) != 1 || gotNets[0].Prefix != "10.20.0.0/24" {
			t.Errorf("executor built from other inputs: %s %v", got, gotNets)
		}
		return f.h, nil
	}
	response, err := pool.Do(context.Background(), cmd, sig, []byte(`{}`), a)
	if err != nil || response.Status != 200 || string(response.Body) != "ok" {
		t.Fatalf("%+v %v", response, err)
	}
	if created.Load() != 1 || f.dialedAddress() != "10.20.0.5:8443" {
		t.Fatalf("created=%d dialed=%q", created.Load(), f.dialedAddress())
	}
}

func TestNetworkPoolDeniesAllowlistDigestMismatch(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}
	policy := targetPolicy(privateTarget("lan", "https://10.20.0.5:8443"))
	raw := policyBytes(t, policy)
	other := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443, 9443)}
	cmd, sig, a := signedPolicyFixture(t, raw, allowSHA(t, other), "https://10.20.0.5:8443/v1/check", []byte(`{}`))
	var created atomic.Int32
	for name, option := range map[string][]NetworkPoolOption{"different allowlist": {WithPrivateNetworks(nets)}, "empty allowlist": nil} {
		pool, err := NewNetworkPool(2, 2, allowAll(a), option...)
		if err != nil {
			t.Fatal(err)
		}
		pool.createPolicy = func([]byte, []appplatform.PrivateNetwork) (*HTTP, error) { created.Add(1); return nil, ErrDenied }
		if _, err := pool.Do(context.Background(), cmd, sig, []byte(`{}`), a); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: %v", name, err)
		}
		if len(pool.entries) != 0 || len(pool.slots) != 0 {
			t.Fatalf("%s: state left behind", name)
		}
	}
	// An empty allowlist cannot satisfy a command that names the digest of the empty list either.
	empty, _ := appplatform.PrivateNetworksSHA256(nil)
	cmd, sig, a = signedPolicyFixture(t, raw, empty, "https://10.20.0.5:8443/v1/check", []byte(`{}`))
	pool, _ := NewNetworkPool(2, 2, allowAll(a))
	pool.createPolicy = func([]byte, []appplatform.PrivateNetwork) (*HTTP, error) { created.Add(1); return nil, ErrDenied }
	if _, err := pool.Do(context.Background(), cmd, sig, []byte(`{}`), a); !errors.Is(err, ErrDenied) {
		t.Fatalf("private policy against an empty allowlist: %v", err)
	}
	if created.Load() != 0 {
		t.Fatal("an executor was created for a refused command")
	}
}

func TestNetworkPoolRejectsInvalidAllowlistOption(t *testing.T) {
	_, a := func() (struct{}, control.Authority) {
		_, _, a := signedPolicyFixture(t, policyBytes(t, targetPolicy(publicTarget("pub", "https://api.example.com"))), "", "https://api.example.com/v1/x", []byte(`{}`))
		return struct{}{}, a
	}()
	if _, err := NewNetworkPool(1, 1, allowAll(a), WithPrivateNetworks([]appplatform.PrivateNetwork{lan("8.8.8.0/24", false, 443)})); !errors.Is(err, ErrDenied) {
		t.Fatalf("a public prefix reached the pool: %v", err)
	}
}

func TestNetworkPoolBudgetIsSharedAcrossV1AndV2(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	rawV1, sigV1, a := signedNetworkFixture(t, h, []byte(`{}`))
	policy := targetPolicy(publicTarget("pub", "https://api.example.com"))
	policy.Concurrency = 1
	rawV2, sigV2, aV2 := signedPolicyFixture(t, policyBytes(t, policy), "", "https://api.example.com/v1/check", []byte(`{}`))
	pool, _ := NewNetworkPool(4, 4, allowAll(a))
	v1, err := control.VerifyNetworkHTTPRequest(rawV1, sigV1, a, pool.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	v2, err := control.VerifyNetworkHTTPRequest(rawV2, sigV2, aV2, pool.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if v1.Command().InstallationID != v2.Command().InstallationID || v1.Command().DeploymentID != v2.Command().DeploymentID {
		t.Fatal("fixtures are not for one installation")
	}
	pool.create = func(HTTPPolicy) (*HTTP, error) { return h, nil }
	pool.createPolicy = func([]byte, []appplatform.PrivateNetwork) (*HTTP, error) { return h, nil }
	first, err := pool.acquire(v1.Command())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.acquire(v2.Command()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("v2 multiplied the installation budget held by v1: %v", err)
	}
	first.release()
	second, err := pool.acquire(v2.Command())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.acquire(v1.Command()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("v1 multiplied the installation budget held by v2: %v", err)
	}
	second.release()
	if len(pool.entries) != 2 {
		t.Fatalf("executors are not kept per policy: %d", len(pool.entries))
	}
}

func TestNetworkCallComparesRawPolicyBytesAndAllowlist(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}
	policy := targetPolicy(privateTarget("lan", "https://10.20.0.5:8443"))
	raw := policyBytes(t, policy)
	f := newTargetFixture(t, policy, nets, true, ok)
	cmd, sig, a := signedPolicyFixture(t, raw, allowSHA(t, nets), "https://10.20.0.5:8443/v1/check", []byte(`{}`))
	current := allowAll(a)
	call, err := NewNetworkCall(f.h, cmd, sig, []byte(`{}`), a, current)
	if err != nil {
		t.Fatal(err)
	}
	response, err := call.Do(context.Background())
	if err != nil || response.Status != 200 {
		t.Fatalf("%+v %v", response, err)
	}
	// The same policy served by an executor with another allowlist is refused.
	other := newTargetFixture(t, policy, []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443, 9443)}, true, ok)
	if _, err := NewNetworkCall(other.h, cmd, sig, []byte(`{}`), a, current); !errors.Is(err, ErrDenied) {
		t.Fatalf("allowlist mismatch: %v", err)
	}
	// An executor built from a different policy (v1) is refused for a v2 command.
	v1, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	if _, err := NewNetworkCall(v1, cmd, sig, []byte(`{}`), a, current); !errors.Is(err, ErrDenied) {
		t.Fatalf("policy mismatch: %v", err)
	}
	// A v2 command whose policy differs by one narrowing limit is a different policy.
	narrower := policy
	narrower.Targets = []appplatform.HTTPTarget{privateTarget("lan", "https://10.20.0.5:8443")}
	narrower.Targets[0].RequestBytes = 8
	cmd2, sig2, a2 := signedPolicyFixture(t, policyBytes(t, narrower), allowSHA(t, nets), "https://10.20.0.5:8443/v1/check", []byte(`{}`))
	if _, err := NewNetworkCall(f.h, cmd2, sig2, []byte(`{}`), a2, allowAll(a2)); !errors.Is(err, ErrDenied) {
		t.Fatalf("narrowed policy served by the wide executor: %v", err)
	}
}
