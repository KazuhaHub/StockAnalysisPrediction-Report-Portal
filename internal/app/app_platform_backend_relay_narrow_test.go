package app

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// SQLite has a single connection, so an open transaction is exactly one connection in use.
func platformConnectionHeld(s *Server) bool { return s.st.db.Stats().InUse != 0 }

// recordPlatformPureSteps installs the seam that reports every CPU-bound step of a backend
// admission together with whether the database connection was held at that moment.
func recordPlatformPureSteps(t *testing.T, s *Server) func() map[string]int {
	t.Helper()
	steps := map[string]int{}
	var inTransaction []string
	platformBackendPureStepHook = func(step string) {
		steps[step]++
		if platformConnectionHeld(s) {
			inTransaction = append(inTransaction, step)
		}
	}
	t.Cleanup(func() { platformBackendPureStepHook = nil })
	return func() map[string]int {
		t.Helper()
		if len(inTransaction) != 0 {
			t.Fatalf("CPU-bound steps ran while the database transaction was open: %v", inTransaction)
		}
		return steps
	}
}

// The predicate the seam relies on must be able to fail: it reports a held connection inside a
// transaction and a free one outside.
func TestPlatformConnectionHeldSeesAnOpenTransaction(t *testing.T) {
	s, _, _ := platformWritableAccessFixture(t, false)
	if platformConnectionHeld(s) {
		t.Fatal("idle store reports a held connection")
	}
	tx, err := s.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	held := platformConnectionHeld(s)
	_ = tx.Rollback()
	if !held || platformConnectionHeld(s) {
		t.Fatal("transaction not visible to the seam predicate")
	}
}

func TestPlatformBackendAccessRunsCPUWorkAfterTheTransaction(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	observed := recordPlatformPureSteps(t, s)
	var readUnderTransaction bool
	a, err := s.withPlatformWritableBackendAccess(context.Background(), actor, lease.installationID, func(platformWritableBackendAccess, []byte) error {
		readUnderTransaction = platformConnectionHeld(s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if readUnderTransaction {
		t.Fatal("the signing callback ran while the transaction was open")
	}
	steps := observed()
	want := map[string]int{"release-verify": 2, "history-build": 1, "identity-verify": 1}
	for step, n := range want {
		if steps[step] != n {
			t.Fatalf("step %q ran %d times, want %d (all steps: %v)", step, steps[step], n, steps)
		}
	}
}

// Access decisions come from one snapshot, but a request spans several admissions. Whatever
// changes between them must be seen by the next admission, including with every verified
// registration already remembered.
func TestPlatformBackendAccessSeesChangesBetweenAdmissions(t *testing.T) {
	fixture := func(t *testing.T) (*Server, platformActorSession, string) {
		s, actor, lease := platformWritableAccessFixture(t, false)
		return s, actor, lease.installationID
	}
	for _, mode := range []string{"session", "grant", "publisher", "policy", "fence", "namespace", "role", "revoke-key"} {
		t.Run(mode, func(t *testing.T) { testPlatformBackendAccessSeesChange(t, fixture, mode, "role", "policy") })
	}
}

func TestPlatformBackendAccessGeneration2SeesChangesBetweenAdmissions(t *testing.T) {
	for _, mode := range []string{"session", "grant", "publisher", "fence", "role", "revoke-key"} {
		// The update history does not bind the control fence to the completed operation, so a fence
		// move is admitted there and refused by the access comparison, like a role change.
		t.Run(mode, func(t *testing.T) {
			testPlatformBackendAccessSeesChange(t, platformGeneration2AccessFixture, mode, "role", "fence")
		})
	}
}

func testPlatformBackendAccessSeesChange(t *testing.T, fixture func(*testing.T) (*Server, platformActorSession, string), mode string, admitted ...string) {
	s, actor, installation := fixture(t)
	first, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// A second admission with nothing changed is accepted and equal: the baseline.
	again, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil || !samePlatformWritableBackendAccess(first, again) {
		t.Fatal("unchanged state must admit an equal access", err)
	}
	again.Close()
	switch mode {
	case "session":
		mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, actor.sessionID)
	case "grant":
		mustExec(t, s.st, `DELETE FROM app_platform_access_grants`)
	case "publisher":
		mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
	case "policy":
		mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2`)
	case "fence":
		mustExec(t, s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1`)
	case "namespace":
		mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET state='retained'`)
	case "role":
		mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	case "revoke-key":
		publisher := scalar[string](t, s.st, `SELECT id FROM app_platform_publishers LIMIT 1`)
		revision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_publishers WHERE id=?`, publisher)
		key := scalar[string](t, s.st, `SELECT key_id FROM app_platform_publisher_keys WHERE publisher_id=? LIMIT 1`, publisher)
		keyRevision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_publisher_keys WHERE publisher_id=? AND key_id=?`, publisher, key)
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "revoke_key", publisherID: publisher, expectedRevision: revision, keyID: key, expectedKeyRevision: keyRevision}); err != nil {
			t.Fatal(err)
		}
	}
	next, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err == nil {
		defer next.Close()
		// Some changes are admitted but must never compare equal to the first access: the
		// relay refuses the request on exactly that comparison.
		if !slices.Contains(admitted, mode) {
			t.Fatalf("%s change was admitted", mode)
		}
		if samePlatformWritableBackendAccess(first, next) {
			t.Fatalf("%s change compares equal to the earlier access", mode)
		}
		return
	}
	if !reflect.DeepEqual(next, platformWritableBackendAccess{}) {
		t.Fatal("refused access returned private material")
	}
}

// A signed registration that is remembered must still be refused once the key that verified it is
// no longer the authority: the runtime binding's key or control revision moved.
func TestPlatformBackendAccessRefusesRotatedRuntimeKeyWithWarmVerificationCache(t *testing.T) {
	for _, mode := range []string{"control_revision", "fingerprint", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			s, actor, lease := platformWritableAccessFixture(t, false)
			a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
			if err != nil {
				t.Fatal(err)
			}
			a.Close()
			hits, _, _ := control.VerifyCacheStats()
			if hits == 0 {
				t.Fatal("verified registrations are not being remembered")
			}
			switch mode {
			case "control_revision":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET control_revision=control_revision+1`)
			case "fingerprint":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET verification_key_fingerprint=?`, platformDigest([]byte("rotated")))
			case "disabled":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			}
			if b, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID); err == nil {
				b.Close()
				t.Fatal("a rotated or disabled runtime key was admitted from remembered verifications")
			}
		})
	}
}

// platformGeneration2AccessFixture is a completed managed update: generation 2, admitted through
// the update-start registration history rather than the initial install's.
func platformGeneration2AccessFixture(t *testing.T) (*Server, platformActorSession, string) {
	t.Helper()
	s, lease, intent, endpoint, observation := platformWritableUpdateStartRegistrationCommitFixture(t, false)
	next, err := s.commitPlatformUpdateStartRegistration(context.Background(), lease, intent, endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
	defer applied.registration.Close()
	if _, err := s.commitPlatformUpdateStartActivation(context.Background(), next, applied, boot); err != nil {
		t.Fatal(err)
	}
	return s, platformManagedFixtureSession(t, s), lease.installationID
}

func TestPlatformBackendAccessGeneration2RunsCPUWorkAfterTheTransaction(t *testing.T) {
	s, actor, installation := platformGeneration2AccessFixture(t)
	observed := recordPlatformPureSteps(t, s)
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.intent.generation != 2 {
		t.Fatal("fixture did not admit the updated generation")
	}
	steps := observed()
	want := map[string]int{"release-verify": 2, "history-build": 1, "identity-verify": 1}
	for step, n := range want {
		if steps[step] != n {
			t.Fatalf("step %q ran %d times, want %d (all steps: %v)", step, steps[step], n, steps)
		}
	}
}
