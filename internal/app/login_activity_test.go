package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginActivityReturnsOnlyTheCurrentAccountsSuccessfulLogins(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	s.st.WriteAudit(AuditEntry{Actor: "alice", Action: AuditLogin, TargetType: "user", TargetID: "alice", At: "2026-09-27T17:00:00Z", IP: "198.51.100.7", Detail: `{"method":"password"}`})
	s.st.WriteAudit(AuditEntry{Actor: "alice", Action: AuditLogout, TargetType: "user", TargetID: "alice", At: "2026-09-27T17:01:00Z", IP: "198.51.100.7"})
	s.st.WriteAudit(AuditEntry{Actor: "", Action: AuditLoginFailed, TargetType: "user", TargetID: "alice", At: "2026-09-27T17:02:00Z", IP: "203.0.113.8"})
	s.st.WriteAudit(AuditEntry{Actor: "bob", Action: AuditLogin, TargetType: "user", TargetID: "bob", At: "2026-09-27T18:00:00Z", IP: "203.0.113.9", Detail: `{"method":"sso"}`})
	s.st.WriteAudit(AuditEntry{Actor: "alice", Action: AuditLogin, TargetType: "user", TargetID: "alice", At: "2026-09-27T19:00:00Z", IP: "2001:db8::7", Detail: `{"method":"passkey","client":{"browser":"Chrome","browser_version":"128.0","os":"macOS","os_version":"14.6","device":"Mac","device_type":"desktop","user_agent":"secret raw user agent","client_hints":{"Sec-CH-UA-Platform":"macOS"}}}`})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/me/login-activity?limit=10", nil)
	s.apiLoginActivity(rec, req, "alice")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			ID     int64          `json:"id"`
			At     string         `json:"at"`
			IP     string         `json:"ip"`
			Method string         `json:"method"`
			Client *ClientSummary `json:"client"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("total/items = %d/%d, want 2/2: %s", got.Total, len(got.Items), rec.Body.String())
	}
	if got.Items[0].At != "2026-09-27T19:00:00Z" || got.Items[0].IP != "2001:db8::7" || got.Items[0].Method != "passkey" {
		t.Fatalf("newest item = %+v", got.Items[0])
	}
	if got.Items[0].Client == nil || got.Items[0].Client.Browser != "Chrome" || got.Items[0].Client.OS != "macOS" || got.Items[0].Client.DeviceType != "desktop" {
		t.Fatalf("client summary = %+v", got.Items[0].Client)
	}
	if strings.Contains(rec.Body.String(), "secret raw user agent") || strings.Contains(rec.Body.String(), "client_hints") {
		t.Fatalf("self-service response exposed admin-only client details: %s", rec.Body.String())
	}
	if got.Items[1].At != "2026-09-27T17:00:00Z" || got.Items[1].Method != "password" {
		t.Fatalf("older item = %+v", got.Items[1])
	}
}

func TestLoginActivityCapsTheRequestedPage(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	for i := 0; i < 30; i++ {
		s.st.WriteAudit(AuditEntry{Actor: "alice", Action: AuditLogin, TargetType: "user", TargetID: "alice", At: "2026-09-27T17:00:00Z"})
	}
	rec := httptest.NewRecorder()
	s.apiLoginActivity(rec, httptest.NewRequest("GET", "/api/me/login-activity?limit=999", nil), "alice")
	var got struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 20 {
		t.Fatalf("items = %d, want the 20-row self-service cap", len(got.Items))
	}
}
