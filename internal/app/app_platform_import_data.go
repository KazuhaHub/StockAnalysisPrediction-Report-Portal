package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The reviewed import_data action (doc 185 section 5): replace the contents of a disabled installation's
// namespace with an archive an operator packed into the runner's import inbox, then start the installation again.
// It is a same-release lifecycle like enable and repair, with one more stage between the checkpoint and the start:
//
//	validate -> quiesce -> snapshot -> import -> start -> probe -> activate -> complete
//
// Deviation from the proposal: the installation must already be disabled (its backend stopped), so quiesce is the
// verified stopped state and not a stop of its own. The review and the console say so.

const platformManagedImportDataFormat = "portal.managed-import-data-review/1-dev.1"

// platformImportStep is the reviewed archive of an import, carried in the plan like a migration is.
type platformImportStep struct {
	ArchiveSHA256 string `json:"archive_sha256"`
	ArchiveBytes  int64  `json:"archive_bytes"`
	Entries       int64  `json:"entries"`
	ContentBytes  int64  `json:"content_bytes"`
	// DataFormat is the schema marker of the archive, which equals the release's data format.
	DataFormat  int64 `json:"data_format"`
	QuotaBytes  int64 `json:"quota_bytes"`
	QuotaInodes int64 `json:"quota_inodes"`
}

func (p platformImportStep) valid() bool {
	return platformValidSHA(p.ArchiveSHA256) && p.ArchiveBytes > 0 && p.Entries >= 0 && p.ContentBytes >= 0 &&
		p.DataFormat >= 0 && appplatform.WithinRunnerDataQuota(p.QuotaBytes, p.QuotaInodes)
}

func (s *Server) platformRuntimeFor(e migExec, epoch string) (platformRuntimeRegistration, error) {
	dek, err := s.dek()
	if err != nil {
		return platformRuntimeRegistration{}, err
	}
	runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
	clear(seed)
	return runtime, err
}

// createPlatformManagedImportDataReview writes the review of an import after the runner confirmed that exactly one
// inbox archive has the digest, and what it holds.
func (s *Server) createPlatformManagedImportDataReview(ctx context.Context, session platformActorSession, installation, archiveSHA string, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformManagedImportDataReviewWithRuntimeContext(ctx, session, installation, archiveSHA, target, facts.runtime, endpoint)
}

func (s *Server) createPlatformManagedImportDataReviewWithRuntimeContext(ctx context.Context, session platformActorSession, installation, archiveSHA string, target *platformManagedInstallTarget, observedRuntime platformRuntimeRegistration, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	if !platformValidSHA(archiveSHA) {
		return platformRemovalReview{}, errPlatformManagementInput
	}
	var query platformDataQueryRequest
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		var desired, phase string
		if err := e.queryRow(`SELECT desired_state,phase FROM app_platform_installations WHERE id=?`, installation).Scan(&desired, &phase); err != nil {
			return errAppPlatformPlanChanged
		}
		if desired != "disabled" || phase != "disabled" {
			return errPlatformDataImportRequiresDisabled
		}
		runtime, err := s.platformRuntimeFor(e, control.epoch)
		if err != nil || !samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		source, err := loadPlatformCheckpointSourceForEnableReview(e, platformImportLease(installation, control.epoch, e), runtime, control.nowMS)
		if err != nil {
			return fmt.Errorf("import data source: %w", err)
		}
		_, inodes, _, err := platformCurrentNamespaceQuota(e, installation, source.namespaceID)
		if err != nil {
			return err
		}
		query, err = s.signPlatformDataQuery(e, control.epoch, source.releaseSHA256, source.descriptor, installation, platformImportQueryPayload(archiveSHA, inodes))
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	report, err := s.queryPlatformData(ctx, endpoint, query)
	if err != nil {
		return platformRemovalReview{}, err
	}
	if !report.ArchiveFound {
		return platformRemovalReview{}, errPlatformDataImportArchiveMissing
	}
	var result platformRemovalReview
	err = s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		if err := lockOUMutation(e); err != nil {
			return err
		}
		runtime, err := s.platformRuntimeFor(e, control.epoch)
		if err != nil || !samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		var generation int64
		if err := e.queryRow(`SELECT generation FROM app_platform_installations WHERE id=?`, installation).Scan(&generation); err != nil {
			return errAppPlatformPlanChanged
		}
		if generation < 2 || generation == math.MaxInt64 {
			return errAppPlatformPlanChanged
		}
		expected := platformImportLease(installation, control.epoch, e)
		source, err := loadPlatformCheckpointSourceForEnableReview(e, expected, runtime, control.nowMS)
		if err != nil {
			return fmt.Errorf("import data review source: %w", err)
		}
		plan, revision, err := platformBuildManagedSameReleasePlan(e, installation, target, runtime, source, control.nowMS, appplatform.ActionImportData, platformManagedImportDataFormat)
		if err != nil {
			return fmt.Errorf("import data review plan: %w", err)
		}
		quotaBytes, quotaInodes, _, err := platformCurrentNamespaceQuota(e, installation, source.namespaceID)
		if err != nil {
			return err
		}
		step := platformImportStep{
			ArchiveSHA256: archiveSHA, ArchiveBytes: mustPlatformCheckpointInt(report.ArchiveBytes),
			Entries: mustPlatformCheckpointInt(report.ArchiveEntries), ContentBytes: mustPlatformCheckpointInt(report.ContentBytes),
			DataFormat: mustPlatformCheckpointInt(report.SchemaMarker), QuotaBytes: quotaBytes, QuotaInodes: quotaInodes,
		}
		switch {
		case step.DataFormat != plan.DataFormat:
			return errPlatformDataFormatMismatch
		case step.ContentBytes > quotaBytes || step.Entries+1 > quotaInodes:
			return errPlatformDataImportTooLarge
		case !step.valid():
			return errPlatformDataImportArchiveInvalid
		}
		plan.Import = &step
		if plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 || plan.SourceGeneration+1 != plan.TargetGeneration || revision == math.MaxInt64 {
			return errAppPlatformPlanChanged
		}
		verified, err := platformVerifyStoredRelease(e, plan.TargetReleaseSHA256, plan.TrustRevision, trust, control.nowMS)
		if err != nil || verified.appID != plan.AppID || verified.publisherID != plan.PublisherID {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 || control.nowMS > math.MaxInt64-platformReviewPlanValidityMS {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: platformDigest(raw), JSON: string(raw), ExpiresMS: control.nowMS + platformReviewPlanValidityMS}
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(
			id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,
			trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,
			plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, plan.AppID, plan.PublisherID, string(appplatform.ActionImportData), plan.SourceReleaseSHA256, plan.TargetReleaseSHA256,
			revision, plan.SourcePolicyRevision, plan.TrustRevision, plan.ControllerEpoch, actor.scope, actor.username, actor.created,
			actor.generation, actor.authorityFingerprint, result.SHA256, result.JSON, result.ExpiresMS, control.nowMS, control.nowMS); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan    string `json:"plan_id"`
			Archive string `json:"archive_sha256"`
			Bytes   int64  `json:"archive_bytes"`
			Entries int64  `json:"entries"`
		}{id, step.ArchiveSHA256, step.ArchiveBytes, step.Entries})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,?,'app_platform.data_import_review','app_platform_installation',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, installation, string(detail))
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

// platformImportLease is the lease value the source loaders take for a review: the installation's own generation.
func platformImportLease(installation, epoch string, e migExec) platformOperationLease {
	var generation int64
	_ = e.queryRow(`SELECT generation FROM app_platform_installations WHERE id=?`, installation).Scan(&generation)
	return platformOperationLease{
		installationID: installation, controllerEpoch: epoch,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionImportData, Managed: true, Generation: uint64(generation), Revision: 1, Stage: appplatform.StageQueued},
	}
}

func (s *Server) consumePlatformManagedImportDataReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	if !platformValidUUID(planID) || !platformValidUUID(key) || !platformValidSHA(planSHA) {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(ctx, session, planID, planSHA, key, target, facts.runtime, appplatform.ActionImportData)
}

// validatePlatformImportData is the validate stage: the runner is asked again whether exactly one inbox archive has
// the reviewed digest, size and counts, and the stage moves on only if it still does.
func (s *Server) validatePlatformImportData(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionImportData || expected.snapshot.Stage != appplatform.StageValidate {
		return zero, errAppPlatformStageEvidence
	}
	var query platformDataQueryRequest
	var plan platformManagedUpdatePlan
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	err = func() error {
		defer tx.Rollback()
		if err := lockOUMutation(e); err != nil {
			return err
		}
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		facts, err := loadPlatformBoundOperationLease(e, expected)
		if err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		var source platformCheckpointSource
		plan, source, _, err = s.loadPlatformAdmittedManagedEnable(e, expected, trust, facts.now)
		if err != nil || plan.Import == nil || !plan.Import.valid() {
			return errAppPlatformStageEvidence
		}
		query, err = s.signPlatformDataQuery(e, expected.controllerEpoch, source.releaseSHA256, source.descriptor, expected.installationID,
			platformImportQueryPayload(plan.Import.ArchiveSHA256, plan.Import.QuotaInodes))
		return err
	}()
	if err != nil {
		return zero, err
	}
	report, err := s.queryPlatformData(ctx, endpoint, query)
	if err != nil {
		return zero, err
	}
	step := plan.Import
	switch {
	case !report.ArchiveFound:
		return zero, errPlatformDataImportArchiveMissing
	case mustPlatformCheckpointInt(report.ArchiveBytes) != step.ArchiveBytes || mustPlatformCheckpointInt(report.ArchiveEntries) != step.Entries ||
		mustPlatformCheckpointInt(report.ContentBytes) != step.ContentBytes:
		return zero, errPlatformDataImportArchiveInvalid
	case mustPlatformCheckpointInt(report.SchemaMarker) != plan.DataFormat:
		return zero, errPlatformDataFormatMismatch
	}
	return s.advancePlatformManagedEnableStage(ctx, expected)
}

func platformImportQueryPayload(archiveSHA string, quotaInodes int64) control.DataQueryPayload {
	return control.DataQueryPayload{Operation: control.DataQueryInspectImport, ArchiveSHA256: archiveSHA, QuotaBytes: "0", QuotaInodes: strconv.FormatInt(quotaInodes, 10)}
}

// platformImportDataIntent is the signed import and its durable effect row.
type platformImportDataIntent struct {
	effectID                      string
	runtime                       platformRuntimeRegistration
	authority                     control.Authority
	command                       control.ImportDataCommand
	plan                          platformManagedUpdatePlan
	checkpoint                    journal.CheckpointReceipt
	descriptor                    []byte
	raw, signature, readSignature []byte
}

// platformImportEffectStage is the stage name of the Portal's effect rows of an import: the journal's own stage list
// has none, and the runner records them under restore_data.
const platformImportEffectStage = "import"

func platformImportDataCommand(expected platformOperationLease, runtime platformRuntimeRegistration, plan platformManagedUpdatePlan, checkpoint journal.CheckpointReceipt, effectID string) (control.ImportDataCommand, error) {
	step := plan.Import
	if step == nil || !step.valid() {
		return control.ImportDataCommand{}, errAppPlatformStageEvidence
	}
	command := control.ImportDataCommand{CommandHeader: control.CommandHeader{
		Format: control.ImportDataFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10), Stage: control.ImportDataStage,
		EffectNumber: control.ImportDataEffectNumber, EffectID: effectID, Kind: control.ImportDataKind,
		InstallationFence: strconv.FormatInt(expected.fence, 10),
	}, Payload: control.ImportDataPayload{
		NamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256, DataFormat: strconv.FormatInt(plan.DataFormat, 10),
		WriterFence: checkpoint.WriterFence, ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode, Device: checkpoint.Device,
		QuotaBytes: strconv.FormatInt(step.QuotaBytes, 10), QuotaInodes: strconv.FormatInt(step.QuotaInodes, 10),
		ArchiveSHA256: step.ArchiveSHA256, ArchiveBytes: strconv.FormatInt(step.ArchiveBytes, 10),
		ArchiveEntries: strconv.FormatInt(step.Entries, 10), ContentBytes: strconv.FormatInt(step.ContentBytes, 10),
		SourceCheckpointOperationID: checkpoint.OperationID, SourceCheckpointEffectID: checkpoint.EffectID,
		SourceCheckpointReceiptSHA: platformDigest(mustMarshalPlatformCheckpointReceipt(checkpoint)),
	}}
	digest, err := control.ImportDataLogicalDigest(command)
	if err != nil {
		return control.ImportDataCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

// loadPlatformImportCheckpoint reads the pre-import checkpoint this operation took and proves it is the plan's.
func loadPlatformImportCheckpoint(e migExec, expected platformOperationLease, plan platformManagedUpdatePlan) (journal.CheckpointReceipt, error) {
	var zero journal.CheckpointReceipt
	var effectID, payload, raw string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`, expected.id).Scan(&effectID, &payload, &raw); err != nil {
		return zero, fmt.Errorf("%w: import checkpoint effect", errAppPlatformStageEvidence)
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(raw))
	if err != nil || receipt.EffectID != effectID || effectID != plan.CheckpointEffectID || receipt.PayloadSHA256 != payload ||
		receipt.OperationID != expected.id || receipt.CheckpointID != plan.CheckpointID || receipt.ManifestSHA256 != plan.CheckpointManifestSHA256 ||
		receipt.NamespaceID != plan.DataNamespaceID || !platformCheckpointReceiptMatchesSourceOperation(receipt, expected.installationID) {
		return zero, fmt.Errorf("%w: import checkpoint receipt", errAppPlatformStageEvidence)
	}
	return receipt, nil
}

func (s *Server) preparePlatformImportData(ctx context.Context, expected platformOperationLease) (platformImportDataIntent, error) {
	var zero platformImportDataIntent
	if expected.snapshot.Action != appplatform.ActionImportData || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageImport {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	plan, source, _, err := s.loadPlatformAdmittedManagedEnable(e, expected, trust, facts.now)
	if err != nil || plan.Import == nil {
		return zero, errAppPlatformStageEvidence
	}
	checkpoint, err := loadPlatformImportCheckpoint(e, expected, plan)
	if err != nil {
		return zero, err
	}
	quotaBytes, quotaInodes, _, err := platformCurrentNamespaceQuota(e, expected.installationID, plan.DataNamespaceID)
	if err != nil || quotaBytes != plan.Import.QuotaBytes || quotaInodes != plan.Import.QuotaInodes {
		return zero, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effectID, kind, digest, effectState, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,admitted_fence,observed_fence,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='import' AND effect_number=1`+lock, expected.id).Scan(&effectID, &kind, &digest, &admitted, &observed, &effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	if fresh {
		if effectID, err = platformNewUUID(); err != nil {
			return zero, err
		}
	}
	command, err := platformImportDataCommand(expected, runtime, plan, checkpoint, effectID)
	if err != nil {
		return zero, err
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'import',1,?,'import-data',?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now, facts.now); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID string `json:"effect_id"`
			Archive  string `json:"archive_sha256"`
		}{effectID, plan.Import.ArchiveSHA256})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.data_import_intent','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	} else if kind != "import-data" || digest != command.PayloadSHA256 || admitted != expected.fence || observed != 0 || effectState != "prepared" || receipt != "{}" {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if issued < now-1000 || issued > now+1000 || expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	authority := platformRuntimeAuthority(runtime)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	intent := platformImportDataIntent{effectID: effectID, runtime: runtime, authority: authority, command: command, plan: plan, checkpoint: checkpoint, descriptor: bytes.Clone(source.descriptor)}
	if intent.raw, intent.signature, err = control.SignImportData(command, key, authority, issued); err != nil {
		return zero, err
	}
	if intent.readSignature, err = control.SignImportDataRead(intent.raw, key, authority, issued); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

type platformImportDataObservation interface {
	ImportDataReceipt() (journal.ImportDataReceipt, error)
	DataEffectStatus() (journal.MigrateDataStatus, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformImportDataObservation(observation platformImportDataObservation, intent platformImportDataIntent, endpoint platformRuntimeEndpoint) (journal.ImportDataReceipt, bool) {
	receipt, err := observation.ImportDataReceipt()
	if err != nil || !journal.ImportDataReceiptMatchesCommand(receipt, intent.command) ||
		!platformObservedBinding(observation.Controller(), observation.Endpoint(), observation.ReceivedAt(), observation.CommandSHA256(), intent.raw, intent.authority, endpoint) {
		return journal.ImportDataReceipt{}, false
	}
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if observedErr != nil || settledErr != nil || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.ImportDataReceipt{}, false
	}
	return receipt, true
}

func (s *Server) dispatchPlatformImportData(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformImportData(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	observation, err := platformExecuteImportData(ctx, endpoint, intent)
	if err != nil {
		var readErr error
		observation, readErr = platformReadImportData(ctx, endpoint, intent)
		if readErr != nil {
			return platformOperationLease{}, errors.Join(err, readErr)
		}
	}
	if err := platformDataEffectFailure(observation, intent.command.CommandHeader, intent.raw, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformImportData(ctx, expected, intent, endpoint, observation)
}

// commitPlatformImportData settles the import effect, records the bytes and inodes the project quota reports as the
// namespace's observed usage, and moves the operation to its start.
func (s *Server) commitPlatformImportData(ctx context.Context, expected platformOperationLease, intent platformImportDataIntent, endpoint platformRuntimeEndpoint, observation platformImportDataObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionImportData || expected.snapshot.Stage != appplatform.StageImport || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	receipt, ok := validPlatformImportDataObservation(observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return zero, errAppPlatformStageEvidence
	}
	if parsed, parseErr := journal.ParseImportDataReceipt(encoded); parseErr != nil || parsed != receipt {
		return zero, errAppPlatformStageEvidence
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	runtime, err := s.platformRuntimeFor(e, expected.controllerEpoch)
	if err != nil || runtime.id != intent.runtime.id {
		return zero, errAppPlatformStageEvidence
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	plan, _, _, err := s.loadPlatformAdmittedManagedEnable(e, expected, trust, facts.now)
	if err != nil || plan.Import == nil || *plan.Import != *intent.plan.Import {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='import' AND effect_number=1 AND effect_id=? AND kind='import-data' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, intent.effectID, intent.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active'`,
		mustPlatformCheckpointInt(receipt.UsedBytes), now, plan.DataNamespaceID, expected.installationID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageStart)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='start',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='import' AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Archive       string `json:"archive_sha256"`
		Entries       string `json:"entries"`
		ObservedBytes string `json:"observed_bytes"`
		UsedInodes    string `json:"used_inodes"`
	}{receipt.ArchiveSHA256, receipt.Entries, receipt.UsedBytes, receipt.UsedInodes})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_imported','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

// removePlatformImportArchive deletes the inbox archive of an import that reached complete, and only of that: a failed
// or unfinished import keeps its archive for another try. It is best effort. The import is complete either way, so a
// failure here never fails or undoes it: it is logged and audited, and the archive stays for the operator to remove.
func (s *Server) removePlatformImportArchive(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) {
	var raw, stage string
	if err := s.st.queryRowContext(ctx, `SELECT p.plan_json,o.stage FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=? AND o.action=?`,
		lease.id, string(appplatform.ActionImportData)).Scan(&raw, &stage); err != nil || stage != string(appplatform.StageComplete) {
		return
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(raw), &plan) != nil || plan.Import == nil || !plan.Import.valid() {
		return
	}
	fail := func(err error) {
		log.Printf("app-platform import archive removal (operation %s): %v", lease.id, err)
		detail, _ := json.Marshal(struct {
			Archive string `json:"archive_sha256"`
		}{plan.Import.ArchiveSHA256})
		_, _ = s.st.execContext(ctx, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.data_import_archive_remove_failed','app_platform_operation',?,?,'')`,
			time.Now().UTC().Format(time.RFC3339), lease.id, string(detail))
	}
	var request platformDataQueryRequest
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		fail(err)
		return
	}
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	err = func() error {
		defer tx.Rollback()
		var descriptor []byte
		if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, plan.TargetReleaseSHA256).Scan(&descriptor); err != nil {
			return err
		}
		var signErr error
		request, signErr = s.signPlatformDataQuery(e, lease.controllerEpoch, plan.TargetReleaseSHA256, descriptor, lease.installationID,
			control.DataQueryPayload{Operation: control.DataQueryRemoveImport, ArchiveSHA256: plan.Import.ArchiveSHA256, QuotaBytes: "0", QuotaInodes: "0"})
		return signErr
	}()
	if err != nil {
		fail(err)
		return
	}
	if _, err := s.queryPlatformData(ctx, endpoint, request); err != nil {
		fail(err)
		return
	}
	detail, _ := json.Marshal(struct {
		Archive string `json:"archive_sha256"`
	}{plan.Import.ArchiveSHA256})
	_, _ = s.st.execContext(ctx, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.data_import_archive_removed','app_platform_operation',?,?,'')`,
		time.Now().UTC().Format(time.RFC3339), lease.id, string(detail))
}
