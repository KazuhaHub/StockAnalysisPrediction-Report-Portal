package app

import (
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformCompletedResumeAt reports whether the installation's operation for the generation is a
// completed recovery resume. A generation has exactly one completed managed operation, so this is a
// routing decision only: the resume loader re-proves everything it relies on.
func platformCompletedResumeAt(e migExec, installation string, generation int64) (bool, error) {
	var n int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='restore' AND o.managed=1 AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND p.plan_json LIKE ?`, installation, generation, platformResumePlanPrefix()).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// loadPlatformWritableResumeBackendHistory reconstructs a completed recovery resume from the same
// signed start and registration evidence the live operation used, so a resumed managed installation
// is admitted exactly like an updated one. The fresh envelopes are access proofs only; nothing here
// can change the completed operation. Any gap in the evidence refuses access.
func (s *Server) loadPlatformWritableResumeBackendHistory(e migExec, b platformBackendHistoryBinding, runtime platformRuntimeRegistration, seed, dek []byte, now int64) (platformWritableRegistrationIntent, error) {
	var zero platformWritableRegistrationIntent
	fail := func(stage string, err error) (platformWritableRegistrationIntent, error) {
		return zero, fmt.Errorf("resume history %s (%v): %w", stage, err, errAppPlatformLaunchUnavailable)
	}
	if b.generation < 2 {
		return fail("generation", nil)
	}
	var operationID string
	var completed, created, updated, fence int64
	if err := e.queryRow(`SELECT o.id,o.completed_ms,o.created_ms,o.updated_ms,o.fence
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='restore' AND o.managed=1 AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL
		AND o.controller_epoch=? AND p.controller_epoch=o.controller_epoch AND p.action='restore' AND p.consumed_ms IS NOT NULL
		AND p.target_release_sha256=? AND p.plan_json LIKE ?`,
		b.installation, b.generation, b.epoch, b.release, platformResumePlanPrefix()).
		Scan(&operationID, &completed, &created, &updated, &fence); err != nil ||
		!platformValidUUID(operationID) || created < 0 || completed < created || updated < completed || updated > now ||
		fence < 1 || fence > b.fence {
		return fail("operation", nil)
	}
	expected := platformOperationLease{
		id: operationID, installationID: b.installation, controllerEpoch: b.epoch, fence: fence,
		snapshot: appplatform.OperationSnapshot{
			Action: appplatform.ActionRestore, Managed: true, Generation: uint64(b.generation), Stage: appplatform.StageActivate,
		},
	}
	ev, err := loadPlatformResumeEvidence(e, expected)
	if err != nil {
		return fail("evidence", err)
	}
	if ev.plan.TargetGeneration != b.generation || ev.plan.ReleaseSHA256 != b.release || ev.plan.RuntimeID != runtime.id {
		return fail("plan tuple", nil)
	}
	var desired, phase, epoch, runtimeID string
	var generation, policy int64
	if err := e.queryRow(`SELECT desired_state,phase,generation,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`, b.installation).Scan(&desired, &phase, &generation, &policy, &epoch, &runtimeID); err != nil ||
		desired != "enabled" || phase != "ready" || generation != b.generation || policy != ev.plan.PolicyRevision ||
		epoch != b.epoch || runtimeID != runtime.id {
		return fail("installation", err)
	}
	var namespaceState string
	var namespaceFormat, writerFence int64
	if err := e.queryRow(`SELECT state,data_format,writer_fence FROM app_platform_data_namespaces
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=?`,
		ev.plan.NamespaceID, b.installation, b.installation).Scan(&namespaceState, &namespaceFormat, &writerFence); err != nil ||
		namespaceState != "active" || namespaceFormat != ev.plan.DataFormat || writerFence != ev.plan.NamespaceWriterFence+1 {
		return fail("namespace", err)
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformResumeCheckpoint(e, expected)
	if err != nil {
		return fail("checkpoint", err)
	}
	source, err := platformResumeCheckpointSourceFor(ev, runtime)
	if err != nil {
		return fail("source", err)
	}
	plan := platformRestoreStartPlan(expected, checkpoint, recoverySetID, ev.plan.PolicyRevision, source)
	start, err := loadPlatformCompletedUpdateStartActivation(e, expected, runtime, source, checkpoint, plan)
	if err != nil {
		return fail("activation", err)
	}
	return platformWritableHistoryFromStart(e, b, expected, runtime, seed, dek, now, start, ev.descriptor)
}
