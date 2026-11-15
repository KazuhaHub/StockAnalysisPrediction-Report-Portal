package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The retention tests run on a synthetic clock: every row is stamped relative to retNow and the sweep is
// handed an instant to judge them by, so no test waits and none depends on the wall clock.
const retDay = int64(24 * 60 * 60 * 1000)

var retNow = 1000 * retDay

// retFixture seeds the control tables with the smallest rows each constraint accepts. A row is described by
// what the sweep reads: its installation, its action and stage, when it completed and what pins it.
type retFixture struct {
	t       *testing.T
	st      *Store
	s       *Server
	n       int
	epoch   string
	hash    string
	release string
	runtime string
	updated int64
}

func newRetFixture(t *testing.T, pg bool) *retFixture {
	t.Helper()
	st := platformTestStore(t, pg)
	f := &retFixture{t: t, st: st, s: &Server{st: st}, n: 10000, epoch: platformFixtureID(99), hash: strings.Repeat("a", 64)}
	// A shared database keeps audit rows and settings between tests; each test starts from none.
	mustExec(t, st, `DELETE FROM audit_log WHERE action=?`, platformRetentionAuditActionName)
	mustExec(t, st, `DELETE FROM meta WHERE k LIKE 'app\_platform\_retention\_%' ESCAPE '\' OR k='app_platform_restore_paused'`)
	e := st.poolExec()
	platformSeedPublisher(t, e, "ret-publisher")
	platformTestInsert(t, e, "publisher_keys", map[string]any{
		"publisher_id": "ret-publisher", "key_id": "k1", "algorithm": "ed25519", "public_key": []byte{1},
		"not_before_ms": 0, "not_after_ms": nil, "revoked_ms": nil, "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
	artifact := platformDigest([]byte("ret-artifact"))
	platformTestInsert(t, e, "frontend_artifacts", map[string]any{"sha256": artifact, "archive_bytes": 1, "content": []byte{1}, "created_ms": 1})
	f.release = platformDigest([]byte("ret-release"))
	platformTestInsert(t, e, "releases", map[string]any{
		"sha256": f.release, "app_id": "ret-app", "publisher_id": "ret-publisher", "version": "1.0.0", "descriptor": []byte("{}"),
		"signature": []byte{1}, "verification_key_id": "k1", "trust_revision": 1, "verified_ms": 1, "frontend_sha256": artifact,
		"managed": 0, "created_ms": 1,
	})
	return f
}

func (f *retFixture) id() string {
	f.n++
	return platformFixtureID(f.n)
}

func (f *retFixture) insert(table string, values map[string]any) {
	f.t.Helper()
	platformTestInsert(f.t, f.st.poolExec(), table, values)
}

// installation adds one installation; desired and phase are the control columns the sweep tells live from removed by.
func (f *retFixture) installation(desired, phase string, generation int64) string {
	f.t.Helper()
	id := f.id()
	f.insert("installations", map[string]any{
		"id": id, "app_id": "ret-app-" + id, "publisher_id": "ret-publisher", "runtime_id": nil, "desired_state": desired, "phase": phase,
		"generation": generation, "revision": 1, "policy_revision": 1, "control_fence": 0, "controller_epoch": f.epoch,
		"sealed_identity_seed": "x", "created_ms": 1, "updated_ms": 1,
	})
	return id
}

func (f *retFixture) removedInstallation() string { return f.installation("removed", "removed", 1) }
func (f *retFixture) liveInstallation(generation int64) string {
	return f.installation("enabled", "ready", generation)
}

// plan adds a review plan; consumed is 0 for an unconsumed plan.
func (f *retFixture) plan(installation, action string, expires, consumed int64) string {
	f.t.Helper()
	id := f.id()
	var consumedValue any
	if consumed != 0 {
		consumedValue = consumed
	}
	var installationValue any
	if installation != "" {
		installationValue = installation
	}
	f.insert("review_plans", map[string]any{
		"id": id, "installation_id": installationValue, "app_id": "ret-app", "publisher_id": "ret-publisher", "action": action,
		"source_release_sha256": nil, "target_release_sha256": nil, "installation_revision": 1, "policy_revision": 1, "trust_revision": 1,
		"controller_epoch": f.epoch, "actor_scope": f.hash, "username": nil, "account_created": "", "account_generation": 0,
		"authority_fingerprint": f.hash, "plan_sha256": f.hash, "plan_json": "{}", "expires_ms": expires, "consumed_ms": consumedValue,
		"created_ms": 1, "updated_ms": 1,
	})
	return id
}

// operation adds a consumed plan and the operation it admitted. completed is 0 for an operation still running.
// The idempotency key digest is the operation's own number, so a test can replay it.
func (f *retFixture) operation(installation, action, stage string, completed, generation int64) (op, plan string) {
	f.t.Helper()
	plan = f.plan(installation, action, retNow*2, 1)
	op = f.id()
	var completedValue any
	updated := int64(1)
	if completed != 0 {
		completedValue, updated = completed, completed
	}
	revision := int64(4)
	f.insert("operations", map[string]any{
		"id": op, "installation_id": installation, "review_plan_id": plan, "actor_scope": f.hash, "idempotency_sha256": f.keyOf(op),
		"request_sha256": f.hash, "action": action, "managed": 0, "source_generation": 0, "target_generation": generation, "stage": stage,
		"state_revision": revision, "row_revision": 1, "lease_owner": nil, "lease_until_ms": nil, "fence": 0, "controller_epoch": f.epoch,
		"error_code": nil, "completed_ms": completedValue, "created_ms": 1, "updated_ms": updated,
	})
	return op, plan
}

func (f *retFixture) keyOf(op string) string {
	return platformDigest([]byte("idempotency:" + op))
}

// effect adds one effect of an operation.
func (f *retFixture) effect(op, kind, state, receipt string) string {
	f.t.Helper()
	id := f.id()
	number := scalar[int](f.t, f.st, `SELECT COALESCE(MAX(effect_number),0)+1 FROM app_platform_operation_effects WHERE operation_id=? AND stage='stage'`, op)
	f.updated++
	f.insert("operation_effects", map[string]any{
		"operation_id": op, "stage": "stage", "effect_number": number, "effect_id": id, "kind": kind, "payload_sha256": f.hash,
		"runtime_id": nil, "controller_epoch": f.epoch, "admitted_fence": 1, "observed_fence": 0, "state": state, "receipt_json": receipt,
		"created_ms": 1, "updated_ms": f.updated,
	})
	return id
}

// effectAt adds an effect at a named stage and number, for the stages a reader looks an effect up by.
func (f *retFixture) effectAt(op, stage string, number int, kind, state, receipt string) string {
	f.t.Helper()
	id := f.id()
	f.updated++
	f.insert("operation_effects", map[string]any{
		"operation_id": op, "stage": stage, "effect_number": number, "effect_id": id, "kind": kind, "payload_sha256": f.hash,
		"runtime_id": nil, "controller_epoch": f.epoch, "admitted_fence": 1, "observed_fence": 0, "state": state, "receipt_json": receipt,
		"created_ms": 1, "updated_ms": f.updated,
	})
	return id
}

// task adds a task. completed is 0 for a task that has not finished; an attempt row is added when asked.
func (f *retFixture) task(installation, occurrence, state string, completed int64, attempt bool) string {
	f.t.Helper()
	id := f.id()
	payload := []byte(`{"occurrence":"` + occurrence + `"}`)
	var completedValue, result any
	updated := int64(1)
	attempts := 0
	if completed != 0 {
		completedValue, updated, result = completed, completed, `{"ok":true}`
	}
	if attempt {
		attempts = 1
	}
	f.insert("tasks", map[string]any{
		"id": id, "installation_id": installation, "app_id": "ret-app", "publisher_id": "ret-publisher", "occurrence_id": occurrence,
		"handler": "app.backend", "payload": payload, "payload_sha256": platformDigest(payload), "state": state, "generation": 1,
		"policy_revision": 1, "release_sha256": f.release, "controller_epoch": f.epoch, "attempts": attempts, "max_attempts": 3, "fence": 1,
		"revision": 1, "worker_id": nil, "lease_id": nil, "lease_until_ms": nil, "next_attempt_ms": 1, "expires_ms": retNow * 2,
		"error_code": nil, "result_json": result, "completed_ms": completedValue, "created_ms": 1, "updated_ms": updated,
	})
	if attempt {
		attemptState := state
		if attemptState == "waiting" {
			attemptState = "failed"
		}
		f.insert("task_attempts", map[string]any{
			"task_id": id, "attempt": 1, "state": attemptState, "worker_id": f.id(), "lease_id": f.id(), "fence": 1, "execution_sha256": f.hash,
			"started_ms": 1000, "lease_until_ms": 31000, "finished_ms": 2000, "error_code": nil, "result_sha256": nil, "evidence_json": nil,
			"created_ms": 1000, "updated_ms": 2000,
		})
	}
	return id
}

// taskState describes what a task row looks like after a sweep.
type retTask struct {
	exists           bool
	payloadLen       int
	hasResult        bool
	attempts         int
	occurrence       string
	handler, payload string
}

func (f *retFixture) taskRow(id string) retTask {
	f.t.Helper()
	var r retTask
	r.exists = scalar[int](f.t, f.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE id=?`, id) == 1
	if !r.exists {
		return r
	}
	var payload []byte
	var result *string
	if err := f.st.queryRow(`SELECT payload,result_json,occurrence_id,handler,payload_sha256 FROM app_platform_tasks WHERE id=?`, id).Scan(&payload, &result, &r.occurrence, &r.handler, &r.payload); err != nil {
		f.t.Fatal(err)
	}
	r.payloadLen, r.hasResult = len(payload), result != nil
	r.attempts = scalar[int](f.t, f.st, `SELECT COUNT(*) FROM app_platform_task_attempts WHERE task_id=?`, id)
	return r
}

// runtimeBinding adds a disabled runtime binding and returns its id.
func (f *retFixture) runtimeBinding() string {
	f.t.Helper()
	id := f.id()
	f.insert("runtime_bindings", map[string]any{
		"id": id, "deployment_id": f.id(), "controller_epoch": f.epoch, "control_revision": 1, "enabled": 0,
		"verification_key_fingerprint": nil, "sealed_control_key": nil, "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
	return id
}

// effectOn adds an effect of an operation that names a runtime.
func (f *retFixture) effectOn(op, runtime, kind, state, receipt string) string {
	f.t.Helper()
	id := f.effect(op, kind, state, receipt)
	mustExec(f.t, f.st, `UPDATE app_platform_operation_effects SET runtime_id=? WHERE effect_id=?`, runtime, id)
	return id
}

// secretBinding adds a binding that names a review plan.
func (f *retFixture) secretBinding(installation, plan string) {
	f.t.Helper()
	credential := f.id()
	f.insert("credentials", map[string]any{
		"id": credential, "label": "c", "state": "active", "sealed_value": "x", "value_revision": 1, "revision": 1,
		"created_ms": 1, "updated_ms": 1, "revoked_ms": nil,
	})
	f.insert("secret_bindings", map[string]any{
		"id": f.id(), "installation_id": installation, "name": "n" + f.id(), "credential_id": credential, "credential_value_revision": 1,
		"operation": "http_bearer", "state": "active", "policy_json": "{}", "policy_revision": 1, "review_plan_id": plan, "revision": 1,
		"created_ms": 1, "updated_ms": 1, "revoked_ms": nil,
	})
}

func (f *retFixture) namespace(installation, state string) string {
	f.t.Helper()
	id := f.id()
	var owner any
	if state == "active" {
		owner = installation
	}
	f.insert("data_namespaces", map[string]any{
		"id": id, "origin_installation_id": installation, "owner_installation_id": owner, "state": state, "data_format": 1,
		"quota_bytes": 1, "observed_bytes": 0, "writer_fence": 0, "revision": 1, "retained_ms": nil, "deleted_ms": nil,
		"created_ms": 1, "updated_ms": 1,
	})
	return id
}

func (f *retFixture) recoverySet(installation, namespace, op, state string, created int64) string {
	f.t.Helper()
	id := f.id()
	var manifest, verified, deleted any
	if state != "preparing" {
		manifest = f.hash
	}
	if state == "verified" {
		verified = int64(1)
	}
	if state == "deleted" {
		deleted = int64(1)
	}
	f.insert("recovery_sets", map[string]any{
		"id": id, "installation_id": installation, "data_namespace_id": namespace, "operation_id": op, "release_sha256": f.release,
		"data_format": 1, "checkpoint_id": f.id(), "manifest_sha256": manifest, "bytes": 1, "state": state, "controller_epoch": f.epoch,
		"writer_fence": 1, "verified_ms": verified, "deleted_ms": deleted, "created_ms": created, "updated_ms": created,
	})
	return id
}

func (f *retFixture) importGrant(set, state string) string {
	f.t.Helper()
	if f.runtime == "" {
		f.runtime = f.id()
		f.insert("runtime_bindings", map[string]any{
			"id": f.runtime, "deployment_id": f.id(), "controller_epoch": f.epoch, "control_revision": 1, "enabled": 0,
			"verification_key_fingerprint": nil, "sealed_control_key": nil, "revision": 1, "created_ms": 1, "updated_ms": 1,
		})
	}
	id := f.id()
	var consumed any
	if state == "consumed" {
		consumed = int64(1)
	}
	f.insert("import_grants", map[string]any{
		"id": id, "recovery_set_id": set, "runtime_id": f.runtime, "controller_epoch": f.epoch, "state": state, "consumed_ms": consumed,
		"created_ms": 1, "updated_ms": 1,
	})
	return id
}

func (f *retFixture) capabilityGrant(installation, plan string) {
	f.t.Helper()
	f.insert("capability_grants", map[string]any{
		"installation_id": installation, "capability": "cap." + f.id(), "granted": 1, "policy_json": "{}", "policy_revision": 1,
		"review_plan_id": plan, "created_ms": 1, "updated_ms": 1,
	})
}

// loadReceipt is a valid applied load receipt of one image, as the load effect leaves it.
func (f *retFixture) loadReceipt(op, installation, config string) string {
	f.t.Helper()
	effect := f.id()
	receipt := journal.LoadImageReceipt{
		Format: "portal.runner.load-image-receipt/1-dev.1",
		Claim: journal.LoadImageClaim{
			Format: "portal.runner.load-image-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", effect), EffectID: effect,
			ReleaseSHA256: f.release, Platform: "linux/amd64", ManifestDigest: f.hash, ConfigDigest: config,
			Layers: control.FormatLoadImageLayers([]appplatform.ImageLayer{{DiffID: f.hash, UncompressedBytes: 1}}), AdmittedFence: "1", ClaimedMS: "100",
		},
		DeploymentID: platformFixtureID(8700), ControllerEpoch: f.epoch, InstallationID: installation, OperationID: op,
		TargetGeneration: "1", Stage: "stage", EffectID: effect, PayloadSHA256: platformDigest([]byte(effect)),
		ImageID: "sha256:" + config, Store: "classic", State: "loaded", ObservedMS: "200", SettledMS: "300",
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := journal.ParseLoadImageReceipt(raw); err != nil {
		f.t.Fatal("the fixture load receipt is not valid:", err)
	}
	return string(raw)
}

func (f *retFixture) removeReceipt(config string) string {
	return fmt.Sprintf(`{"claim":{"config_digest":%q}}`, config)
}

func (f *retFixture) exists(table, id string) bool {
	f.t.Helper()
	return scalar[int](f.t, f.st, `SELECT COUNT(*) FROM `+platformPrefix+table+` WHERE id=?`, id) == 1
}

func (f *retFixture) sweep(now int64) platformRetentionResult {
	f.t.Helper()
	result, err := f.s.sweepPlatformRetention(f.t.Context(), now, defaultPlatformRetentionLimits())
	if err != nil {
		f.t.Fatalf("sweep: %v", err)
	}
	return result
}
