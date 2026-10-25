package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func patchGroup(s *Server, id int64, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/groups/"+itoa(id), strings.NewReader(body))
	r.SetPathValue("id", itoa(id))
	s.apiGroupPatch(w, r, "admin")
	return w
}

func TestOUPatchPreservesOtherPolicyDomains(t *testing.T) {
	s := ouAPIServer(t)
	id := int64(postGroup(t, s, `{"name":"Scoped policy","description":"Directory","weight":7,"allow_urgent":false,"max_queued":4,"daily_run_quota":20,"run_quota_period":"month","totp_enroll":false,"passkey_enroll":true,"require_2fa":true,"forecast_scope":"own","restricted":true}`)["id"].(float64))
	w := patchGroup(s, id, `{"max_queued":9}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if got := scalar[int](t, s.st, `SELECT max_queued FROM user_groups WHERE id=?`, id); got != 9 {
		t.Fatalf("max queued=%d", got)
	}
	if got := scalar[int](t, s.st, `SELECT weight FROM user_groups WHERE id=?`, id); got != 7 {
		t.Fatalf("weight=%d", got)
	}
	if got := scalar[int](t, s.st, `SELECT require_2fa FROM user_groups WHERE id=?`, id); got != 1 {
		t.Fatal("run policy changed security")
	}
	if got := scalar[string](t, s.st, `SELECT forecast_scope FROM user_groups WHERE id=?`, id); got != "own" {
		t.Fatal("run policy changed forecast access")
	}
	if got := scalar[string](t, s.st, `SELECT description FROM user_groups WHERE id=?`, id); got != "Directory" {
		t.Fatal("run policy changed directory")
	}
	w = patchGroup(s, id, `{"totp_enroll":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("inherit: %d %s", w.Code, w.Body.String())
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups WHERE id=? AND totp_enroll IS NULL AND max_queued=9 AND daily_run_quota=20 AND run_quota_period='month'`, id); got != 1 {
		t.Fatal("explicit inheritance changed another policy")
	}
}

func TestOUPatchRejectsInvalidInputAtomically(t *testing.T) {
	s := ouAPIServer(t)
	id := int64(postGroup(t, s, `{"name":"Original","weight":8}`)["id"].(float64))
	for _, body := range []string{`{"unknown":true}`, `{"name":"","weight":3}`, `{"max_queued":"bad"}`, `{"parent_id":999999}`, `{"forecast_scope":"bad"}`} {
		before := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`)
		w := patchGroup(s, id, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		if got := scalar[int](t, s.st, `SELECT weight FROM user_groups WHERE id=?`, id); got != 8 {
			t.Fatal("rejected patch changed policy")
		}
		if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`); got != before {
			t.Fatal("rejected patch wrote audit")
		}
	}
}

func TestOUPatchPreservesUneditedRootNullPolicies(t *testing.T) {
	s := ouAPIServer(t)
	id := s.st.EnsureDefaultGroup()
	if _, err := s.st.db.Exec(`UPDATE user_groups SET weight=NULL,max_queued=NULL,allow_urgent=NULL,daily_run_quota=NULL WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w := patchGroup(s, id, `{"description":"Directory edit only"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups WHERE id=? AND weight IS NULL AND max_queued IS NULL AND allow_urgent IS NULL AND daily_run_quota IS NULL`, id); got != 1 {
		t.Fatal("directory patch normalized unrelated root policies")
	}
}

func TestOUPatchDoesNotMoveALegacyRoot(t *testing.T) {
	s := ouAPIServer(t)
	id := int64(postGroup(t, s, `{"name":"Legacy root"}`)["id"].(float64))
	if _, err := s.st.db.Exec(`UPDATE user_groups SET parent_id=NULL WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w := patchGroup(s, id, `{"max_queued":6}`)
	if w.Code != http.StatusOK {
		t.Fatalf("unrelated patch required a move: %d %s", w.Code, w.Body.String())
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups WHERE id=? AND COALESCE(parent_id,0)=0 AND max_queued=6`, id); got != 1 {
		t.Fatal("patch moved a legacy root")
	}
}

func TestOUConcurrentDomainPatchesCompose(t *testing.T) {
	s := ouAPIServer(t)
	id := int64(postGroup(t, s, `{"name":"Concurrent domains"}`)["id"].(float64))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, body := range []string{`{"max_queued":12}`, `{"description":"Updated directory"}`} {
		wg.Go(func() {
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &patch); err != nil {
				errs <- err
				return
			}
			<-start
			_, err := s.patchOUInput(id, patch)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups WHERE id=? AND max_queued=12 AND description='Updated directory'`, id); got != 1 {
		t.Fatal("concurrent settings pages overwrote each other")
	}
}
