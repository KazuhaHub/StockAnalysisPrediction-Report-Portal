package app

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

var bootstrapBlock = regexp.MustCompile(`<script type="application/json" id="rp-bootstrap">(.*?)</script>`)

func bootstrapShell(t *testing.T, source func() map[string]any) (body, csp string) {
	t.Helper()
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><html><head><title>` + defaultSiteTitle + `</title></head><body><div id="root"></div></body></html>`)}}
	rec := httptest.NewRecorder()
	spaHandlerFSWithBootstrap(fsys, nil, "test", source).ServeHTTP(rec, httptest.NewRequest("GET", "/manage/apps", nil))
	return rec.Body.String(), rec.Header().Get("Content-Security-Policy")
}

// The public site settings ride in the shell, so the SPA paints its first frame branded without asking for them.
func TestSpaEmbedsTheSiteBootstrapAsAnInertJSONBlock(t *testing.T) {
	body, _ := bootstrapShell(t, func() map[string]any { return map[string]any{"siteTitle": "智研平台", "pwaEnabled": true} })
	match := bootstrapBlock.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no bootstrap block in the shell: %q", body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(match[1]), &got); err != nil || got["siteTitle"] != "智研平台" || got["pwaEnabled"] != true {
		t.Fatalf("the bootstrap does not carry the settings: %v %v", got, err)
	}
	// It sits in the head, before the body the SPA mounts into.
	if strings.Index(body, `id="rp-bootstrap"`) > strings.Index(body, "</head>") {
		t.Fatalf("the bootstrap is not in the head: %q", body)
	}
}

// An administrator's text cannot close the block or open markup: the encoding escapes angle brackets and ampersands.
func TestSpaBootstrapEscapesAdministratorText(t *testing.T) {
	hostile := `</script><img src=x onerror=alert(1)>&"` + " "
	body, _ := bootstrapShell(t, func() map[string]any { return map[string]any{"siteTitle": hostile, "footerText": hostile} })
	if strings.Count(body, "</script>") != 1 || strings.Contains(body, "<img src=x") {
		t.Fatalf("the bootstrap was not escaped: %q", body)
	}
	match := bootstrapBlock.FindStringSubmatch(body)
	var got map[string]any
	if match == nil || json.Unmarshal([]byte(match[1]), &got) != nil || got["siteTitle"] != hostile {
		t.Fatalf("the escaped bootstrap does not decode back to the text: %v", match)
	}
}

// Only what the public /api/site serves is embedded, from the very same source: nothing per user, nothing secret.
func TestSpaBootstrapIsTheServersPublicSiteSettingsAndNothingElse(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	public := s.siteSettingsJSON()
	body, _ := bootstrapShell(t, s.siteSettingsJSON)
	match := bootstrapBlock.FindStringSubmatch(body)
	var got map[string]any
	if match == nil || json.Unmarshal([]byte(match[1]), &got) != nil {
		t.Fatalf("no bootstrap: %q", body)
	}
	if len(got) != len(public) {
		t.Fatalf("the bootstrap has %d keys, the public site settings %d", len(got), len(public))
	}
	for key := range got {
		if _, ok := public[key]; !ok {
			t.Fatalf("the bootstrap carries %q, which /api/site does not", key)
		}
	}
	lower := strings.ToLower(match[1])
	for _, secret := range []string{"password", "secret", "token", "key\":", "dek", "session"} {
		if strings.Contains(lower, secret) {
			t.Fatalf("the bootstrap mentions %q", secret)
		}
	}
}

// An inert data block needs no policy change: scripts stay same-origin and no inline execution or nonce is allowed.
func TestSpaBootstrapLeavesTheContentSecurityPolicyAlone(t *testing.T) {
	withBlock, csp := bootstrapShell(t, func() map[string]any { return map[string]any{"siteTitle": "x"} })
	_, without := bootstrapShell(t, nil)
	if withBlock == "" || csp == "" || csp != without {
		t.Fatalf("the policy changed with the bootstrap:\n%s\n%s", csp, without)
	}
	script := regexp.MustCompile(`script-src[^;]*`).FindString(csp)
	if strings.Contains(script, "unsafe-inline") || strings.Contains(script, "nonce-") || strings.Contains(script, "unsafe-eval") {
		t.Fatalf("script-src allows inline execution: %s", script)
	}
}
