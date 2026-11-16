package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Email service page sends a blank recipient and relies on the server to address
// the test to the signed-in admin; a blank recipient with no address on file is refused.
func TestEmailTestBlankRecipientUsesSignedInAccount(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "admin"})
	s.st.SetUserProfile("kaz", "Kazuha", "kaz@x.io")
	s.st.UpsertUser(User{Username: "nomail", PasswordHash: "hash1", Role: "admin"})
	sent := enableEmail(s)
	send := func(user, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiEmailTest(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), user)
		return rec
	}

	if rec := send("kaz", `{"to":"  "}`); rec.Code != http.StatusOK {
		t.Fatalf("blank recipient → %d %s", rec.Code, rec.Body.String())
	}
	if m := wantEmail(t, sent); !strings.HasPrefix(m, "kaz@x.io\n") {
		t.Errorf("test email = %q, want it addressed to kaz@x.io", m)
	}
	if rec := send("kaz", `{"to":"other@x.io"}`); rec.Code != http.StatusOK {
		t.Fatalf("explicit recipient → %d", rec.Code)
	}
	if m := wantEmail(t, sent); !strings.HasPrefix(m, "other@x.io\n") {
		t.Errorf("explicit recipient ignored: %q", m)
	}
	rec := send("nomail", `{"to":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank recipient without an address on file → %d, want 400", rec.Code)
	}
	// The SPA translates the reason through err.<code>; a bare message would reach every locale in English.
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "no_recipient" {
		t.Fatalf("no-recipient body = %s, want code no_recipient", rec.Body.String())
	}
	wantNoEmail(t, sent)
}
