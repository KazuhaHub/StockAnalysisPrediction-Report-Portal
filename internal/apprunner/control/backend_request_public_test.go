package control

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The runner's structural check of a request's assertion understands a public one (doc 179), and
// still refuses everything a public token must not carry.
func TestBackendRequestDataAcceptsPublicAssertionsOnlyForTheirRequest(t *testing.T) {
	signer := protocol.Signer{Issuer: "portal.app/d", Audience: "portal.app/i", AppID: "status", InstanceID: "inst", BootID: "boot", KeyID: "k", Key: []byte(strings.Repeat("k", 32))}
	token, err := appplatform.SignPublicAssertion(signer, 2, "history", "/v1/public/history", "from=1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/public/history?from=1"
	check := func(boot, method, path string, body []byte, assertion string) error {
		return validateBackendRequestData(boot, method, path, sha(body), sha([]byte(assertion)), body, assertion)
	}
	if err := check("boot", "GET", target, nil, token); err != nil {
		t.Fatal("a public request was refused", err)
	}
	for name, err := range map[string]error{
		"other boot":  check("other", "GET", target, nil, token),
		"other query": check("boot", "GET", "/v1/public/history?from=2", nil, token),
		"other path":  check("boot", "GET", "/v1/public/status?from=1", nil, token),
		"no query":    check("boot", "GET", "/v1/public/history", nil, token),
		"write":       check("boot", "POST", target, []byte(`{}`), token),
		"body":        check("boot", "GET", target, []byte(`{}`), token),
		"private":     check("boot", "GET", "/v1/admin?from=1", nil, token),
		"digest":      validateBackendRequestData("boot", "GET", target, sha(nil), sha([]byte("x")), nil, token),
	} {
		if err == nil {
			t.Fatal("accepted:", name)
		}
	}
}
