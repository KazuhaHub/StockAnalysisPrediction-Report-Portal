package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func platformReportsFixture(t *testing.T) (*Server, platformActorSession, string) {
	t.Helper()
	s, admin := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, admin)
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	compatibility, ok := manifest["compatibility"].(map[string]any)
	if !ok {
		t.Fatal("missing compatibility fixture")
	}
	compatibility["platform_api"] = ">=2.0.0-0 <3.0.0"
	manifest["capabilities"] = map[string]any{"required": []string{"reports.read"}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(admin, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	s, reader, installation := completePlatformLaunchFixture(t, s, admin, release.sha256)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='reports.read'`, installation)
	platformGrantLaunchUser(t, s, reader, installation)
	return s, reader, installation
}

func platformReportSession(t *testing.T, s *Server, username string) platformActorSession {
	t.Helper()
	account := s.st.GetUser(username)
	if account == nil {
		t.Fatal("missing report fixture user")
	}
	generation, err := notificationAccountGeneration(s.st.poolExec(), username)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := fmt.Sprintf("%x", sha256.Sum256([]byte(username)))
	session := platformActorSession{username: username, created: account.CreatedAt, sessionID: sessionID, sessionRevision: account.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(t, s.st, session)
	return session
}

func platformReportsRequest(t *testing.T, s *Server, session platformActorSession, installation string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	cookie := s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())
	return sessionTestRequest(mux, "POST", "/api/app-platform/installations/"+installation+"/reports", cookie, string(payload))
}

func TestPlatformReportsCurrentScopeAndCapability(t *testing.T) {
	s, first, installation := platformReportsFixture(t)
	binding, err := s.inspectPlatformLaunchAccess(first, installation)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `INSERT INTO reports(title,symbol,rtype,rdate,version,body_md) VALUES('visible report','600100','research','2026-10-30','','body')`)
	body := map[string]any{"binding": map[string]string{"generation": binding.generation, "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}, "query": map[string]any{"q": "visible", "limit": 20}}
	response := platformReportsRequest(t, s, first, installation, body)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("visible report")) {
		t.Fatalf("current viewer report query failed: %d %s", response.Code, response.Body.String())
	}
	var list struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
		Total int  `json:"total"`
		Items []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if json.Unmarshal(response.Body.Bytes(), &list) != nil || !list.OK || list.Count != 1 || list.Total != 1 || len(list.Items) != 1 {
		t.Fatal("report list contract changed")
	}
	detail := map[string]any{"binding": body["binding"], "id": list.Items[0].ID}
	response = platformReportsRequest(t, s, first, installation, detail)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("body_md")) {
		t.Fatalf("report detail failed: %d %s", response.Code, response.Body.String())
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='reports.read'`, installation)
	response = platformReportsRequest(t, s, first, installation, body)
	if response.Code == http.StatusOK {
		t.Fatal("revoked reports.read still returned business data")
	}
}

func TestPlatformReportsTwoOUsAndOldBinding(t *testing.T) {
	s, first, installation := platformReportsFixture(t)
	binding, err := s.inspectPlatformLaunchAccess(first, installation)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the default fixture reader with two disjoint restricted readers.
	if err := s.st.UpsertUser(User{Username: "report-ou-a", PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertUser(User{Username: "report-ou-b", PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	root := s.st.EnsureDefaultGroup()
	groupA, _ := s.st.CreateUserGroup("reports-a", "", int(root))
	groupB, _ := s.st.CreateUserGroup("reports-b", "", int(root))
	s.st.SetGroupRestricted(groupA, true)
	s.st.SetGroupRestricted(groupB, true)
	ids := map[string]int64{}
	for index, value := range []struct {
		user, version, title string
		group                int64
	}{{"report-ou-a", "report-version-a", "A only", groupA}, {"report-ou-b", "report-version-b", "B only", groupB}} {
		if err := s.st.SetPrimaryGroup(value.user, value.group); err != nil {
			t.Fatal(err)
		}
		s.st.SaveVersion(ReportVersion{Name: value.version, Ord: 3, Visibility: VisibilityGroup})
		s.st.SetVersionGrants(value.version, []string{groupPrincipal(value.group)})
		id, _, err := s.st.UpsertReport(Rep{Symbol: fmt.Sprintf("60010%d", index), Date: "2026-10-30", RType: "research", Title: value.title, Version: value.version, MD: value.title})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.st.StampReportOwner(id, value.group); err != nil {
			t.Fatal(err)
		}
		if err := s.st.AddReportViewer(id, "2026-10-30", value.user, value.group); err != nil {
			t.Fatal(err)
		}
		session := platformReportSession(t, s, value.user)
		platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(700 + index), "installation_id": installation, "principal_type": "user", "username": value.user, "account_created": session.created, "account_generation": session.generation, "group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
		ids[value.user] = id
	}
	a := platformReportSession(t, s, "report-ou-a")
	b := platformReportSession(t, s, "report-ou-b")
	bindingMap := map[string]string{"generation": binding.generation, "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}
	for user, session := range map[string]platformActorSession{"report-ou-a": a, "report-ou-b": b} {
		response := platformReportsRequest(t, s, session, installation, map[string]any{"binding": bindingMap, "query": map[string]any{"limit": 20}})
		if response.Code != http.StatusOK {
			t.Fatalf("%s list failed: %s", user, response.Body.String())
		}
		var result struct {
			Total int `json:"total"`
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		json.Unmarshal(response.Body.Bytes(), &result)
		if result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != ids[user] {
			t.Fatalf("%s saw another OU's report: %+v", user, result)
		}
		other := "report-ou-b"
		if user == other {
			other = "report-ou-a"
		}
		denied := platformReportsRequest(t, s, session, installation, map[string]any{"binding": bindingMap, "id": ids[other]})
		if denied.Code != http.StatusNotFound {
			t.Fatalf("%s enumerated another OU's report: %d", user, denied.Code)
		}
	}
	oldBinding := map[string]string{"generation": "2", "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}
	response := platformReportsRequest(t, s, a, installation, map[string]any{"binding": oldBinding, "query": map[string]any{"limit": 20}})
	if response.Code == http.StatusOK {
		t.Fatal("old generation read report data")
	}
}
