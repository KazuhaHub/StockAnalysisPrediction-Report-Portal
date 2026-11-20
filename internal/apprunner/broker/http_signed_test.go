package broker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const (
	signedKeyID     = "LTAIbrokerfixture"
	signedKeySecret = "broker-fixture-secret"
	signedToken     = "STS.broker-fixture-token-0123456789"
)

// signedHeadersFor is what Portal would send for a request: ACS3 headers computed with the fixture key.
func signedHeadersFor(t *testing.T, method, target, contentType string, body []byte, token string) []appplatform.SignedHeader {
	t.Helper()
	sum := sha256.Sum256(body)
	headers, err := appplatform.ACS3Sign(appplatform.ACS3Input{
		Method: method, Target: target, ContentType: contentType, BodySHA256: hex.EncodeToString(sum[:]),
		Action: "QueryAccountBalance", Version: "2017-12-14", Date: time.Now().UTC(),
		Nonce: strings.Repeat("0f", 16), AccessKeyID: signedKeyID, SecurityToken: []byte(token),
	}, []byte(signedKeySecret))
	if err != nil {
		t.Fatal(err)
	}
	return headers
}

// verifyACS3Upstream is the provider's side, written independently of package appplatform: it rebuilds
// the canonical request from what arrived on the wire and checks the signature with the fixture secret.
func verifyACS3Upstream(r *http.Request, body []byte) string {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "ACS3-HMAC-SHA256 Credential="+signedKeyID+",SignedHeaders=")
	if !ok {
		return "bad authorization prefix: " + auth
	}
	names, signature, ok := strings.Cut(rest, ",Signature=")
	if !ok {
		return "bad authorization shape"
	}
	var canonical strings.Builder
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical.WriteString(r.Method + "\n" + path + "\n" + r.URL.RawQuery + "\n")
	list := strings.Split(names, ";")
	if !sort.StringsAreSorted(list) {
		return "signed headers not sorted"
	}
	for _, name := range list {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		if value == "" {
			return "signed header " + name + " absent"
		}
		canonical.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
	}
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Acs-Content-Sha256") != hex.EncodeToString(sum[:]) {
		return "body hash differs from x-acs-content-sha256"
	}
	canonical.WriteString("\n" + names + "\n" + hex.EncodeToString(sum[:]))
	hashed := sha256.Sum256([]byte(canonical.String()))
	mac := hmac.New(sha256.New, []byte(signedKeySecret))
	mac.Write([]byte("ACS3-HMAC-SHA256\n" + hex.EncodeToString(hashed[:])))
	if hex.EncodeToString(mac.Sum(nil)) != signature {
		return "signature mismatch"
	}
	for _, forbidden := range []string{"Cookie", "X-Api-Key"} {
		if r.Header.Get(forbidden) != "" {
			return "unexpected header " + forbidden
		}
	}
	return ""
}

func signedNetworkCommandFixture(t *testing.T, h *HTTP, body []byte, token string) ([]byte, []byte, control.Authority) {
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
	c := control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, Method: "POST", URL: h.policy.Origin + "/v1/check", ContentType: "application/json", BodyBytes: strconv.Itoa(len(body)), BodySHA256: hex.EncodeToString(digest[:]), CredentialName: "qwen-billing", SigningScheme: appplatform.ACS3Scheme, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	c.SetSignedHeaders(signedHeadersFor(t, c.Method, c.URL, c.ContentType, body, token))
	raw, sig, err := control.SignNetworkHTTPRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, a
}

func TestSignedNetworkCallReachesTheUpstreamWithAVerifiableSignature(t *testing.T) {
	for _, token := range []string{"", signedToken} {
		var hits atomic.Int32
		h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			body, _ := io.ReadAll(r.Body)
			if problem := verifyACS3Upstream(r, body); problem != "" {
				t.Errorf("upstream refused the request: %s", problem)
				w.WriteHeader(403)
				return
			}
			if (r.Header.Get("X-Acs-Security-Token") != "") != (token != "") {
				t.Errorf("security token header = %q for token %q", r.Header.Get("X-Acs-Security-Token"), token)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balance":"12.34"}`))
		})
		body := []byte(`{}`)
		raw, sig, a := signedNetworkCommandFixture(t, h, body, token)
		call, err := NewNetworkCall(h, raw, sig, body, a, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
		if err != nil {
			t.Fatal(err)
		}
		got, err := call.Do(context.Background())
		if err != nil || got.Status != 200 || string(got.Body) != `{"balance":"12.34"}` || hits.Load() != 1 {
			t.Fatalf("token %q: %+v %v hits=%d", token, got, err, hits.Load())
		}
	}
}

func TestHTTPRefusesSignedHeadersThatAreNotExactlyWhatPortalSigned(t *testing.T) {
	body := []byte(`{}`)
	const target = "https://api.example.com/v1/check"
	good := func(t *testing.T) []appplatform.SignedHeader {
		return signedHeadersFor(t, "POST", target, "application/json", body, signedToken)
	}
	for name, mutate := range map[string]func(*HTTPRequest){
		"an extra header name": func(r *HTTPRequest) {
			r.SignedHeaders = append(r.SignedHeaders, appplatform.SignedHeader{Name: "x-forwarded-for", Value: []byte("1.2.3.4")})
		},
		"a body that differs from x-acs-content-sha256": func(r *HTTPRequest) { r.Body = []byte(`{"a":1}`) },
		"more than nine pairs": func(r *HTTPRequest) {
			r.SignedHeaders = append(r.SignedHeaders, r.SignedHeaders[:2]...)
		},
		"a credential header beside":  func(r *HTTPRequest) { r.HeaderName, r.HeaderValue = "X-API-Key", []byte("k") },
		"a different content type":    func(r *HTTPRequest) { r.ContentType = "text/plain" },
		"a host other than the URL's": func(r *HTTPRequest) { r.URL = strings.Replace(r.URL, "api.example.com", "api.example.com:444", 1) },
		"a missing authorization": func(r *HTTPRequest) {
			var kept []appplatform.SignedHeader
			for _, h := range r.SignedHeaders {
				if h.Name != "authorization" {
					kept = append(kept, h)
				}
			}
			r.SignedHeaders = kept
		},
	} {
		var hits atomic.Int32
		h, l, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
		request := HTTPRequest{Method: "POST", URL: target, ContentType: "application/json", Body: bytes.Clone(body), SignedHeaders: good(t)}
		mutate(&request)
		if _, err := h.Do(context.Background(), request); err == nil || l.Load() != 0 || hits.Load() != 0 {
			t.Errorf("%s: err=%v lookups=%d hits=%d", name, err, l.Load(), hits.Load())
		}
	}
}

// A response that gives the security token or the signature back is withheld, in the body and in the
// content type, in the forms the credential echo already recognises.
func TestHTTPWithholdsAResponseThatEchoesTheSecurityTokenOrTheSignature(t *testing.T) {
	body := []byte(`{}`)
	const target = "https://api.example.com/v1/check"
	headers := signedHeadersFor(t, "POST", target, "application/json", body, signedToken)
	authorization := string(appplatform.ACS3HeaderValue(headers, "authorization"))
	signature := authorization[strings.LastIndex(authorization, "=")+1:]
	for name, echoed := range map[string]string{
		"token":         signedToken,
		"signature":     signature,
		"authorization": authorization,
	} {
		for place, write := range map[string]func(http.ResponseWriter){
			"body":         func(w http.ResponseWriter) { _, _ = w.Write([]byte("seen " + echoed)) },
			"content type": func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/plain; x="+echoed) },
		} {
			if place == "content type" && strings.ContainsAny(echoed, ", ") {
				continue
			}
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { write(w); w.WriteHeader(200) })
			got, err := h.Do(context.Background(), HTTPRequest{Method: "POST", URL: target, ContentType: "application/json", Body: body, SignedHeaders: headers})
			if err != ErrUncertain || len(got.Body) != 0 {
				t.Errorf("%s in %s: %+v %v", name, place, got, err)
			}
		}
	}
	// A clean response passes.
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	if got, err := h.Do(context.Background(), HTTPRequest{Method: "POST", URL: target, ContentType: "application/json", Body: body, SignedHeaders: headers}); err != nil || got.Status != 200 {
		t.Fatalf("clean response refused: %+v %v", got, err)
	}
}

// Every copy of the signed header values the pool makes for a call is wiped when the call ends, on the
// foreground path and the background one alike.
func TestNetworkPoolWipesItsCopiesOfTheSignedHeaders(t *testing.T) {
	for name, run := range map[string]func(*NetworkPool, context.Context, []byte, []byte, []byte, control.Authority) (HTTPResponse, error){
		"foreground": (*NetworkPool).Do,
		"background": (*NetworkPool).DoBackground,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("ok"))
			})
			body := []byte(`{}`)
			raw, sig, a := signedNetworkCommandFixture(t, h, body, signedToken)
			var mu sync.Mutex
			var seen [][]byte
			pool, err := NewNetworkPool(2, 2, func(_ context.Context, c control.NetworkHTTPRequestCommand) (control.Authority, error) {
				mu.Lock()
				seen = append(seen, c.SignedHeaderValues...)
				mu.Unlock()
				return a, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			pool.create = func(HTTPPolicy) (*HTTP, error) { return h, nil }
			if _, err := run(pool, context.Background(), raw, sig, body, a); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 {
				t.Fatal("the authority callback never saw signed headers")
			}
			for i, got := range seen {
				if len(got) == 0 || !bytes.Equal(got, make([]byte, len(got))) {
					t.Fatalf("callback copy %d of a signed header survived the call: %q", i, got)
				}
			}
		})
	}
}
