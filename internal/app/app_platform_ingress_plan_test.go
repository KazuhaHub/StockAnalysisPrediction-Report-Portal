package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// The console's review request names a route for an ingress endpoint and shows the sender's address and
// no secret (doc 180 section 4).
func TestIngressBindingPlanRoute(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		secret := ingressSecret("p")
		credential, err := e.s.changePlatformCredential(e.actor, platformCredentialChange{kind: "create", label: "Sender", value: secret})
		if err != nil {
			t.Fatal(err)
		}
		plan := func(body string) *httptest.ResponseRecorder { return e.admin(t, "POST", "secret-bindings/plan", body) }
		base := `"name":"api","credential_id":"` + credential.ID + `","operation":"ingress_hmac_sha256"`
		for name, body := range map[string]string{
			"no route":         `{` + base + `}`,
			"principals":       `{` + base + `,"ingress_route":"heartbeat","principals":["service"]}`,
			"bad route":        `{` + base + `,"ingress_route":"Heart"}`,
			"route on bearer":  `{"name":"api","credential_id":"` + credential.ID + `","operation":"http_bearer","ingress_route":"heartbeat"}`,
			"undeclared route": `{` + base + `,"ingress_route":"nope"}`,
		} {
			if w := plan(body); w.Code == 200 {
				t.Fatalf("%s was reviewed: %s", name, w.Body.String())
			}
		}
		w := plan(`{` + base + `,"ingress_route":"heartbeat"}`)
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), secret) {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Review map[string]any `json:"review"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Review["ingress_route"] != "heartbeat" || out.Review["ingress_url"] != "/ingress/apps/"+e.id+"/heartbeat/api" || out.Review["operation"] != "ingress_hmac_sha256" {
			t.Fatal(w.Body.String())
		}
	})
}
