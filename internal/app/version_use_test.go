package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

func TestRecordVersionUseRequiresASession(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.requireUserJSON(s.handleVersionUse)(rec, httptest.NewRequest(http.MethodPost, "/api/me/version-use", strings.NewReader(`{"version":"v2026.40.3"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous version use = %d, want 401", rec.Code)
	}
}

func TestRecordUserVersionUseKeepsTheHighestRelease(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertUser(User{Username: "alice", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		version string
		first   bool
		stored  string
	}{
		{version: "v2026.40.2", first: true, stored: "v2026.40.2"},
		{version: "v2026.40.2", first: false, stored: "v2026.40.2"},
		{version: "v2026.39.9", first: false, stored: "v2026.40.2"},
		{version: "dev", first: false, stored: "v2026.40.2"},
		{version: "v2026.40.3", first: true, stored: "v2026.40.3"},
	} {
		first, err := st.RecordUserVersionUse("alice", tc.version)
		if err != nil {
			t.Fatalf("record %s: %v", tc.version, err)
		}
		if first != tc.first {
			t.Errorf("record %s first = %v, want %v", tc.version, first, tc.first)
		}
		got := st.GetUser("alice")
		if got == nil || got.LastUsedVersion != tc.stored {
			t.Errorf("after %s stored version = %v, want %q", tc.version, got, tc.stored)
		}
	}
}

func TestRecordVersionUseOnlyAcceptsTheRunningPage(t *testing.T) {
	s := tenancyServer(t)
	if err := s.st.UpsertUser(User{Username: "alice", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.BuildDate
	t.Cleanup(func() {
		version.Version, version.Commit, version.BuildDate = oldVersion, oldCommit, oldDate
	})
	version.Version = "v2026.40.3"

	call := func(pageVersion string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/me/version-use", strings.NewReader(`{"version":"`+pageVersion+`"}`))
		s.handleVersionUse(rec, req, "alice")
		var out map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, out
	}

	if code, out := call("v2026.40.2"); code != 409 || out["firstUse"] != false {
		t.Fatalf("stale page response = %d %v, want 409 and no first use", code, out)
	}
	if got := s.st.GetUser("alice"); got == nil || got.LastUsedVersion != "" {
		t.Fatalf("a stale page recorded version use: %+v", got)
	}

	if code, out := call("v2026.40.3"); code != 200 || out["version"] != "v2026.40.3" || out["firstUse"] != true {
		t.Fatalf("first current-page response = %d %v", code, out)
	}
	if code, out := call("v2026.40.3"); code != 200 || out["firstUse"] != false {
		t.Fatalf("second current-page response = %d %v", code, out)
	}
}
