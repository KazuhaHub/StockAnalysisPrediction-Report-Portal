package app

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A resume that follows an earlier resume whose start was applied has to quiesce first. The earlier start
// left a container in the namespace, and the runner refuses a second start while any container of the
// namespace is not stopped or absent. The first resume of an installation has nothing to quiesce and stays
// evidence-only; this one issues the ordinary restore stop and remove effects for the earlier container
// before its own start.

// platformResumeContainer names the applied start of an earlier resume of the installation whose container
// no later operation has removed.
type platformResumeContainer struct {
	operationID, effectID, payload, receipt string
	fence, generation                       int64
}

// platformResumeContainerSource finds the earlier resume's container, if any, that the resume lease still
// has to stop and remove on the runtime. Operations the lease's own removal effects settled stay found, so
// the answer does not change while the resume works through its quiesce stage.
func platformResumeContainerSource(e migExec, lease platformOperationLease, runtimeID string) (platformResumeContainer, bool, error) {
	var c platformResumeContainer
	err := e.queryRow(`SELECT o.id,f.effect_id,f.payload_sha256,f.receipt_json,f.admitted_fence,o.target_generation
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='start' AND f.effect_number=1
		WHERE o.installation_id=? AND o.id<>? AND o.action='restore' AND o.managed=1 AND o.controller_epoch=?
		AND o.stage='failed' AND o.completed_ms IS NOT NULL AND p.plan_json LIKE ?
		AND f.kind='restore-start' AND f.state='applied' AND f.runtime_id=?
		AND NOT EXISTS (SELECT 1 FROM app_platform_operations o2
			JOIN app_platform_operation_effects q ON q.operation_id=o2.id AND q.stage='quiesce' AND q.kind='remove_code' AND q.state='applied'
			WHERE o2.installation_id=o.installation_id AND o2.id<>? AND o2.created_ms>o.created_ms)
		ORDER BY o.completed_ms DESC,o.id DESC LIMIT 1`,
		lease.installationID, lease.id, lease.controllerEpoch, platformResumePlanPrefix(), runtimeID, lease.id).
		Scan(&c.operationID, &c.effectID, &c.payload, &c.receipt, &c.fence, &c.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, true, nil
}

// loadPlatformResumeRemovalSource rebuilds the start of the earlier resume that left the container, from
// the evidence it was built from and the running receipt the runner returned, and proves it is the very
// command the effect row records. It signs nothing.
func loadPlatformResumeRemovalSource(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, container platformResumeContainer, now int64) (platformUpdateStartIntent, platformCheckpointSource, []byte, error) {
	var zero platformUpdateStartIntent
	fail := func(reason string, err error) (platformUpdateStartIntent, platformCheckpointSource, []byte, error) {
		return zero, platformCheckpointSource{}, nil, fmt.Errorf("resume removal source %s: %w", reason, errors.Join(errAppPlatformStageEvidence, err))
	}
	readLease := platformOperationLease{
		id: container.operationID, installationID: lease.installationID, controllerEpoch: lease.controllerEpoch, fence: container.fence,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionRestore, Managed: true, Generation: uint64(container.generation), Stage: appplatform.StageStart},
	}
	evidence, err := loadPlatformResumeEvidence(e, readLease)
	if err != nil {
		return fail("evidence", err)
	}
	source, err := platformResumeCheckpointSourceFor(evidence, runtime)
	if err != nil {
		return fail("source", err)
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, readLease)
	if err != nil || recoverySetID == "" {
		return fail("checkpoint", err)
	}
	checkpointRaw, err := loadPlatformRestoreCheckpointRaw(e, readLease, checkpoint)
	if err != nil {
		return fail("checkpoint receipt", err)
	}
	running, err := journal.ParseUpdateStartRunningReceipt([]byte(container.receipt))
	if err != nil {
		return fail("running receipt", err)
	}
	activation := running.Claim.ResourceID
	command, err := platformRestoreStartCommand(readLease, runtime, source, checkpoint, checkpointRaw, activation, container.effectID, evidence.policy)
	if err != nil || command.PayloadSHA256 != container.payload || !platformUpdateStartRunningMatches(running, command, activation, container.effectID, nil) {
		return fail("command", err)
	}
	return platformUpdateStartIntent{
		resourceID: activation, effectID: container.effectID, runtime: runtime,
		authority: control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
			ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint},
		command: command, checkpoint: checkpoint, checkpointRaw: checkpointRaw, source: source,
		plan:             platformRestoreStartPlan(readLease, checkpoint, recoverySetID, evidence.policy, source),
		targetDescriptor: bytes.Clone(source.descriptor), running: &running,
	}, source, source.descriptor, nil
}
