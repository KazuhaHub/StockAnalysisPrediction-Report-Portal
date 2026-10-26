package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Explicitly requested loopback browser fixture with a disposable database and
// synthetic publisher/session. No listener exists in ordinary tests or binaries.
func TestPlatformManagementBrowserFixture(t *testing.T) {
	stateFile := os.Getenv("APP_INSTALL_HTTP_STATE")
	if stateFile == "" {
		t.Skip("installation browser fixture not requested")
	}
	s, mux, actor, _, _ := platformManagementFixture(t, false)
	platformStartFrontendService(t, s)
	server := httptest.NewServer(mux)
	defer server.Close()
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	raw, err := json.Marshal(map[string]string{"url": server.URL, "cookieName": cookieName, "cookie": cookie})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(stateFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = output.Write(raw); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stateFile + ".stop"); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		case <-timer.C:
			t.Fatal("installation browser fixture expired")
		}
	}
}
