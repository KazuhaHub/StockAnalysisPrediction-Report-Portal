//go:build linux

package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const egrCredentialRequest = `{"requestId":"req-1","method":"GET","url":"https://api.example.com/v1/x","accept":"application/json","contentType":"","bodyBase64":"","credential":"upstream"}`

var egrCredentialResources = []string{"network.http", "notifications.publish", "secrets.use"}

// Portal places the credential: it is the only party that knows the value, so
// the command it signs carries it, and the runner checks it was asked for.
func egrPlaceCredential(name, header, value string) func(*control.NetworkHTTPRequestCommand) {
	return func(c *control.NetworkHTTPRequestCommand) {
		c.CredentialName, c.CredentialHeader, c.CredentialValue = name, header, []byte(value)
	}
}

func TestServiceEgressAdmitCredentialCanonical(t *testing.T) {
	base := EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", Accept: "application/json", BodySHA256: strings.Repeat("a", 64)}
	plain, err := EncodeEgressAdmit(base)
	if err != nil || strings.Contains(string(plain), "credential") {
		t.Fatal("an admit without a credential changed shape", string(plain), err)
	}
	named := base
	named.Credential = "deepseek"
	raw, err := EncodeEgressAdmit(named)
	if err != nil || !strings.HasSuffix(string(raw), `"credential":"deepseek"}`) {
		t.Fatal("credential not last in the admit", string(raw), err)
	}
	if got, err := DecodeEgressAdmit(raw); err != nil || got != named {
		t.Fatal("admit roundtrip", err)
	}
	for _, name := range []string{"Upper", "1x", "a_b", strings.Repeat("a", 65), " a"} {
		bad := base
		bad.Credential = name
		if _, err := EncodeEgressAdmit(bad); err == nil {
			t.Fatal("accepted credential name", name)
		}
	}
	// An empty name is the absent field, never a second spelling of it.
	if _, err := DecodeEgressAdmit([]byte(strings.Replace(string(plain), `"bodySha256"`, `"credential":"","bodySha256"`, 1))); err == nil {
		t.Fatal("accepted an empty credential member")
	}
}

func TestServiceEgressCredentialRequestReachesPortalAsAName(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrCredentialResources})
	p.mu.Lock()
	p.mutate = egrPlaceCredential("upstream", "Authorization", "Bearer s3cret-token-value")
	p.mu.Unlock()
	var seen control.NetworkHTTPRequestCommand
	egrHooks.Store(f, egrBackgroundHook(func(_ context.Context, c control.NetworkHTTPRequestCommand) { seen = c }))
	t.Cleanup(func() { egrHooks.Delete(f) })
	reply := f.network(t, egrCredentialRequest)
	if reply.status != 200 || strings.Contains(reply.body, "s3cret-token-value") {
		t.Fatalf("reply %d %s", reply.status, reply.body)
	}
	select {
	case admit := <-p.admits:
		if admit.Credential != "upstream" {
			t.Fatalf("Portal was not told the binding: %+v", admit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no admit")
	}
	if seen.CredentialName != "upstream" || seen.CredentialHeader != "Authorization" || string(seen.CredentialValue) != "Bearer s3cret-token-value" {
		t.Fatal("the executor did not receive the credential Portal placed")
	}
	// A call without a credential still carries none.
	seen = control.NetworkHTTPRequestCommand{}
	p.mu.Lock()
	p.mutate = nil
	p.mu.Unlock()
	if r := f.network(t, egrGetRequest); r.status != 200 || seen.CredentialName != "" {
		t.Fatalf("plain call %d", r.status)
	}
}

func TestServiceEgressCredentialNeedsSecretsUseInTheSession(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	egrError(t, f.network(t, egrCredentialRequest), 403, "denied")
	select {
	case admit := <-p.admits:
		t.Fatalf("a credential request reached Portal from a session without secrets.use: %+v", admit)
	case <-time.After(200 * time.Millisecond):
	}
	if f.executed.Load() != 0 {
		t.Fatal("executed")
	}
	// The session is intact for calls that name no credential.
	if r := f.network(t, egrGetRequest); r.status != 200 {
		t.Fatalf("session did not survive: %+v", r)
	}
}

func TestServiceEgressCredentialCommandMustMatchTheAdmit(t *testing.T) {
	for name, c := range map[string]struct {
		request string
		mutate  func(*control.NetworkHTTPRequestCommand)
	}{
		"other binding name":           {egrCredentialRequest, egrPlaceCredential("other", "Authorization", "Bearer s3cret-token-value")},
		"credential asked, none given": {egrCredentialRequest, nil},
		"credential given, none asked": {egrGetRequest, egrPlaceCredential("upstream", "Authorization", "Bearer s3cret-token-value")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			p := f.open(egrOptions{resources: egrCredentialResources})
			p.mu.Lock()
			p.mutate = c.mutate
			p.mu.Unlock()
			reply := f.network(t, c.request)
			egrError(t, reply, 403, "denied")
			if f.executed.Load() != 0 || strings.Contains(reply.body, "s3cret") {
				t.Fatal("a mismatched command executed or leaked")
			}
		})
	}
}
