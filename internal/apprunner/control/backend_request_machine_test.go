package control

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The runner's structural check understands a machine assertion (doc 183) and refuses everything a
// machine token must not carry, including a viewer token in the machine space.
func TestBackendRequestDataAcceptsMachineAssertionsOnlyForTheirRequest(t *testing.T) {
	signer := protocol.Signer{Issuer: "portal.app/d", Audience: "portal.app/i", AppID: "status", InstanceID: "inst", BootID: "boot", KeyID: "k", Key: []byte(strings.Repeat("k", 32))}
	token, err := appplatform.SignMachineAssertion(signer, 2, "bars", "/v1/machine/bars", "from=1", "abcdefghijkl", "Dify", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/machine/bars?from=1"
	check := func(boot, method, path string, body []byte, assertion string) error {
		return validateBackendRequestData(boot, method, path, sha(body), sha([]byte(assertion)), body, assertion)
	}
	if err := check("boot", "GET", target, nil, token); err != nil {
		t.Fatal("a machine request was refused", err)
	}
	viewer, _ := signer.Sign("GET", target, nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, time.Now())
	public, _ := appplatform.SignPublicAssertion(signer, 1, "history", "/v1/public/history", "", time.Now())
	for name, err := range map[string]error{
		"other boot":      check("other", "GET", target, nil, token),
		"other query":     check("boot", "GET", "/v1/machine/bars?from=2", nil, token),
		"other path":      check("boot", "GET", "/v1/machine/other?from=1", nil, token),
		"write":           check("boot", "POST", target, []byte(`{}`), token),
		"body":            check("boot", "GET", target, []byte(`{}`), token),
		"private":         check("boot", "GET", "/v1/admin?from=1", nil, token),
		"public space":    check("boot", "GET", "/v1/public/history", nil, token),
		"digest":          validateBackendRequestData("boot", "GET", target, sha(nil), sha([]byte("x")), nil, token),
		"viewer in space": check("boot", "GET", target, nil, viewer),
		"public in space": check("boot", "GET", target, nil, public),
	} {
		if err == nil {
			t.Fatal("accepted:", name)
		}
	}
}
