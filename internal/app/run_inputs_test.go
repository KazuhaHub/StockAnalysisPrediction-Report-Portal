package app

import (
	"fmt"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// TestRunInputsInjectsOwnerTokenForEveryRun proves the owner-attribution token is added to the
// Dify inputs of every run that has a submitter, internal and external alike (ADR 0022 R1, amended
// by P39b), so every prediction set can be attributed to the OU its run belonged to. A run with no
// submitter stays unattributed, a caller-supplied reserved value never survives, and the caller's
// map is never mutated.
func TestRunInputsInjectsOwnerTokenForEveryRun(t *testing.T) {
	st := newTestStore(t)
	root := st.EnsureDefaultGroup()
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}

	// An internal OU: a plain child of Default, not restricted.
	internal, err := st.CreateUserGroup("Internal", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	st.SetGroupParent(internal, root)

	// A restricted tenant OU.
	org, _ := st.CreateUserGroup("ext-org", "", 0)
	st.SetGroupParent(org, root)
	st.SetGroupRestricted(org, true)

	st.UpsertUser(User{Username: "staff", PasswordHash: "h", Role: "user"}) // unassigned: Default
	st.UpsertUser(User{Username: "insider", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("insider", internal)
	st.UpsertUser(User{Username: "ext", PasswordHash: "h", Role: "user"})
	st.SetPrimaryGroup("ext", org)
	st.UpsertUser(User{Username: "boss", PasswordHash: "h", Role: "admin"})

	base := map[string]string{"symbol": "600519"}
	for _, tc := range []struct {
		name, user string
		wantOU     int64
	}{
		{"internal member of Default", "staff", root},
		{"internal member of Internal", "insider", internal},
		{"restricted member", "ext", org},
		{"admin submitter", "boss", root},
	} {
		got := s.runInputs(BatchJob{CreatedBy: tc.user}, base)
		tok := got[ownerTokenInput]
		if tok == "" {
			t.Errorf("%s: the run carries no owner token", tc.name)
			continue
		}
		if ou, who, ok := s.ownerFromToken(tok); !ok || ou != tc.wantOU || who != tc.user {
			t.Errorf("%s: token = (ou %d, user %q, ok %v), want (%d, %q, true)", tc.name, ou, who, ok, tc.wantOU, tc.user)
		}
		if got["symbol"] != "600519" || len(got) != 2 {
			t.Errorf("%s: inputs = %v, want the caller's inputs plus the token", tc.name, got)
		}
	}

	// No submitter: nothing to attribute, so no token (and no "u:" principal for nobody later).
	got := s.runInputs(BatchJob{CreatedBy: ""}, base)
	if _, ok := got[ownerTokenInput]; ok {
		t.Error("a run with no submitter must not carry an owner token")
	}
	if got["symbol"] != "600519" || len(got) != 1 {
		t.Errorf("no-submitter inputs = %v, want the caller's inputs unchanged", got)
	}

	// The caller's map is never mutated.
	if len(base) != 1 {
		t.Errorf("runInputs mutated the caller's inputs map: %v", base)
	}

	// A caller-supplied reserved value is replaced by the minted one, and any other reserved key is
	// dropped: for an internal and for a restricted submitter alike.
	for _, user := range []string{"staff", "ext"} {
		forged := map[string]string{"symbol": "600519", ownerTokenInput: "forged", "_rp_other": "x"}
		got := s.runInputs(BatchJob{CreatedBy: user}, forged)
		if got[ownerTokenInput] == "" || got[ownerTokenInput] == "forged" {
			t.Errorf("%s: the caller's reserved value survived: %q", user, got[ownerTokenInput])
		}
		if _, ok := got["_rp_other"]; ok {
			t.Errorf("%s: an unknown reserved input was forwarded", user)
		}
		if forged[ownerTokenInput] != "forged" || len(forged) != 3 {
			t.Errorf("%s: runInputs mutated the caller's map: %v", user, forged)
		}
	}
	// And with no submitter the forged value is dropped outright, not forwarded.
	if got := s.runInputs(BatchJob{}, map[string]string{ownerTokenInput: "forged"}); len(got) != 0 {
		t.Errorf("a forged reserved value reached an unattributed run: %v", got)
	}
}

// TestTokenMintFailureLeavesRunUnattributed proves a mint refusal never blocks the run: with no
// usable secret the inputs simply carry no token, and the ingest that follows leaves the report and
// its prediction set unattributed. A restricted submitter is included because that is the case the
// pre-change code did mint for, with an empty key.
func TestTokenMintFailureLeavesRunUnattributed(t *testing.T) {
	s := tokenServer(t)
	newTenancyFixture(t, s)
	s.cfg.SecretKey = "" // the secret goes missing after the portal started

	for i, user := range []string{"staff", "alice"} {
		got := s.runInputs(BatchJob{CreatedBy: user}, map[string]string{"symbol": "600519"})
		if _, ok := got[ownerTokenInput]; ok {
			t.Fatalf("%s: a token was minted with no secret", user)
		}
		if got["symbol"] != "600519" {
			t.Errorf("%s: the run lost its own inputs: %v", user, got)
		}

		// The workflow has nothing to echo, so the ingest carries no owner_token.
		reportID, setID := ingestWithToken(t, s, fmt.Sprintf("60041%d", i), got[ownerTokenInput])
		if ou, ok := reportOwner(t, s.st, reportID); ok {
			t.Errorf("%s: owner_group = %d, want unset", user, ou)
		}
		if v := attributedOU(t, s.st, setID); v.Valid {
			t.Errorf("%s: attributed_ou = %d, want NULL", user, v.Int64)
		}
	}

	// The run itself still starts and finishes: a real batch job reaches the backend with its own
	// inputs and no reserved key.
	rec := &runRecorder{}
	backend := recordingBackend(t, rec)
	post(t, s.apiBatchPluginImport, batchTestSpec)
	added := post(t, s.apiBatchTargetAdd, fmt.Sprintf(`{"plugin_slug":"p","name":"t","config":{"base_url":%q}}`, backend.URL))
	jobID := createJobAs(t, s, "staff", int64(added["id"].(float64)), `[{"code":"a"}]`)
	if j := waitForJobDone(t, s.st, jobID); j.Status != "finished" || j.Succeeded != 1 {
		t.Fatalf("job = %+v, want one succeeded run", j)
	}
	runs := rec.runs()
	if len(runs) != 1 || runs[0]["code"] != "a" {
		t.Fatalf("backend saw %v, want the one run", runs)
	}
	if _, ok := runs[0][ownerTokenInput]; ok {
		t.Errorf("the backend received a token that was never minted: %v", runs[0])
	}
}
