package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestUnifiedOUConcurrentMovesCannotCreateCycle(t *testing.T) {
	s := ouAPIServer(t)
	a := int64(postGroup(t, s, `{"name":"concurrent-a"}`)["id"].(float64))
	b := int64(postGroup(t, s, `{"name":"concurrent-b"}`)["id"].(float64))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, pair := range [][2]int64{{a, b}, {b, a}} {
		wg.Go(func() {
			<-start
			p := pair[1]
			_, err := s.writeOUInput(pair[0], groupInput{Name: "concurrent-" + itoa(pair[0]), ParentID: &p}, false)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("reciprocal move failures=%d, want exactly one", failed)
	}
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.graph.policyChain(a); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.graph.policyChain(b); err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedOURejectedSaveIsAtomic(t *testing.T) {
	s := ouAPIServer(t)
	parent := int64(postGroup(t, s, `{"name":"atomic-parent"}`)["id"].(float64))
	child := int64(postGroup(t, s, fmt.Sprintf(`{"name":"atomic-child","parent_id":%d}`, parent))["id"].(float64))
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`)
	w := putGroup(s, parent, fmt.Sprintf(`{"name":"rejected-name","weight":77,"parent_id":%d}`, child))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid move: %d %s", w.Code, w.Body.String())
	}
	if got := scalar[string](t, s.st, `SELECT name FROM user_groups WHERE id=?`, parent); got != "atomic-parent" {
		t.Fatalf("rejected save changed name: %q", got)
	}
	if after := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log`); after != before {
		t.Fatal("rejected save wrote change audit")
	}
}

func TestUnifiedOURejectedCreateLeavesNoRow(t *testing.T) {
	s := ouAPIServer(t)
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups`)
	w := httptest.NewRecorder()
	s.apiGroupAdd(w, httptest.NewRequest(http.MethodPost, "/api/admin/groups", strings.NewReader(`{"name":"invalid-create","parent_id":99999}`)), "admin")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing parent accepted: %d", w.Code)
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups`); got != before {
		t.Fatal("rejected create left an OU")
	}
}

func TestUnifiedOUMembershipRejectsMissingDestination(t *testing.T) {
	s, org, _, _, _ := ouFixture(t)
	if err := s.st.SetPrimaryGroup("ext", org); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPrimaryGroup("ext", 999999); err == nil {
		t.Fatal("unknown destination silently cleared the membership")
	}
	if got := s.st.PrimaryGroupOf("ext"); got != org {
		t.Fatalf("membership changed to %d", got)
	}
	if err := s.st.SetPrimaryGroup("not-present", org); err == nil {
		t.Fatal("missing account accepted")
	}
}

func TestUnifiedOUBulkMembershipIsAtomic(t *testing.T) {
	s, org, team, _, _ := ouFixture(t)
	if _, err := s.st.SetPrimaryGroups([]string{"ext", "missing-account"}, org); err == nil {
		t.Fatal("partially missing batch accepted")
	}
	if got := s.st.PrimaryGroupOf("ext"); got != team {
		t.Fatal("invalid batch partially moved its first member")
	}
	changes, err := s.st.SetPrimaryGroups([]string{"ext"}, org)
	if err != nil || changes["ext"] != team {
		t.Fatalf("valid batch: %v %v", changes, err)
	}
	changes, err = s.st.SetPrimaryGroups([]string{"ext"}, org)
	if err != nil || len(changes) != 0 {
		t.Fatalf("no-op batch reported changes: %v %v", changes, err)
	}
}
