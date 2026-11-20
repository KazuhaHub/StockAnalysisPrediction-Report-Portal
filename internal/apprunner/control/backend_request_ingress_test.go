package control

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The runner's structural check of a request's assertion understands an ingress one (doc 180), and
// refuses everything an ingress token must not carry.
func TestBackendRequestDataAcceptsIngressAssertionsOnlyForTheirRequest(t *testing.T) {
	signer := protocol.Signer{Issuer: "portal.app/d", Audience: "portal.app/i", AppID: "status", InstanceID: "inst", BootID: "boot", KeyID: "k", Key: []byte(strings.Repeat("k", 32))}
	const path = "/v1/ingress/heartbeat"
	body := []byte(`{"timestamp":1,"nonce":"3f9c0000000000000000"}`)
	token, err := appplatform.SignIngressAssertion(signer, 2, "heartbeat", "api", path, time.Now(), "3f9c0000000000000000", body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check := func(boot, method, target string, b []byte, assertion string) error {
		return validateBackendRequestData(boot, method, target, sha(b), sha([]byte(assertion)), b, assertion)
	}
	if err := check("boot", "POST", path, body, token); err != nil {
		t.Fatal("an ingress request was refused", err)
	}
	for name, err := range map[string]error{
		"other boot":   check("other", "POST", path, body, token),
		"other path":   check("boot", "POST", "/v1/ingress/other", body, token),
		"other body":   check("boot", "POST", path, []byte(`{"timestamp":2,"nonce":"3f9c0000000000000000"}`), token),
		"read":         check("boot", "GET", path, nil, token),
		"no body":      check("boot", "POST", path, nil, token),
		"viewer space": check("boot", "POST", "/v1/documents", body, token),
		"digest":       validateBackendRequestData("boot", "POST", path, sha(body), sha([]byte("x")), body, token),
		"viewer token": check("boot", "POST", path, body, "abc.def"),
	} {
		if err == nil {
			t.Fatal("accepted:", name)
		}
	}
}
