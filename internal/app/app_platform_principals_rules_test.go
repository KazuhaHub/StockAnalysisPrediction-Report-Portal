package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (p *platformPrincipalEnv) searchLabels(t *testing.T, kind string) []string {
	t.Helper()
	items, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": kind}))
	return platformPrincipalLabels(items)
}

func (p *platformPrincipalEnv) deriver(t *testing.T) appplatform.ViewerPrincipalDeriver {
	t.Helper()
	appID := scalar[string](t, p.s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, p.installation)
	seed, err := p.s.readPlatformInstallationSeed(p.s.st.poolExec(), p.installation, appID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(seed) })
	deriver, err := appplatform.NewViewerPrincipalDeriver(appID, p.installation, seed)
	if err != nil {
		t.Fatal(err)
	}
	return deriver
}

// Doc 181 test 2: only a current administrator of an enabled, ready installation whose capability
// is granted and whose launch binding matches. The answers carry no principal at all.
func TestPlatformPrincipalRoutesRefuseEveryOtherCaller(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		for _, route := range []string{"search", "resolve"} {
			body := func(p *platformPrincipalEnv) map[string]any {
				if route == "search" {
					return map[string]any{"kind": "user"}
				}
				return map[string]any{"principals": []string{"user:" + strings.Repeat("a", 64)}}
			}
			for name, arrange := range map[string]func(*testing.T, *platformPrincipalEnv, map[string]string) (string, map[string]any){
				"non administrator": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					alice := platformReportSession(t, p.s, "alice")
					return p.s.managedCookie(*p.s.st.GetUser("alice"), alice.sessionID, alice.expires.Unix()), body(p)
				},
				// The fixture declares principals.read optional, so a withdrawn grant still launches
				// and only the directory's own granted check can refuse the call.
				"capability withdrawn": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					mustExec(t, p.s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='principals.read'`, p.installation)
					return p.cookie, body(p)
				},
				"app disabled": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					mustExec(t, p.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, p.installation)
					return p.cookie, body(p)
				},
				"stale generation": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					b["generation"] = "2"
					out := body(p)
					out["binding"] = b
					return p.cookie, out
				},
				"stale release": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					b["releaseSha256"] = strings.Repeat("c", 64)
					out := body(p)
					out["binding"] = b
					return p.cookie, out
				},
				"administrator outside the audience": func(t *testing.T, p *platformPrincipalEnv, b map[string]string) (string, map[string]any) {
					mustExec(t, p.s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=? AND principal_type='user' AND username=?`, p.installation, p.admin.username)
					return p.cookie, body(p)
				},
			} {
				t.Run(route+"/"+name, func(t *testing.T) {
					p := platformPrincipalEnvironment(t, pg)
					b := p.binding(t)
					cookie, request := arrange(t, p, b)
					if _, ok := request["binding"]; !ok {
						request["binding"] = b
					}
					w := p.postAs(t, cookie, route, request)
					if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"not_permitted"`) || strings.Contains(w.Body.String(), "principal\"") {
						t.Fatal("refusal", w.Code, w.Body.String())
					}
				})
			}
		}
	})
}

func TestPlatformPrincipalRoutesRequireASession(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		for _, route := range []string{"search", "resolve"} {
			if w := p.postAs(t, "", route, map[string]any{"kind": "user"}); w.Code != http.StatusUnauthorized {
				t.Fatal("anonymous", route, w.Code, w.Body.String())
			}
		}
		// Reading is not a method of these routes.
		for _, route := range []string{"search", "resolve"} {
			if w := sessionTestRequest(p.mux, "GET", "/api/app-platform/installations/"+p.installation+"/principals/"+route, p.cookie, ""); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
				t.Fatal("GET reached the directory", route, w.Code)
			}
		}
	})
}

// Doc 181 test 3: a user outside the audience and an OU outside the granted subtrees never appear,
// including through resolve, which only answers for principals inside the result space.
func TestPlatformPrincipalResolveStaysInsideTheAudience(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		deriver := p.deriver(t)
		derive := func(username string) string {
			generation, err := notificationAccountGeneration(p.s.st.poolExec(), username)
		if err != nil {
			t.Fatal(err)
		}
		value, err := deriver.Account(appplatform.ViewerAccountIdentity{Name: username, Created: p.s.st.GetUser(username).CreatedAt, Generation: generation})
			if err != nil {
				t.Fatal(err)
			}
			return "user:" + value
		}
		other, err := deriver.OU(appplatform.ViewerOUIdentity{ID: p.groups["Other"], Created: scalar[string](t, p.s.st, `SELECT created_at FROM user_groups WHERE id=?`, p.groups["Other"])})
		if err != nil {
			t.Fatal(err)
		}
		research, err := deriver.OU(appplatform.ViewerOUIdentity{ID: p.groups["Research"], Created: scalar[string](t, p.s.st, `SELECT created_at FROM user_groups WHERE id=?`, p.groups["Research"])})
		if err != nil {
			t.Fatal(err)
		}
		asked := []string{derive("carol"), derive("alice"), "ou:" + other, derive("ivan"), "ou:" + research, "user:" + strings.Repeat("0", 64)}
		w := p.post(t, "resolve", map[string]any{"principals": asked})
		if w.Code != 200 {
			t.Fatal("resolve", w.Code, w.Body.String())
		}
		var out struct {
			Items   []platformPrincipalItem `json:"items"`
			Unknown []string                `json:"unknown"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Items) != 2 || out.Items[0].Principal != asked[1] || out.Items[0].Label != "Alice Analyst" || out.Items[1].Principal != asked[4] || out.Items[1].Label != "Research" {
			t.Fatal("resolved the wrong principals", w.Body.String())
		}
		if !slices.Equal(out.Unknown, []string{asked[0], asked[2], asked[3], asked[5]}) {
			t.Fatal("principals outside the audience were not reported unknown", out.Unknown)
		}
		if strings.Contains(w.Body.String(), "carol") || strings.Contains(w.Body.String(), "Other") || strings.Contains(w.Body.String(), "ivan") {
			t.Fatal("a label outside the audience leaked")
		}
	})
}

// Doc 181 test 4, with the other ways an account stops being listable.
func TestPlatformPrincipalDirectoryFollowsCurrentMembership(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		if labels := p.searchLabels(t, "user"); !slices.Contains(labels, "Alice Analyst") {
			t.Fatal("fixture user missing", labels)
		}
		if _, err := p.s.st.SetPrimaryGroups([]string{"alice"}, p.groups["Other"]); err != nil {
			t.Fatal(err)
		}
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "Alice Analyst") {
			t.Fatal("a user moved out of the granted OU is still listed", labels)
		}
		if _, err := p.s.st.SetPrimaryGroups([]string{"alice"}, p.groups["Research"]); err != nil {
			t.Fatal(err)
		}
		if labels := p.searchLabels(t, "user"); !slices.Contains(labels, "Alice Analyst") {
			t.Fatal("a user moved back into the granted OU is not listed", labels)
		}
		// An OU taken out of the granted subtree stops being listed too.
		mustExec(t, p.s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, p.groups["Research Lab"])
		if labels := p.searchLabels(t, "ou"); !slices.Equal(labels, []string{"Research"}) {
			t.Fatal("an OU outside the granted subtree is listed", labels)
		}
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "bob") {
			t.Fatal("a member of an OU outside the granted subtree is listed", labels)
		}
		// A direct grant on an expired or disabled account does not list it.
		mustExec(t, p.s.st, `UPDATE users SET expires_at='2000-01-01' WHERE username='dave'`)
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "dave") {
			t.Fatal("an expired account is listed", labels)
		}
		mustExec(t, p.s.st, `UPDATE users SET expires_at='' WHERE username='dave'`)
		mustExec(t, p.s.st, `UPDATE users SET active=0 WHERE username='dave'`)
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "dave") {
			t.Fatal("a disabled account is listed", labels)
		}
		// A recreated account instance does not inherit the old direct grant.
		mustExec(t, p.s.st, `UPDATE users SET active=1 WHERE username='dave'`)
		mustExec(t, p.s.st, `UPDATE app_platform_access_grants SET account_generation=account_generation+1 WHERE installation_id=? AND username='dave'`, p.installation)
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "dave") {
			t.Fatal("a recreated account inherited the old grant", labels)
		}
	})
}

// A recreated OU is a different instance: it does not inherit the old grant, as at launch.
func TestPlatformPrincipalDirectoryIgnoresARecreatedGrantedOU(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		mustExec(t, p.s.st, `UPDATE user_groups SET created_at='recreated-instance' WHERE id=?`, p.groups["Research"])
		if labels := p.searchLabels(t, "ou"); len(labels) != 0 {
			t.Fatal("a recreated OU kept the old grant", labels)
		}
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "Alice Analyst") || slices.Contains(labels, "bob") {
			t.Fatal("members of a recreated OU kept the old grant", labels)
		}
	})
}

// Doc 181 test 5: principals are scoped to one installation seed.
func TestPlatformPrincipalsDifferPerInstallationSeed(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		first, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user"}))
		dek, err := p.s.readPlatformDEK(p.s.st.poolExec())
		if err != nil {
			t.Fatal(err)
		}
		defer clear(dek)
		seed := make([]byte, 32)
		for i := range seed {
			seed[i] = byte(i + 1)
		}
		sealed, err := aesSeal(dek, seed, aad("app-platform", p.installation))
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, p.s.st, `UPDATE app_platform_installations SET sealed_identity_seed=? WHERE id=?`, sealPrefix+sealed, p.installation)
		second, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user"}))
		if len(first) == 0 || len(first) != len(second) {
			t.Fatal("fixture changed", len(first), len(second))
		}
		known := map[string]bool{}
		for _, item := range first {
			known[item.Principal] = true
		}
		for _, item := range second {
			if known[item.Principal] {
				t.Fatal("a principal survived a different installation seed", item.Label)
			}
		}
	})
}

// Doc 181 test 6, first half.
func TestPlatformPrincipalCallsAreRateLimitedPerViewerAndInstallation(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		for n := 1; n <= 30; n++ {
			if w := p.post(t, "search", map[string]any{"kind": "ou"}); w.Code != 200 {
				t.Fatal("call", n, w.Code, w.Body.String())
			}
		}
		w := p.post(t, "resolve", map[string]any{"principals": []string{"user:" + strings.Repeat("a", 64)}})
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
			t.Fatal("31st call", w.Code, w.Header(), w.Body.String())
		}
		// The ceiling belongs to the viewer: another administrator keeps their own allowance.
		mustExec(t, p.s.st, `UPDATE users SET role='admin' WHERE username='alice'`)
		alice := platformReportSession(t, p.s, "alice")
		if w := p.postAs(t, p.s.managedCookie(*p.s.st.GetUser("alice"), alice.sessionID, alice.expires.Unix()), "search", map[string]any{"kind": "ou"}); w.Code != 200 {
			t.Fatal("another viewer was throttled", w.Code, w.Body.String())
		}
	})
}

// Doc 181 test 6, second half: the whole population is scanned per call, so it must stay cheap.
func TestPlatformPrincipalDirectoryAnswersTenThousandUsersWithinTheDocumentBudget(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		tx, err := p.s.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 10000; n++ {
			if _, err := tx.Exec(p.s.st.bind(`INSERT INTO users(username,password_hash,role,created_at,session_rev,group_id,active,login_history_initialized) VALUES(?,?,?,?,?,?,1,1)`), fmt.Sprintf("bulk-%05d", n), "x", "user", "2026-01-01T00:00:00Z", 1, p.groups["Research"]); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		items, next := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user", "query": "bulk-0", "limit": 50}))
		elapsed := time.Since(start)
		if len(items) != 50 || next == "" {
			t.Fatal("first page", len(items), next)
		}
		if elapsed > platformDocumentTimeout/2 {
			t.Fatal("ten thousand users took", elapsed)
		}
	})
}

// Doc 181 test 7: nothing but the principal and a label.
func TestPlatformPrincipalAnswersCarryNoProfileData(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		var seen strings.Builder
		for _, kind := range []string{"user", "ou"} {
			w := p.post(t, "search", map[string]any{"kind": kind})
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			seen.WriteString(w.Body.String())
		}
		body := seen.String()
		for _, forbidden := range []string{"alice", "example.test", `"role"`, `"id"`, "created", "Z\"", `"email"`, `"username"`} {
			if strings.Contains(body, forbidden) {
				t.Fatal("answer contains", forbidden, body)
			}
		}
		for username, created := range map[string]string{"alice": p.s.st.GetUser("alice").CreatedAt, "bob": p.s.st.GetUser("bob").CreatedAt} {
			if strings.Contains(body, created) {
				t.Fatal("answer contains the creation time of", username)
			}
		}
		// The username only stands in as a label where no display name exists.
		if !strings.Contains(body, `"label":"bob"`) || !strings.Contains(body, `"label":"Alice Analyst"`) {
			t.Fatal("label rule", body)
		}
		for _, id := range p.groups {
			if strings.Contains(body, fmt.Sprintf(`:%d,`, id)) {
				t.Fatal("numeric OU ID in answer")
			}
		}
	})
}

func TestPlatformPrincipalSearchPagesFiltersAndRejectsMalformedInput(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		var all []string
		cursor := ""
		for page := 0; page < 10; page++ {
			items, next := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user", "limit": 1, "after": cursor}))
			if len(items) != 1 {
				t.Fatal("page", page, len(items))
			}
			all = append(all, items[0].Principal)
			if next == "" {
				break
			}
			cursor = next
		}
		if len(all) != 4 || !slices.IsSorted(all) || len(slices.Compact(slices.Clone(all))) != 4 {
			t.Fatal("paging skipped or repeated a principal", all)
		}
		items, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user", "query": "ALICE"}))
		if len(items) != 1 || items[0].Label != "Alice Analyst" {
			t.Fatal("case-insensitive label substring", items)
		}
		// The label is searched, not the username behind a display name.
		if items, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "user", "query": "alice"})); len(items) != 1 {
			t.Fatal("search by label", items)
		}
		good, _ := decodePlatformPrincipals(t, p.post(t, "search", map[string]any{"kind": "ou", "limit": 1}))
		validCursor := platformPrincipalCursor(good[0].Principal)
		for name, body := range map[string]map[string]any{
			"unknown kind":        {"kind": "role"},
			"missing kind":        {},
			"long query":          {"kind": "ou", "query": strings.Repeat("q", 65)},
			"zero limit":          {"kind": "ou", "limit": 0},
			"large limit":         {"kind": "ou", "limit": 51},
			"negative limit":      {"kind": "ou", "limit": -1},
			"garbage cursor":      {"kind": "ou", "after": "!!!"},
			"cursor of the wrong": {"kind": "user", "after": validCursor},
			"huge cursor":         {"kind": "ou", "after": strings.Repeat("A", 129)},
			"unknown field":       {"kind": "ou", "email": "x"},
		} {
			if w := p.post(t, "search", body); w.Code != http.StatusBadRequest {
				t.Fatal(name, w.Code, w.Body.String())
			}
		}
		valid := "user:" + strings.Repeat("a", 64)
		many := make([]string, 101)
		for i := range many {
			many[i] = fmt.Sprintf("user:%064x", i)
		}
		for name, principals := range map[string][]string{
			"empty": {}, "duplicate": {valid, valid}, "too many": many, "bad prefix": {"role:" + strings.Repeat("a", 64)},
			"short": {"user:abc"}, "upper case": {"user:" + strings.Repeat("A", 64)}, "raw username": {"alice"},
		} {
			if w := p.post(t, "resolve", map[string]any{"principals": principals}); w.Code != http.StatusBadRequest {
				t.Fatal(name, w.Code, w.Body.String())
			}
		}
		if w := p.post(t, "resolve", map[string]any{"principals": many[:100]}); w.Code != 200 {
			t.Fatal("exactly one hundred principals", w.Code, w.Body.String())
		}
	})
}

func TestPlatformPrincipalLabelsAreBoundedOnAUTF8Boundary(t *testing.T) {
	label := strings.Repeat("研", 100)
	got := truncatePlatformPrincipalLabel(label)
	if len(got) > 160 || !utf8.ValidString(got) || !strings.HasPrefix(label, got) || len(got) != 159 {
		t.Fatal("label cut inside a rune or too long", len(got))
	}
	if truncatePlatformPrincipalLabel("short") != "short" || truncatePlatformPrincipalLabel("a\xffb") != "ab" {
		t.Fatal("short or invalid label changed unexpectedly")
	}
}

// Doc 181 section 5: one audit row per viewer, installation and hour, with call counts only.
func TestPlatformPrincipalDirectoryUseIsAuditedOncePerHour(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		for _, call := range []struct {
			route string
			body  map[string]any
		}{
			{"search", map[string]any{"kind": "user", "query": "secret-needle"}},
			{"search", map[string]any{"kind": "ou"}},
			{"resolve", map[string]any{"principals": []string{"user:" + strings.Repeat("b", 64)}}},
		} {
			if w := p.post(t, call.route, call.body); w.Code != 200 {
				t.Fatal(call.route, w.Code, w.Body.String())
			}
		}
		// Refused and malformed calls are not use of the directory.
		p.post(t, "search", map[string]any{"kind": "nope"})
		rows, total := p.s.st.ListAudit(AuditFilter{Action: "app_platform.principal_directory_used"})
		if total != 1 || len(rows) != 1 {
			t.Fatal("audit rows", total)
		}
		row := rows[0]
		if row.Actor != p.admin.username || row.TargetID != p.installation || !strings.Contains(row.Detail, `"search":2`) || !strings.Contains(row.Detail, `"resolve":1`) {
			t.Fatal("audit row", row)
		}
		if strings.Contains(row.Detail, "secret-needle") || strings.Contains(row.Detail, "bbbb") {
			t.Fatal("audit row recorded a query or principal", row.Detail)
		}
	})
}

// Launch refuses an account whose OU chain holds an OU without a creation identity, even when the
// account is granted directly (platformAudienceChain). The directory applies the same rule, so it
// never lists a user or OU that could not present the principal at launch.
func TestPlatformPrincipalDirectoryExcludesChainsWithoutACreationIdentity(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		mustExec(t, p.s.st, `UPDATE user_groups SET created_at='' WHERE id=?`, p.groups["Research Lab"])
		if labels := p.searchLabels(t, "ou"); !slices.Equal(labels, []string{"Research"}) {
			t.Fatal("an OU without a creation identity is listed", labels)
		}
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "bob") || !slices.Contains(labels, "Alice Analyst") {
			t.Fatal("a member of an OU without a creation identity is listed, or a healthy chain is not", labels)
		}
		// An ancestor without one removes the whole subtree below it.
		mustExec(t, p.s.st, `UPDATE user_groups SET created_at='' WHERE id=?`, p.groups["Research"])
		if labels := p.searchLabels(t, "ou"); len(labels) != 0 {
			t.Fatal("OUs below an ancestor without a creation identity are listed", labels)
		}
		// A direct grant does not rescue an account whose own chain is unusable.
		mustExec(t, p.s.st, `UPDATE user_groups SET created_at='' WHERE id=?`, p.groups["Other"])
		if labels := p.searchLabels(t, "user"); slices.Contains(labels, "dave") || slices.Contains(labels, "Alice Analyst") {
			t.Fatal("a directly granted account with an unusable chain is listed", labels)
		}
	})
}

// Only a real installation the viewer may use reaches the limiter, so arbitrary installation ids
// cannot grow the table past its cap and flush an exhausted viewer's counter.
func TestPlatformPrincipalRateLimitSurvivesArbitraryInstallationIDs(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		body := map[string]any{"kind": "ou"}
		for n := 1; n <= 30; n++ {
			if w := p.post(t, "search", body); w.Code != 200 {
				t.Fatal("call", n, w.Code)
			}
		}
		binding := p.binding(t)
		raw, err := json.Marshal(map[string]any{"binding": binding, "kind": "ou"})
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 4097; n++ {
			path := fmt.Sprintf("/api/app-platform/installations/%08x-0000-4000-8000-%012x/principals/search", n, n)
			if w := sessionTestRequest(p.mux, "POST", path, p.cookie, string(raw)); w.Code == 200 || w.Code == http.StatusTooManyRequests {
				t.Fatal("an unknown installation was answered", w.Code)
			}
		}
		if w := p.post(t, "search", body); w.Code != http.StatusTooManyRequests {
			t.Fatal("the limit was reset by unrelated installation ids", w.Code)
		}
	})
}

// The hourly row is counted inside one database transaction, not under an in-process mutex alone:
// a second server value over the same store (another process) must not lose counts either.
func TestPlatformPrincipalAuditCountsAcrossServersWithoutLosingCalls(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		other := &Server{st: p.s.st}
		var wg sync.WaitGroup
		errs := make(chan error, 80)
		for i := 0; i < 80; i++ {
			server := p.s
			if i%2 == 1 {
				server = other
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- server.recordPlatformPrincipalDirectoryUse(p.admin.username, 0, p.installation, "search", "")
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		rows, total := p.s.st.ListAudit(AuditFilter{Action: "app_platform.principal_directory_used"})
		if total != 1 || len(rows) != 1 || !strings.Contains(rows[0].Detail, `"search":80`) {
			t.Fatal("audit rows", total, rows)
		}
	})
}
