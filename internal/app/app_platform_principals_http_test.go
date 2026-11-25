package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type platformPrincipalEnv struct {
	s            *Server
	mux          *http.ServeMux
	admin        platformActorSession
	installation string
	cookie       string
	groups       map[string]int64
}

func (p *platformPrincipalEnv) binding(t *testing.T) map[string]string {
	t.Helper()
	b, err := p.s.inspectPlatformLaunchAccess(p.admin, p.installation)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"generation": b.generation, "policyRevision": b.policyRevision, "releaseSha256": b.releaseSHA}
}

func (p *platformPrincipalEnv) post(t *testing.T, route string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return p.postAs(t, p.cookie, route, body)
}

func (p *platformPrincipalEnv) postAs(t *testing.T, cookie, route string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if _, ok := body["binding"]; !ok {
		body["binding"] = p.binding(t)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return sessionTestRequest(p.mux, "POST", "/api/app-platform/installations/"+p.installation+"/principals/"+route, cookie, string(raw))
}

type platformPrincipalItem struct {
	Principal string  `json:"principal"`
	Label     string  `json:"label"`
	Parent    *string `json:"parent"`
}

func decodePlatformPrincipals(t *testing.T, w *httptest.ResponseRecorder) ([]platformPrincipalItem, string) {
	t.Helper()
	if w.Code != 200 {
		t.Fatal("principals", w.Code, w.Body.String())
	}
	var out struct {
		Items []platformPrincipalItem `json:"items"`
		Next  string                  `json:"next"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items, out.Next
}

func platformPrincipalLabels(items []platformPrincipalItem) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.Label)
	}
	slices.Sort(labels)
	return labels
}

// platformPrincipalEnvironment builds the directory fixture of doc 181:
//
//	Research (granted)          alice "Alice Analyst", ivan (inactive)
//	  Research Lab (descendant) bob (no display name)
//	Other (not granted)         carol
//
// The fixture reader is an administrator in Research. Access is the OU policy (doc 190, D7).
func platformPrincipalEnvironment(t *testing.T, pg bool) *platformPrincipalEnv {
	t.Helper()
	s, reader, id := platformPrincipalsFixture(t, pg)
	// The directory fixture shares a database with every other PostgreSQL test: clear what an
	// earlier run left, and what this one creates, so the tests stay independent.
	clearPlatformPrincipalFixture(t, s)
	t.Cleanup(func() { clearPlatformPrincipalFixture(t, s) })
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, reader.username)
	groups := map[string]int64{}
	for _, name := range []string{"Research", "Research Lab", "Other"} {
		group, err := s.st.CreateUserGroup(name, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		groups[name] = group
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, groups["Research"], groups["Research Lab"])
	for username, group := range map[string]string{"alice": "Research", "bob": "Research Lab", "carol": "Other", "dave": "Research", "ivan": "Research"} {
		if err := s.st.UpsertUser(User{Username: username, PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
			t.Fatal(err)
		}
		mustExec(t, s.st, `UPDATE users SET group_id=?,email=? WHERE username=?`, groups[group], username+"@example.test", username)
	}
	mustExec(t, s.st, `UPDATE users SET display_name='Alice Analyst' WHERE username='alice'`)
	mustExec(t, s.st, `UPDATE users SET active=0 WHERE username='ivan'`)
	// The policy opens Research (and so Research Lab); the administrator and dave sit in Research.
	platformSetAccess(t, s, id, int64(groups["Research"]), true)
	mustExec(t, s.st, `UPDATE users SET group_id=? WHERE username=?`, groups["Research"], reader.username)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	return &platformPrincipalEnv{s: s, mux: mux, admin: reader, installation: id, cookie: cookie, groups: groups}
}

// Doc 181 test 1: the listed values are byte for byte what each user and OU
// later present at launch.
func TestPlatformPrincipalSearchEqualsLaunchViewerValues(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		users, next := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user"}))
		if next != "" {
			t.Fatal("a short result space paged")
		}
		listedUsers := map[string]platformPrincipalItem{}
		for _, item := range users {
			listedUsers[item.Principal] = item
			if item.Parent != nil {
				t.Fatal("a user carried a parent")
			}
		}
		// alice, bob (in a descendant of the granted OU), dave (direct) and the administrator.
		// Not carol (outside the audience) and not ivan (inactive).
		if labels := platformPrincipalLabels(users); !slices.Equal(labels, []string{"Alice Analyst", "bob", "dave", p.admin.username}) {
			t.Fatal("wrong user result space", labels)
		}
		ous, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "ou"}))
		if labels := platformPrincipalLabels(ous); !slices.Equal(labels, []string{"Research", "Research Lab"}) {
			t.Fatal("wrong OU result space", labels)
		}
		ouByLabel := map[string]platformPrincipalItem{}
		for _, item := range ous {
			ouByLabel[item.Label] = item
		}
		if ouByLabel["Research"].Parent != nil || ouByLabel["Research Lab"].Parent == nil || *ouByLabel["Research Lab"].Parent != ouByLabel["Research"].Principal {
			t.Fatal("OU parents differ from the nearest ancestor inside the result space", ous)
		}
		for _, username := range []string{"alice", "bob", "dave", p.admin.username} {
			session := p.admin
			if username != p.admin.username {
				session = platformReportSession(t, p.s, username)
			}
			viewer, err := p.s.readPlatformLaunchViewerContext(context.Background(), session, p.installation)
			if err != nil {
				t.Fatal(username, err)
			}
			if _, ok := listedUsers["user:"+viewer.Subject]; !ok {
				t.Fatal("listed principal differs from the viewer subject of", username)
			}
		}
		for username, label := range map[string]string{"alice": "Research", "bob": "Research Lab"} {
			viewer, err := p.s.readPlatformLaunchViewerContext(context.Background(), platformReportSession(t, p.s, username), p.installation)
			if err != nil || !slices.Contains(viewer.OUs, strings.TrimPrefix(ouByLabel[label].Principal, "ou:")) {
				t.Fatal("listed OU differs from the OU chain of", username, err)
			}
		}
	})
}

// clearPlatformPrincipalFixture removes the accounts, OUs and audit rows the directory fixture and its
// tests create. The platform tables are cleaned by platformTestStore.
func clearPlatformPrincipalFixture(t *testing.T, s *Server) {
	t.Helper()
	for _, name := range []string{"alice", "bob", "carol", "dave", "ivan"} {
		if s.st.GetUser(name) != nil {
			if err := s.st.DeleteUser(name); err != nil {
				t.Error(err)
			}
		}
	}
	mustExec(t, s.st, `DELETE FROM users WHERE username LIKE 'bulk-%'`)
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE name IN ('Research','Research Lab','Other')`)
	mustExec(t, s.st, `DELETE FROM user_groups WHERE name IN ('Research','Research Lab','Other')`)
	mustExec(t, s.st, `DELETE FROM audit_log WHERE action IN ('app_platform.principal_directory_used','app_platform.capability_granted','app_platform.capability_withdrawn')`)
}
