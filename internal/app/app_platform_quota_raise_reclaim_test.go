package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A lease re-claimed through the real claim path raises the installation fence. The raise row admitted under the old
// fence is reused, and the update goes on to complete.
func testUpdateRaiseSurvivesALeaseReclaimWithAHigherFence(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); err == nil {
		t.Fatal("an unanswered raise succeeded")
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, lease.id)
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7850))
	if err != nil || selected.lease.id != lease.id || selected.lease.fence <= lease.fence {
		t.Fatalf("the re-claim did not raise the fence: %v %d/%d", err, selected.lease.fence, lease.fence)
	}
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	next, err := s.runPlatformManagedUpdate(context.Background(), selected.lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart {
		t.Fatalf("the raise did not commit under the new lease: %v %v", next.snapshot, err)
	}
	if len(raise.sent) != 2 || raise.sent[0].effectID != raise.sent[1].effectID || raise.sent[0].command.PayloadSHA256 != raise.sent[1].command.PayloadSHA256 {
		t.Fatal("the re-claim did not reuse the effect")
	}
	platformCompleteUpdateFromStart(t, s, next, endpoint)
}

func TestUpdateRaiseSurvivesALeaseReclaimWithAHigherFenceSQLite(t *testing.T) {
	testUpdateRaiseSurvivesALeaseReclaimWithAHigherFence(t, false)
}
func TestUpdateRaiseSurvivesALeaseReclaimWithAHigherFencePostgres(t *testing.T) {
	testUpdateRaiseSurvivesALeaseReclaimWithAHigherFence(t, true)
}

// The same holds for a reviewed raise.
func testSetDataQuotaSurvivesALeaseReclaimWithAHigherFence(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	endpoint := platformCheckpointEndpointFixture()
	raise := &platformFakeRaiseRunner{t: t, endpoint: endpoint}
	raise.install(t)
	installation := enabled.installationID
	namespace := platformNamespaceOf(t, s, installation)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	current := platformSetDataQuotaAtPrepare(t, s, actor, installation, target, runtime, bytes0*2, inodes0, 1700)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	if _, err := s.dispatchPlatformSetDataQuota(context.Background(), current, endpoint); err == nil {
		t.Fatal("an unanswered raise succeeded")
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, current.id)
	selected, err := s.claimNextPlatformSetDataQuota(context.Background(), platformFixtureID(1710))
	if err != nil || selected.lease.id != current.id {
		t.Fatal("re-claim", err)
	}
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	done, err := s.runPlatformSetDataQuota(context.Background(), selected.lease, endpoint)
	if err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the raise did not complete under the new lease: %v %v", done.snapshot, err)
	}
}

func TestSetDataQuotaSurvivesALeaseReclaimWithAHigherFenceSQLite(t *testing.T) {
	testSetDataQuotaSurvivesALeaseReclaimWithAHigherFence(t, false)
}
func TestSetDataQuotaSurvivesALeaseReclaimWithAHigherFencePostgres(t *testing.T) {
	testSetDataQuotaSurvivesALeaseReclaimWithAHigherFence(t, true)
}
