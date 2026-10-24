package app

import (
	"database/sql"
	"testing"
)

type ouCountingExec struct {
	execer
	queries int
}

func (e *ouCountingExec) Query(q string, args ...any) (*sql.Rows, error) {
	e.queries++
	return e.execer.Query(q, args...)
}
func (e *ouCountingExec) QueryRow(q string, args ...any) *sql.Row {
	e.queries++
	return e.execer.QueryRow(q, args...)
}

func TestUnifiedOUSnapshotReadsAreIndependentOfDepth(t *testing.T) {
	st := newTestStore(t)
	root := st.EnsureDefaultGroup()
	parent := root
	for i := 0; i < 12; i++ {
		id, err := st.CreateUserGroup("depth-"+itoa(int64(i)), "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetGroupParent(id, parent); err != nil {
			t.Fatal(err)
		}
		parent = id
	}
	if err := st.UpsertUser(User{Username: "snapshot-member", Role: "user", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPrimaryGroup("snapshot-member", parent); err != nil {
		t.Fatal(err)
	}
	e := &ouCountingExec{execer: st.db}
	sub, snap, err := st.loadOUPolicySubjectFrom(e, "snapshot-member")
	if err != nil {
		t.Fatal(err)
	}
	if sub.AssignedOU != parent || sub.EffectiveOU != parent || e.queries != 2 {
		t.Fatalf("subject=%+v queries=%d", sub, e.queries)
	}
	chain, err := snap.graph.policyChain(sub.EffectiveOU)
	if err != nil || len(chain) != 13 {
		t.Fatalf("chain=%v err=%v", chain, err)
	}
	if e.queries != 2 {
		t.Fatal("traversal performed per-parent reads")
	}
	if _, _, err := st.loadOUPolicySubjectFrom(e, "missing-member"); err == nil {
		t.Fatal("missing account became a Default member")
	}
}

func TestUnifiedOUSnapshotResolvesTypedPolicies(t *testing.T) {
	s, org, team, _, _ := ouFixture(t)
	st := s.st
	if _, err := st.exec(`UPDATE user_groups SET max_queued=3,totp_enroll=1 WHERE is_default=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`UPDATE user_groups SET max_queued=9,totp_enroll=0,daily_run_quota=20,run_quota_period='month',require_2fa=1 WHERE id=?`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`UPDATE user_groups SET daily_run_quota=0,run_quota_period=NULL,require_2fa=0 WHERE id=?`, team); err != nil {
		t.Fatal(err)
	}
	sub, snap, err := st.loadOUPolicySubject("ext")
	if err != nil {
		t.Fatal(err)
	}
	p, err := snap.resolve(sub.EffectiveOU)
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxQueued.Effective != 9 || p.MaxQueued.SourceOU != org || p.TOTP.Effective || p.TOTP.SourceOU != org {
		t.Fatalf("nearest parent disagreement: %+v", p)
	}
	if p.Quota.Effective.Limit != 0 || p.Quota.Effective.Period != QuotaDay || p.Quota.SourceOU != team {
		t.Fatalf("quota was split: %+v", p.Quota)
	}
	if !p.Require2FA.Effective || p.Require2FA.SourceOU != org {
		t.Fatalf("sticky mandate relaxed: %+v", p.Require2FA)
	}
}

func TestUnifiedOUSecurityRejectsTruncatedChain(t *testing.T) {
	s, org, team, _, _ := ouFixture(t)
	if err := s.st.SetGroupParent(org, team); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SecuritySettings("ext"); err == nil {
		t.Fatal("cycle was shortened into a successful security policy")
	}
}
