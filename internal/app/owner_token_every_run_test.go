package app

// P39b: every run that has a submitter carries the signed owner token, so internal runs are
// attributed too. The tests here follow the token from the run inputs, through a real batch run,
// to the ingest that echoes it back, and check the three things that must stay true while it flows:
// an internal run attributes its set but shares no group principal, a restricted run still shares
// within its tenant, and the token itself is never written anywhere an operator could read it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// runRecorder remembers the inputs a backend received, one map per run.
type runRecorder struct {
	mu sync.Mutex
	in []map[string]any
}

func (r *runRecorder) add(m map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.in = append(r.in, m)
}

func (r *runRecorder) runs() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.in...)
}

// recordingBackend is difyServer that also records what each run was sent. A row whose code is
// "fail" is reported failed, so both outcomes can be exercised.
func recordingBackend(t *testing.T, rec *runRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs, _ := body["inputs"].(map[string]any)
		rec.add(inputs)
		if code, _ := inputs["code"].(string); code == "fail" {
			io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"failed","error":"bad code"}}`)
			return
		}
		io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"succeeded"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// createJobAs submits a batch job as the given user and returns its id.
func createJobAs(t *testing.T, s *Server, user string, targetID int64, rows string) int64 {
	t.Helper()
	body := fmt.Sprintf(`{"target_id":%d,"concurrency":1,"max_retries":0,"rows":%s}`, targetID, rows)
	rec := httptest.NewRecorder()
	s.apiBatchJobCreate(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), user)
	if rec.Code != http.StatusOK {
		t.Fatalf("create job as %s: %d %s", user, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return int64(out["job_id"].(float64))
}

// ingestVersioned posts a report with predictions under a named version, the way a workflow echoes
// the token it was given.
func ingestVersioned(t *testing.T, s *Server, symbol, token, version string) (reportID, setID int64) {
	t.Helper()
	body := ingestBody(string(predWire(nil)))
	body = replaceOnce(body, `"symbol":"600160"`, `"symbol":"`+symbol+`"`)
	extra := `,"version":"` + version + `"`
	if token != "" {
		extra += `,"owner_token":"` + token + `"`
	}
	body = strings.TrimSuffix(body, "}") + extra + "}"
	code, m := ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, m)
	}
	id, _ := m["id"].(float64)
	if err := s.st.queryRow("SELECT id FROM prediction_sets WHERE report_id=? ORDER BY id DESC LIMIT 1",
		int64(id)).Scan(&setID); err != nil {
		t.Fatalf("read set: %v", err)
	}
	return int64(id), setID
}

func viewerPrincipals(t *testing.T, st *Store, reportID int64) []string {
	t.Helper()
	rows, err := st.query(`SELECT principal FROM report_viewers WHERE report_id=? ORDER BY principal`, reportID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// tenancyFixture is an internal Default OU, an unrestricted child "Internal", and a restricted
// tenant, each with members, plus a group-visibility version granted to the tenant.
type tenancyFixture struct {
	root, internal, tenant, otherTenant int64
}

func newTenancyFixture(t *testing.T, s *Server) tenancyFixture {
	t.Helper()
	st := s.st
	f := tenancyFixture{root: st.EnsureDefaultGroup()}
	var err error
	if f.internal, err = st.CreateUserGroup("Internal", "", 0); err != nil {
		t.Fatal(err)
	}
	st.SetGroupParent(f.internal, f.root)
	if f.tenant, err = st.CreateUserGroup("tenant", "", 0); err != nil {
		t.Fatal(err)
	}
	st.SetGroupParent(f.tenant, f.root)
	st.SetGroupRestricted(f.tenant, true)
	if f.otherTenant, err = st.CreateUserGroup("other-tenant", "", 0); err != nil {
		t.Fatal(err)
	}
	st.SetGroupParent(f.otherTenant, f.root)
	st.SetGroupRestricted(f.otherTenant, true)

	member := func(name string, ou int64) {
		st.UpsertUser(User{Username: name, PasswordHash: "h", Role: "user"})
		if ou != 0 {
			st.SetPrimaryGroup(name, ou)
		}
	}
	member("staff", 0) // unassigned: the Default OU itself
	member("insider", f.internal)
	member("alice", f.tenant)
	member("bob", f.tenant)
	member("carol", f.otherTenant)

	if err := st.SaveVersion(ReportVersion{Name: "external-edition", Ord: 1, Visibility: VisibilityGroup}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVersionGrants("external-edition", []string{groupPrincipal(f.tenant), groupPrincipal(f.otherTenant)}); err != nil {
		t.Fatal(err)
	}
	return f
}

// An internal run is attributed, but attribution is not sharing: it writes the person's own viewer
// row and no group row, so a restricted tenant whose chain includes the Default OU cannot read an
// internal report just because its version is group-visible and granted to them.
func TestInternalRunAttributesSetButSharesNoGroupPrincipal(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	for i, tc := range []struct {
		user   string
		wantOU int64
	}{
		{"staff", f.root},
		{"insider", f.internal},
	} {
		tok := s.runInputs(BatchJob{CreatedBy: tc.user}, map[string]string{"code": "600519"})[ownerTokenInput]
		if tok == "" {
			t.Fatalf("%s: the run carries no token", tc.user)
		}
		reportID, setID := ingestVersioned(t, s, fmt.Sprintf("60043%d", i), tok, "external-edition")

		if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != tc.wantOU {
			t.Errorf("%s: attributed_ou = %v, want %d", tc.user, got, tc.wantOU)
		}
		if ou, ok := reportOwner(t, s.st, reportID); !ok || ou != tc.wantOU {
			t.Errorf("%s: owner_group = %d (set %v), want %d", tc.user, ou, ok, tc.wantOU)
		}
		got := viewerPrincipals(t, s.st, reportID)
		if len(got) != 1 || got[0] != userPrincipal(tc.user) {
			t.Errorf("%s: viewer principals = %v, want only %s", tc.user, got, userPrincipal(tc.user))
		}
		for _, p := range got {
			if strings.HasPrefix(p, "g:") {
				t.Errorf("%s: an internal run wrote the group principal %s", tc.user, p)
			}
		}
		for _, reader := range []string{"alice", "carol"} {
			if rep, _ := s.st.GetNew(reportID, s.viewerScope(reader)); rep != nil {
				t.Errorf("%s's internal report is readable by restricted tenant member %s", tc.user, reader)
			}
		}
	}
}

// A restricted run keeps sharing within its tenant, and only within it.
func TestRestrictedRunStillSharesWithinTenant(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	tok := s.runInputs(BatchJob{CreatedBy: "alice"}, map[string]string{"code": "600519"})[ownerTokenInput]
	reportID, setID := ingestVersioned(t, s, "600440", tok, "external-edition")

	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != f.tenant {
		t.Errorf("attributed_ou = %v, want %d", got, f.tenant)
	}
	got := viewerPrincipals(t, s.st, reportID)
	want := []string{groupPrincipal(f.tenant), userPrincipal("alice")}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("viewer principals = %v, want %v", got, want)
	}
	if rep, _ := s.st.GetNew(reportID, s.viewerScope("alice")); rep == nil {
		t.Error("the requester cannot read their own run")
	}
	if rep, _ := s.st.GetNew(reportID, s.viewerScope("bob")); rep == nil {
		t.Error("a second member of the tenant cannot read the tenant's report")
	}
	if rep, _ := s.st.GetNew(reportID, s.viewerScope("carol")); rep != nil {
		t.Error("another tenant's member can read the report")
	}
}

// syncBuf is a log sink safe to write from the run goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// dumpAllTables renders every row of every table as text, so a secret that was written to ANY
// column of ANY table is found, not only the tables someone thought to check.
func dumpAllTables(t *testing.T, st *Store) string {
	t.Helper()
	names, err := st.query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	names.Close()

	var out strings.Builder
	for _, tbl := range tables {
		rows, err := st.query("SELECT * FROM " + tbl)
		if err != nil {
			t.Fatalf("dump %s: %v", tbl, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("dump %s: %v", tbl, err)
			}
			fmt.Fprintf(&out, "%s:", tbl)
			for _, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				fmt.Fprintf(&out, " %v", v)
			}
			out.WriteByte('\n')
		}
		rows.Close()
	}
	return out.String()
}

// The token is a credential-shaped value that now rides on every run. It must reach the workflow
// and come back at ingest, and appear nowhere else: not in a log line, not in an audit row, not in a
// stored run, and not in any response body, successful or not.
func TestOwnerTokenNeverAppearsInLogsAuditOrErrorBodies(t *testing.T) {
	logs := &syncBuf{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	rec := &runRecorder{}
	backend := recordingBackend(t, rec)
	post(t, s.apiBatchPluginImport, batchTestSpec)
	added := post(t, s.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, backend.URL))
	jobID := createJobAs(t, s, "staff", int64(added["id"].(float64)), `[{"code":"a"},{"code":"fail"}]`)
	if j := waitForJobDone(t, s.st, jobID); j.Status != "finished" || j.Succeeded != 1 || j.Failed != 1 {
		t.Fatalf("job = %+v, want one succeeded and one failed run", j)
	}

	// Every run reached the workflow with the same kind of verifiable token for the submitter.
	runs := rec.runs()
	if len(runs) != 2 {
		t.Fatalf("backend saw %d runs, want 2", len(runs))
	}
	var tok string
	for _, in := range runs {
		v, _ := in[ownerTokenInput].(string)
		if v == "" {
			t.Fatalf("a run reached the workflow without a token: %v", in)
		}
		if ou, who, ok := s.ownerFromToken(v); !ok || ou != f.root || who != "staff" {
			t.Fatalf("token = (ou %d, user %q, ok %v), want (%d, staff, true)", ou, who, ok, f.root)
		}
		tok = v
	}

	var bodies []string
	ingest := func(body string) {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		w := httptest.NewRecorder()
		s.v1Ingest(w, req)
		bodies = append(bodies, w.Body.String())
	}
	good := `{"symbol":"600450","date":"2026-10-17","subtype":"val","title":"t","body_md":"x","owner_token":"` + tok + `"}`
	ingest(good)
	// The echo path with errors: a bad date, a missing body, an unknown field shape. Each carries the
	// token in the request and must not carry it back in the answer.
	ingest(`{"symbol":"600451","date":"not-a-date","subtype":"val","title":"t","body_md":"x","owner_token":"` + tok + `"}`)
	ingest(`{"symbol":"600452","date":"2026-10-17","subtype":"val","title":"t","owner_token":"` + tok + `"}`)
	ingest(`{"symbol":"600453","date":"2026-10-17","subtype":"val","title":"t","body_md":"x","owner_token":"` + tok + `","predictions":{"schema":"nope"}}`)

	// Positive controls, so a silent capture failure cannot make the scans below pass: the first
	// ingest succeeded and left its audit row and log line, the rejected ones answered with errors.
	if !strings.Contains(bodies[0], `"ok":true`) || strings.Contains(bodies[1], `"ok":true`) {
		t.Fatalf("unexpected ingest outcomes: %v", bodies)
	}
	dump := dumpAllTables(t, s.st)
	for _, want := range []string{AuditReportIngest, "600450", "staff"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("the table dump does not contain %q, so the scan below proves nothing", want)
		}
	}
	if !strings.Contains(logs.String(), "v1 ingest 600450") {
		t.Fatalf("the log capture saw no ingest line, so the scan below proves nothing:\n%s", logs.String())
	}

	for i, b := range bodies {
		if strings.Contains(b, tok) {
			t.Errorf("response %d echoed the owner token: %s", i, b)
		}
	}
	if strings.Contains(logs.String(), tok) {
		t.Errorf("the owner token appears in the log output:\n%s", logs.String())
	}
	for _, line := range strings.Split(dump, "\n") {
		if strings.Contains(line, tok) {
			t.Errorf("the owner token was stored: %.160s", line)
		}
	}
	// The payload half of the token is the person and the OU; neither is a secret, but the signature is.
	sig := tok[strings.LastIndex(tok, ".")+1:]
	if strings.Contains(logs.String(), sig) || strings.Contains(dump, sig) {
		t.Error("the token signature leaked on its own")
	}
}

// P39a keeps hiding the reserved input from the run form and the CSV template while P39b injects it
// into every run: the injection happens to a copy at run time and is never written back to the
// target, the stored rows, or the lists the form and the template are built from.
func TestInjectionLeavesTheRunFormAndTemplateAlone(t *testing.T) {
	s := batchServer(t)
	s.st.EnsureDefaultGroup()
	s.st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"})
	params := paramsWithReserved
	srv := reservedDifyStub(t, &params)

	probe := probeDify(t, s, srv.URL)
	addBody := fmt.Sprintf(`{"name":"T","base_url":%q,"api_key":"k","inputs":%s,"reserved_inputs":["%s"]}`,
		srv.URL, p39aJSON(t, probe["inputs"]), ownerTokenInput)
	added := post(t, s.apiBatchDifyTargetAdd, addBody)
	id := int64(added["id"].(float64))

	inputs := s.runInputs(BatchJob{CreatedBy: "staff"}, map[string]string{"symbol": "600519"})
	if inputs[ownerTokenInput] == "" {
		t.Fatal("the run carries no token")
	}

	code, got := getTarget(t, s.apiBatchDifyTargetGet, id)
	if code != http.StatusOK {
		t.Fatalf("GET target: %d %v", code, got)
	}
	if names := inputNames(t, got["inputs"]); len(names) != 1 || names[0] != "symbol" {
		t.Errorf("the form's input list = %v, want [symbol]", names)
	}
	for _, in := range difyTargetInputs(mustTargetConfig(t, s, id)) {
		if isReservedInput(in.Variable) {
			t.Errorf("the template source lists %q", in.Variable)
		}
	}
	if strings.Contains(dumpAllTables(t, s.st), inputs[ownerTokenInput]) {
		t.Error("the injected token was written back to the database")
	}
}

// A workflow that runs on the Dify-native provider, the path production uses, receives the token as
// an ordinary run input and echoes it back to an ingest that attributes the set. Every other test
// here goes through a manifest plugin; this one pins the real provider, so a change in how the
// native provider assembles its request cannot quietly drop the token.
func TestRunThroughTheDifyNativeProviderCarriesTheToken(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	rec := &runRecorder{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflows/run" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs, _ := body["inputs"].(map[string]any)
		rec.add(inputs)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event":"workflow_started","task_id":"t","workflow_run_id":"run-n","data":{}}`+"\n\n")
		io.WriteString(w, `data: {"event":"workflow_finished","task_id":"t","workflow_run_id":"run-n","data":{"status":"succeeded","outputs":{}}}`+"\n\n")
	}))
	t.Cleanup(stub.Close)

	added := post(t, s.apiBatchDifyTargetAdd, fmt.Sprintf(
		`{"name":"T","base_url":%q,"api_key":"k","inputs":[{"variable":"symbol","type":"text-input","required":true}],"reserved_inputs":["%s"]}`,
		stub.URL, ownerTokenInput))
	targetID := int64(added["id"].(float64))
	if tgt, ok := s.st.GetTarget(targetID); !ok || tgt.PluginSlug != difyPluginSlug {
		t.Fatalf("target %d is not a Dify-native target: %+v", targetID, tgt)
	}
	// A restricted tenant runs only what its allow-list names.
	if err := s.st.SetGroupTargets(f.tenant, []GroupTarget{{TargetID: targetID}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		user   string
		wantOU int64
	}{
		{"staff", f.root},
		{"insider", f.internal},
		{"alice", f.tenant},
	} {
		before := len(rec.runs())
		jobID := createJobAs(t, s, tc.user, targetID, `[{"symbol":"600519"}]`)
		if j := waitForJobDone(t, s.st, jobID); j.Status != "finished" || j.Succeeded != 1 {
			t.Fatalf("%s: job = %+v, want one succeeded run", tc.user, j)
		}
		runs := rec.runs()
		if len(runs) != before+1 {
			t.Fatalf("%s: the workflow saw %d new runs, want 1", tc.user, len(runs)-before)
		}
		in := runs[len(runs)-1]
		if in["symbol"] != "600519" {
			t.Errorf("%s: the run lost its own input: %v", tc.user, in)
		}
		tok, _ := in[ownerTokenInput].(string)
		ou, who, ok := s.ownerFromToken(tok)
		if tok == "" || !ok || ou != tc.wantOU || who != tc.user {
			t.Fatalf("%s: token = (ou %d, user %q, ok %v), want (%d, %q, true)", tc.user, ou, who, ok, tc.wantOU, tc.user)
		}

		// The workflow echoes it to the ingest, which attributes the set to the submitter's OU.
		_, setID := ingestVersioned(t, s, fmt.Sprintf("6005%02d", before), tok, "external-edition")
		if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != tc.wantOU {
			t.Errorf("%s: attributed_ou = %v, want %d", tc.user, got, tc.wantOU)
		}
	}
}

// Attribution is not sharing, and it is not sharing for an unrestricted account either. An admin
// can be placed in a restricted tenant's OU, and the OU alone would then have written the tenant's
// group principal for the admin's runs, handing every tenant member reports the admin never chose to
// share. The report is still attributed to the OU the admin belongs to; only the sharing is withheld.
func TestUnrestrictedSubmitterInARestrictedOUSharesNothingWithTheTenant(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)
	s.st.UpsertUser(User{Username: "boss", PasswordHash: "h", Role: "admin"})
	s.st.SetPrimaryGroup("boss", f.tenant)
	if s.isRestricted("boss") {
		t.Fatal("fixture: an admin must not be restricted")
	}

	tok := s.runInputs(BatchJob{CreatedBy: "boss"}, map[string]string{"code": "600519"})[ownerTokenInput]
	reportID, setID := ingestVersioned(t, s, "600460", tok, "external-edition")

	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != f.tenant {
		t.Errorf("attributed_ou = %v, want the admin's OU %d", got, f.tenant)
	}
	if ou, ok := reportOwner(t, s.st, reportID); !ok || ou != f.tenant {
		t.Errorf("owner_group = %d (set %v), want %d", ou, ok, f.tenant)
	}
	got := viewerPrincipals(t, s.st, reportID)
	if len(got) != 1 || got[0] != userPrincipal("boss") {
		t.Errorf("viewer principals = %v, want only %s", got, userPrincipal("boss"))
	}
	for _, reader := range []string{"alice", "bob", "carol"} {
		if rep, _ := s.st.GetNew(reportID, s.viewerScope(reader)); rep != nil {
			t.Errorf("tenant member %s can read the admin's report", reader)
		}
	}
}

// issuedTokenFor mints a token the way mintOwnerToken does, but as if it had been issued at the given
// instant, so a test can describe a run that started before something else happened.
func issuedTokenFor(s *Server, ou int64, user string, issued time.Time) string {
	msg := fmt.Sprintf("%s|%d|%s|%d", ownerTokenPrefix, ou, user, issued.Add(ownerTokenTTL).Unix())
	return encodeSessionMessage(msg) + "." + s.hmac(msg)
}

// A run outlives its submitter's account: the token stays valid for days, and deleting the account
// cannot stop a workflow that is already executing. The late ingest still attributes the report, but
// it must not put the deleted name back on the viewer list, because the next holder of that name
// would then read a report that was never theirs.
func TestLateIngestAfterTheSubmitterWasDeletedWritesNoViewerRow(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	for i, tc := range []struct {
		user   string
		wantOU int64
	}{
		{"staff", f.root},
		{"alice", f.tenant},
	} {
		tok := s.runInputs(BatchJob{CreatedBy: tc.user}, map[string]string{"code": "600519"})[ownerTokenInput]
		if err := s.st.DeleteUser(tc.user); err != nil {
			t.Fatal(err)
		}
		reportID, setID := ingestVersioned(t, s, fmt.Sprintf("60047%d", i), tok, "external-edition")

		if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != tc.wantOU {
			t.Errorf("%s: attributed_ou = %v, want %d (attribution is kept)", tc.user, got, tc.wantOU)
		}
		if ou, ok := reportOwner(t, s.st, reportID); !ok || ou != tc.wantOU {
			t.Errorf("%s: owner_group = %d (set %v), want %d", tc.user, ou, ok, tc.wantOU)
		}
		if got := viewerPrincipals(t, s.st, reportID); len(got) != 0 {
			t.Errorf("%s: a deleted account's late ingest wrote viewer rows %v", tc.user, got)
		}

		// Somebody else takes the name in the other tenant, which holds the version grant.
		s.st.UpsertUser(User{Username: tc.user, PasswordHash: "h", Role: "user"})
		s.st.SetPrimaryGroup(tc.user, f.otherTenant)
		if rep, _ := s.st.GetNew(reportID, s.viewerScope(tc.user)); rep != nil {
			t.Errorf("the new holder of %q can read the previous holder's report", tc.user)
		}
		if rep, _ := s.st.GetNew(reportID, s.viewerScope("bob")); rep != nil {
			t.Errorf("a tenant member can read a deleted account's report (%s)", tc.user)
		}
	}
}

// The same holds when the name was already taken again by the time the run finished: a token names
// a person, and an account created after the token was issued is not that person.
func TestLateIngestIntoARecreatedAccountWritesNoViewerRow(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)
	stamp := func(at time.Time) {
		t.Helper()
		if _, err := s.st.exec("UPDATE users SET created_at=? WHERE username=?", at.Format("2006-01-02 15:04:05"), "staff"); err != nil {
			t.Fatal(err)
		}
	}

	issued := time.Now().Add(-time.Hour)
	tok := issuedTokenFor(s, f.root, "staff", issued)
	if err := s.st.DeleteUser("staff"); err != nil {
		t.Fatal(err)
	}
	s.st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"}) // a different person, created now
	reportID, setID := ingestVersioned(t, s, "600480", tok, "external-edition")
	if got := viewerPrincipals(t, s.st, reportID); len(got) != 0 {
		t.Errorf("a token issued before the account existed wrote viewer rows %v", got)
	}
	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != f.root {
		t.Errorf("attributed_ou = %v, want %d (attribution is kept)", got, f.root)
	}

	// Control: an account that existed before the token was issued is the person it names.
	stamp(issued.Add(-time.Hour))
	reportID, _ = ingestVersioned(t, s, "600481", tok, "external-edition")
	if got := viewerPrincipals(t, s.st, reportID); len(got) != 1 || got[0] != userPrincipal("staff") {
		t.Errorf("viewer principals = %v, want only %s", got, userPrincipal("staff"))
	}
	// An account whose creation time is unknown (a row written before it was stamped) is trusted
	// as it always was.
	if _, err := s.st.exec("UPDATE users SET created_at=NULL WHERE username=?", "staff"); err != nil {
		t.Fatal(err)
	}
	reportID, _ = ingestVersioned(t, s, "600482", tok, "external-edition")
	if got := viewerPrincipals(t, s.st, reportID); len(got) != 1 || got[0] != userPrincipal("staff") {
		t.Errorf("an unstamped account: viewer principals = %v, want only %s", got, userPrincipal("staff"))
	}
}

// A token that names an OU but nobody attributes the report and its set, and puts nobody on the
// viewer list: there is no "u:" principal for no one.
func TestTokenNamingNobodyAttributesButWritesNoViewerRow(t *testing.T) {
	s := tokenServer(t)
	f := newTenancyFixture(t, s)

	tok := s.mintOwnerToken(f.internal, "")
	if tok == "" {
		t.Fatal("fixture: no token minted for an OU with no person")
	}
	reportID, setID := ingestVersioned(t, s, "600490", tok, "external-edition")
	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != f.internal {
		t.Errorf("attributed_ou = %v, want %d", got, f.internal)
	}
	if got := viewerPrincipals(t, s.st, reportID); len(got) != 0 {
		t.Errorf("viewer principals = %v, want none", got)
	}
}
