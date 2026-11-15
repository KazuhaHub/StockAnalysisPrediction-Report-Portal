package app

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// platformServiceHistoryFor reads one installation's active tuple exactly as the
// service task preflight does and asks for the history a task would run against.
func platformServiceHistoryFor(t *testing.T, s *Server, installation string) (platformServiceBackendHistory, error) {
	t.Helper()
	h, _, err := platformServiceHistoryWithSeed(t, s, installation)
	return h, err
}

// platformServiceHistoryWithSeed also returns the controller signing seed the
// task path signs its command with; the caller clears it.
func platformServiceHistoryWithSeed(t *testing.T, s *Server, installation string) (platformServiceBackendHistory, []byte, error) {
	t.Helper()
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	var b platformBackendHistoryBinding
	b.installation = installation
	var generation int64
	if err := e.queryRow(`SELECT i.generation,i.controller_epoch,i.control_fence,
		a.id,a.release_sha256,a.data_namespace_id,a.data_format,a.observed_ms,a.readiness_evidence
		FROM app_platform_installations i
		JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
		WHERE i.id=?`, installation).Scan(&generation, &b.epoch, &b.fence,
		&b.activation, &b.release, &b.namespace, &b.format, &b.observed,
		&b.readiness); err != nil {
		t.Fatal(err)
	}
	b.generation = generation
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(dek)
	runtime, seed, err := s.platformDataRuntime(e, b.epoch, dek)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.loadPlatformServiceBackendHistory(e, b, runtime, seed, dek, now)
	return h, seed, err
}

func requireServiceHistory(t *testing.T, h platformServiceBackendHistory, format string, initial, update bool, writable ...bool) {
	t.Helper()
	defer h.Close()
	if h.format != format || (h.initial != nil) != initial || (h.update != nil) != update || (h.writable != nil) != (len(writable) > 0 && writable[0]) ||
		len(h.raw) == 0 || len(h.signature) == 0 || len(h.descriptor) == 0 ||
		h.runtime.id == "" || h.authority.DeploymentID == "" {
		t.Fatalf("service history: format=%q initial=%v update=%v", h.format, h.initial != nil, h.update != nil)
	}
	if _, err := h.registration.Metadata(); err != nil {
		t.Fatal("service history registration:", err)
	}
}

// The managed generation-1 path keeps the initial registration the runner verifies.
func testPlatformServiceHistoryManagedGenerationOne(t *testing.T, pg bool) {
	s, _, lease := platformManagedAccessFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.BackendRegistrationFormat, true, false)
}

// A writable generation-1 install resolves the writable registration the runner
// verifies as a third kind. It is neither mis-read as managed (the managed reader's
// effect count) nor refused.
func testPlatformServiceHistoryWritableGenerationOneUsesWritableKind(t *testing.T, pg bool) {
	s, _, lease := platformWritableAccessFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.WritableBackendRegistrationFormat, false, false, true)
}

// Generation 2 after an update selects the update reader.
func testPlatformServiceHistoryGenerationTwoAfterUpdate(t *testing.T, pg bool) {
	s, lease, _ := platformUpdateStartActivatedFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// Generation 2 after a completed restore selects the restore reader; the update
// reader only reads completed update operations.
func testPlatformServiceHistoryGenerationTwoAfterRestore(t *testing.T, pg bool) {
	s, lease, _ := platformRestoreStartActivatedFixture(t, pg)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='restore' AND stage='complete'`, lease.installationID) != 1 {
		t.Fatal("fixture: no completed restore")
	}
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// Generation 2 after a completed resume selects the resume reader.
func testPlatformServiceHistoryGenerationTwoAfterResume(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	commit := f.reviewAndConsumeResume(t)
	startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	platformResumeStartToReady(t, f.s, startLease, f.endpoint)
	h, err := platformServiceHistoryFor(t, f.s, f.installation)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// A plan with a validly digested but unknown format is never guessed to be managed.
func testPlatformServiceHistoryUnknownPlanFormatIsRefused(t *testing.T, pg bool) {
	s, _, lease := platformManagedAccessFixture(t, pg)
	plan := `{"format":"portal.unknown/1"}`
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, platformDigest([]byte(plan)), lease.id)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err == nil {
		h.Close()
		t.Fatal("an unknown plan format resolved a service-task history")
	}
	if !errors.Is(err, errAppPlatformTaskUnavailable) ||
		!errors.Is(err, errPlatformUnknownServiceTaskPlan) {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

func TestPlatformServiceHistoryManagedGenerationOneSQLite(t *testing.T) {
	testPlatformServiceHistoryManagedGenerationOne(t, false)
}
func TestPlatformServiceHistoryManagedGenerationOnePostgres(t *testing.T) {
	testPlatformServiceHistoryManagedGenerationOne(t, true)
}

func TestPlatformServiceHistoryWritableGenerationOneUsesWritableKindSQLite(t *testing.T) {
	testPlatformServiceHistoryWritableGenerationOneUsesWritableKind(t, false)
}
func TestPlatformServiceHistoryWritableGenerationOneUsesWritableKindPostgres(t *testing.T) {
	testPlatformServiceHistoryWritableGenerationOneUsesWritableKind(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterUpdateSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterUpdate(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterUpdatePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterUpdate(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterRestoreSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterRestore(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterRestorePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterRestore(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterResumeSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterResume(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterResumePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterResume(t, true)
}

func TestPlatformServiceHistoryUnknownPlanFormatIsRefusedSQLite(t *testing.T) {
	testPlatformServiceHistoryUnknownPlanFormatIsRefused(t, false)
}
func TestPlatformServiceHistoryUnknownPlanFormatIsRefusedPostgres(t *testing.T) {
	testPlatformServiceHistoryUnknownPlanFormatIsRefused(t, true)
}

// serviceTaskResources is what the grant of each handler carries.
var serviceTaskResources = map[string][]string{
	appplatform.TaskHandlerBackend: {"tasks.schedule"},
	appplatform.TaskHandlerDigest:  {"reports.read", "tasks.schedule"},
	appplatform.TaskHandlerEvent:   {"events.subscribe", "tasks.schedule"},
}

// fakeRunnerTaskAfterGrantChange raises (this fixture does not re-grant the capability rows; the Portal's own
// pre-check for that is TestPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChange) ... bumps the installation's policy revision `bumps` times without a
// new generation (as a recovery action does), then drives one task of the handler through the
// Portal and runner contracts without a socket: the Portal resolves the history it would use,
// the registration signer answers the handshake as the app would, the Portal signs the task
// with its controller key, a fake runner verifies it exactly as the runner does
// (control.VerifyServiceBackendTask and ValidateServiceBackendTaskData) and answers on the task
// wire, and the Portal decodes the answer. It returns the Portal's outcome, the runner-side
// registration identity and the history format.
func fakeRunnerTaskAfterGrantChange(t *testing.T, s *Server, installation, handler string, bumps int) (appplatform.TaskExecutionResult, control.ServiceBackendTaskRegistrationIdentity, string) {
	t.Helper()
	if bumps > 0 {
		tx, err := s.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		e := migExec{s: s.st, ex: tx}
		for range bumps {
			if err := invalidateAppPlatformPrincipal(e, &platformPrincipalImpact{installations: []string{installation}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		// The history reader refuses rows stamped in the database clock's future; a task never
		// follows a grant change within the same millisecond, so keep the fixture off that edge.
		time.Sleep(30 * time.Millisecond)
	}
	h, seed, err := platformServiceHistoryWithSeed(t, s, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	defer clear(seed)
	parsed, err := appplatform.ParseDescriptor(h.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var generation, policy int64
	var epoch string
	if err := s.st.queryRow(`SELECT controller_epoch,generation,policy_revision FROM app_platform_installations WHERE id=?`, installation).Scan(&epoch, &generation, &policy); err != nil {
		t.Fatal(err)
	}
	// The Portal's handshake: challenge, the app's answer, verification.
	signer, err := h.registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	answer, err := signer.AnswerChallenge(challenge, "boot-fake", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	boot, err := signer.VerifyAnswer(challenge, answer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	leaseID, taskID := platformFixtureID(7301), platformFixtureID(7302)
	identity, err := appplatform.SignServiceIdentity(bytes.Repeat([]byte{7}, 32), appplatform.ServiceIdentityGrant{
		Format: appplatform.ServiceIdentityFormat, LeaseID: leaseID, AppID: parsed.Manifest.ID,
		InstallationID: installation, ControllerEpoch: epoch, Generation: strconv.FormatInt(generation, 10),
		PolicyRevision: strconv.FormatInt(policy, 10), ReleaseSHA256: parsed.SHA256,
		Resources: serviceTaskResources[handler], IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+60000, 10),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	if handler == appplatform.TaskHandlerEvent {
		body, _, err = appplatform.BuildEventDelivery(platformFixtureID(7304), taskID, appplatform.EventReportIngested, "report", "41", now, []byte(`{"id":41}`))
	} else {
		body, err = appplatform.EncodeTaskBackendRequest(taskID, "grant-change:1", []byte(`{"fixture":true}`))
	}
	if err != nil {
		t.Fatal(err)
	}
	command := control.ServiceBackendTaskCommand{
		Format: control.ServiceBackendTaskFormat, RequestID: platformFixtureID(7303), TaskID: taskID,
		LeaseID: leaseID, Handler: handler, InstallationID: installation,
		Generation: strconv.FormatInt(generation, 10), PolicyRevision: strconv.FormatInt(policy, 10),
		ControllerEpoch: epoch, ReleaseSHA256: parsed.SHA256,
		BootID: boot, RegistrationRaw: bytes.Clone(h.raw), RegistrationSignature: bytes.Clone(h.signature),
		ServiceIdentity: identity, BodySHA256: platformDigest(body),
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignServiceBackendTask(command, key, h.authority, now)
	if err != nil {
		t.Fatalf("%s task refused when signed (policy revision %d): %v", handler, policy, err)
	}
	// The fake runner: verify as the runner does, then answer on the task wire.
	verified, err := control.VerifyServiceBackendTask(raw, signature, h.authority, now)
	if err != nil {
		t.Fatalf("%s task refused by the runner's verifier (policy revision %d): %v", handler, policy, err)
	}
	if err := control.ValidateServiceBackendTaskData(verified, body); err != nil {
		t.Fatal("task body refused by the runner:", err)
	}
	reply := []byte(`{"format":"portal.app-task.response/1-dev.1","outcome":"succeeded","result":{"ran":true}}`)
	result, err := appplatform.DecodeTaskBackendResponse(reply)
	if err != nil {
		t.Fatal(err)
	}
	return result, verified.RegistrationIdentity(), h.format
}

// testFakeRunnerTaskAfterGrantChange runs each handler, before and after the revision is raised, on
// a fresh fixture each: one installation resolves its history once.
func testFakeRunnerTaskAfterGrantChange(t *testing.T, fixture func() (*Server, string), wantFormat string, bumpCounts ...int) {
	t.Helper()
	for _, handler := range []string{appplatform.TaskHandlerBackend, appplatform.TaskHandlerDigest, appplatform.TaskHandlerEvent} {
		for _, bumps := range bumpCounts {
			s, installation := fixture()
			before := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
			result, identity, format := fakeRunnerTaskAfterGrantChange(t, s, installation, handler, bumps)
			after := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
			if (bumps == 1) != (after > before) {
				t.Fatal("policy revision", before, "->", after, "with", bumps, "grant change(s)")
			}
			if result.Outcome != appplatform.TaskResultSucceeded || format != wantFormat ||
				identity.Format != wantFormat || identity.PolicyRevision != strconv.FormatInt(after, 10) {
				t.Fatal(handler, "outcome", result.Outcome, "format", format, identity, "revision", after)
			}
		}
	}
}

// A policy revision raised at generation 1, then a task: the runner must still accept the task of a
// managed install at the new policy revision, for the backend, digest and event handlers.
func testPlatformServiceTaskAfterGrantChangeManaged(t *testing.T, pg bool) {
	testFakeRunnerTaskAfterGrantChange(t, func() (*Server, string) {
		s, _, lease := platformManagedAccessFixture(t, pg)
		return s, lease.installationID
	}, control.BackendRegistrationFormat, 0, 1)
}

// The same for a writable generation-1 install, through the writable kind.
func testPlatformServiceTaskAfterGrantChangeWritable(t *testing.T, pg bool) {
	testFakeRunnerTaskAfterGrantChange(t, func() (*Server, string) {
		s, _, lease := platformWritableAccessFixture(t, pg)
		return s, lease.installationID
	}, control.WritableBackendRegistrationFormat, 0, 1)
}

// An updated app is at generation 2: its tasks reach the runner on the update-start kind.
// Only the no-change case runs here: the update, restore and resume history readers pin the
// installation's policy revision to the update's target revision (doc 170), so after a grant
// change the Portal refuses to resolve the history before any runner is involved. The runner
// accepts the later revision (TestServiceBackendTaskUpdateGenerationAcceptsPolicyRevisionAfterGrantChange).
func testPlatformServiceTaskAfterGrantChangeGenerationTwo(t *testing.T, pg bool) {
	testFakeRunnerTaskAfterGrantChange(t, func() (*Server, string) {
		s, lease, _ := platformUpdateStartActivatedFixture(t, pg)
		if g := scalar[int](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, lease.installationID); g != 2 {
			t.Fatal("fixture is not at generation 2", g)
		}
		return s, lease.installationID
	}, control.UpdateStartRegistrationFormat, 0)
}

func TestPlatformServiceTaskAfterGrantChangeManagedSQLite(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeManaged(t, false)
}
func TestPlatformServiceTaskAfterGrantChangeManagedPostgres(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeManaged(t, true)
}
func TestPlatformServiceTaskAfterGrantChangeWritableSQLite(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeWritable(t, false)
}
func TestPlatformServiceTaskAfterGrantChangeWritablePostgres(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeWritable(t, true)
}
func TestPlatformServiceTaskAfterGrantChangeGenerationTwoSQLite(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeGenerationTwo(t, false)
}
func TestPlatformServiceTaskAfterGrantChangeGenerationTwoPostgres(t *testing.T) {
	testPlatformServiceTaskAfterGrantChangeGenerationTwo(t, true)
}
