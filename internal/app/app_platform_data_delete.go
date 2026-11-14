package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformDeleteDataIntent struct {
	command           control.DeleteDataCommand
	raw, signature    []byte
	authority         control.Authority
	runtime           platformRuntimeRegistration
	descriptor        []byte
	sourceOperationID string
	dataReceiptSHA256 string
}

func platformDeleteDataCommand(
	expected platformOperationLease,
	runtime platformRuntimeRegistration,
	namespaceID, release string,
	format, writerFence, projectID,
	quotaBytes, quotaInodes int64,
	device, namespaceInode uint64,
	effectID string,
) (control.DeleteDataCommand, error) {
	command := control.DeleteDataCommand{
		Format:          control.DeleteDataFormat,
		DeploymentID:    runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(
			runtime.controlRevision, 10),
		InstallationID: expected.installationID,
		OperationID:    expected.id,
		TargetGeneration: strconv.FormatUint(
			expected.snapshot.Generation, 10),
		Stage: "remove_data", EffectNumber: "1",
		EffectID: effectID, Kind: "delete-data",
		InstallationFence: strconv.FormatInt(expected.fence, 10),
		Payload: control.DeleteDataPayload{
			NamespaceID:    namespaceID,
			ReleaseSHA256:  release,
			ProjectID:      strconv.FormatInt(projectID, 10),
			DataFormat:     strconv.FormatInt(format, 10),
			QuotaBytes:     strconv.FormatInt(quotaBytes, 10),
			QuotaInodes:    strconv.FormatInt(quotaInodes, 10),
			ExpectedDevice: strconv.FormatUint(device, 10),
			ExpectedNamespaceInode: strconv.FormatUint(
				namespaceInode, 10),
			WriterFence: strconv.FormatInt(writerFence, 10),
		},
	}
	digest, err := control.DeleteDataLogicalDigest(command)
	if err != nil {
		return control.DeleteDataCommand{},
			errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func (s *Server) preparePlatformDeleteDataIntent(
	ctx context.Context, expected platformOperationLease,
) (platformDeleteDataIntent, error) {
	var zero platformDeleteDataIntent
	if expected.snapshot.Action != appplatform.ActionUninstall ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRemoveData ||
		expected.snapshot.Generation < 2 {
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
	runtime, seed, err := s.platformDataRuntime(
		e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedRemoval(
		e, expected, facts); err != nil {
		return zero, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var namespaceID, origin, owner, state string
	var format, writerFence, revision int64
	if err := e.queryRow(`SELECT id,origin_installation_id,
		owner_installation_id,state,data_format,writer_fence,revision
		FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`+lock,
		expected.installationID).Scan(
		&namespaceID, &origin, &owner, &state,
		&format, &writerFence, &revision); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if origin != expected.installationID ||
		owner != expected.installationID || state != "active" ||
		format < 1 || writerFence < 1 || revision < 2 {
		return zero, errAppPlatformStageEvidence
	}
	never, err := platformOperationNeverStarted(e, expected.id)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var release string
	var dataRaw, dataDescriptor []byte
	var sourceOperation, planRaw, planSHA string
	var sourceGeneration, sourceFence, completed int64
	if never == nil {
		var activationNamespace string
		var activationGeneration, activationFormat int64
		if err := e.queryRow(`SELECT release_sha256,generation,
			data_namespace_id,data_format
			FROM app_platform_activations
			WHERE installation_id=? AND data_namespace_id=?
			ORDER BY generation DESC,created_ms DESC LIMIT 1`,
			expected.installationID, namespaceID).Scan(
			&release, &activationGeneration, &activationNamespace,
			&activationFormat); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		if activationNamespace != namespaceID ||
			activationFormat != format ||
			activationGeneration+1 !=
				int64(expected.snapshot.Generation) ||
			!platformValidSHA(release) {
			return zero, errAppPlatformStageEvidence
		}
		if err := e.queryRow(`SELECT descriptor
			FROM app_platform_releases WHERE sha256=?`,
			release).Scan(&dataRaw); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		parsed, err := appplatform.ParseDescriptor(dataRaw)
		if err != nil || parsed.Manifest.Backend == nil ||
			parsed.SHA256 != release {
			return zero, errAppPlatformStageEvidence
		}
		dataDescriptor = bytes.Clone(dataRaw)

		if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,
			o.target_generation,o.fence,o.completed_ms
			FROM app_platform_operations o
			JOIN app_platform_review_plans p ON p.id=o.review_plan_id
			WHERE o.installation_id=? AND o.action='install' AND o.managed=1
			AND o.stage='complete' AND o.completed_ms IS NOT NULL
			AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
			AND o.error_code IS NULL
			ORDER BY o.target_generation DESC,o.completed_ms DESC LIMIT 1`,
			expected.installationID).Scan(
			&sourceOperation, &planRaw, &planSHA,
			&sourceGeneration, &sourceFence, &completed); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		if sourceGeneration < 1 || sourceFence < 1 ||
			completed > facts.now ||
			platformDigest([]byte(planRaw)) != planSHA {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		// A never-started uninstall has no completed install and no activation. The release, the review and the
		// fence come from the failed install and its applied prepare_data effect, which the plan named and
		// validatePlatformAdmittedRemoval just compared with the database.
		release, sourceOperation = never.ReleaseSHA256, never.OperationID
		if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&dataRaw); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		parsed, err := appplatform.ParseDescriptor(dataRaw)
		if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != release ||
			int64(parsed.Manifest.Data.Schema) != format {
			return zero, errAppPlatformStageEvidence
		}
		dataDescriptor = bytes.Clone(dataRaw)
		if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.target_generation,o.completed_ms
			FROM app_platform_operations o
			JOIN app_platform_review_plans p ON p.id=o.review_plan_id
			WHERE o.id=? AND o.installation_id=? AND o.action='install' AND o.managed=1
			AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
			AND o.lease_until_ms IS NULL AND o.error_code IS NOT NULL`,
			sourceOperation, expected.installationID).Scan(
			&planRaw, &planSHA, &sourceGeneration, &completed); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		// The effect's fence against the install operation's own fence is compared by the evidence loader that
		// validatePlatformAdmittedRemoval just ran (the effect cannot have been admitted at a newer fence); the
		// admitted fence is then compared with the effect row itself below.
		sourceFence = never.AdmittedFence
		if sourceGeneration != 1 || sourceFence < 1 || completed > facts.now ||
			platformDigest([]byte(planRaw)) != planSHA {
			return zero, errAppPlatformStageEvidence
		}
	}
	var plan platformInstallProjection
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil ||
		!platformManagedFormat(plan.Format) || plan.Runtime == nil ||
		plan.ControllerEpoch != expected.controllerEpoch ||
		plan.Runtime.RuntimeID != runtime.id ||
		validateManagedInstallTarget(
			e, plan.Runtime, expected.controllerEpoch) != nil {
		return zero, errAppPlatformStageEvidence
	}
	effectID, digest, receiptRaw := "", "", ""
	var admittedFence int64
	if err := e.queryRow(`SELECT effect_id,payload_sha256,
		receipt_json,admitted_fence
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='prepare_data'
		AND effect_number=1 AND kind='prepare-data'
		AND state='applied'`, sourceOperation).Scan(
		&effectID, &digest, &receiptRaw,
		&admittedFence); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if !platformValidUUID(effectID) ||
		!platformValidSHA(digest) ||
		admittedFence != sourceFence {
		return zero, errAppPlatformStageEvidence
	}
	data, dataErr := journal.ParseDataPreparationReceipt(
		[]byte(receiptRaw))
	source := platformManagedSource{
		operationID:    sourceOperation,
		installationID: expected.installationID,
		epoch:          expected.controllerEpoch,
		generation:     uint64(sourceGeneration),
		fence:          sourceFence,
	}
	dataCommand, commandErr := platformDataCommandForSource(
		e, source, runtime, namespaceID, effectID)
	original := dataCommand
	original.ControlRevision = data.ControlRevision
	if dataErr != nil || commandErr != nil ||
		dataCommand.PayloadSHA256 != digest ||
		!data.MatchesCommand(original) ||
		data.Claim.ResourceID != namespaceID ||
		data.Claim.RootFilesystemUUID == "" ||
		data.Claim.RootInode == "" ||
		data.InstallationID != expected.installationID ||
		data.ProjectID == "" || data.Device == "" ||
		data.NamespaceInode == "" || data.QuotaBytes == "" ||
		data.QuotaInodes == "" {
		return zero, errAppPlatformStageEvidence
	}
	project, projectErr := strconv.ParseInt(
		data.ProjectID, 10, 64)
	device, deviceErr := strconv.ParseUint(data.Device, 10, 64)
	namespaceInode, inodeErr := strconv.ParseUint(
		data.NamespaceInode, 10, 64)
	quotaBytes, bytesErr := strconv.ParseInt(
		data.QuotaBytes, 10, 64)
	quotaInodes, quodesErr := strconv.ParseInt(
		data.QuotaInodes, 10, 64)
	if projectErr != nil || deviceErr != nil || inodeErr != nil ||
		bytesErr != nil || quodesErr != nil || project <= 0 ||
		project > math.MaxUint32 || device <= 0 ||
		namespaceInode <= 0 || quotaBytes <= 0 ||
		quotaBytes%512 != 0 || quotaInodes <= 0 {
		return zero, errAppPlatformStageEvidence
	}
	var effectCount int
	if err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='remove_data'
		AND effect_number=1`, expected.id).Scan(
		&effectCount); err != nil || effectCount != 0 {
		return zero, errAppPlatformStageEvidence
	}
	deleteEffectID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	command, err := platformDeleteDataCommand(
		expected, runtime, namespaceID, release, format,
		writerFence, project, quotaBytes, quotaInodes,
		device, namespaceInode, deleteEffectID)
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	command.IssuedMS = strconv.FormatInt(issued, 10)
	command.ExpiresMS = strconv.FormatInt(expires, 10)
	authority := control.Authority{
		DeploymentID:    runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch,
		ControlRevision: runtime.controlRevision,
		PublicKey:       bytes.Clone(runtime.publicKey),
		Fingerprint:     runtime.fingerprint,
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignDeleteData(
		command, key, authority, issued)
	if err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
		operation_id,stage,effect_number,effect_id,kind,
		payload_sha256,runtime_id,controller_epoch,admitted_fence,
		observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'remove_data',1,?,'delete-data',?,?,?,?,0,
		'prepared','{}',?,?)`,
		expected.id, deleteEffectID, command.PayloadSHA256,
		runtime.id, expected.controllerEpoch, expected.fence,
		facts.now, facts.now); err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_delete_intent',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
		expected.id, string(mustPlatformJSON(struct {
			NamespaceID string `json:"namespace_id"`
			EffectID    string `json:"effect_id"`
		}{namespaceID, deleteEffectID}))); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformDeleteDataIntent{
		command: command, raw: raw, signature: signature,
		authority: authority, runtime: runtime,
		descriptor:        dataDescriptor,
		sourceOperationID: sourceOperation,
		dataReceiptSHA256: platformDigest([]byte(receiptRaw)),
	}, nil
}

func mustPlatformJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func platformDeleteDataReceiptMatchesIntent(
	receipt journal.DeleteDataReceipt,
	intent platformDeleteDataIntent,
) bool {
	c := intent.command
	return receipt.DeploymentID == c.DeploymentID &&
		receipt.ControllerEpoch == c.ControllerEpoch &&
		receipt.InstallationID == c.InstallationID &&
		receipt.OperationID == c.OperationID &&
		receipt.TargetGeneration == c.TargetGeneration &&
		receipt.EffectID == c.EffectID &&
		receipt.PayloadSHA256 == c.PayloadSHA256 &&
		receipt.NamespaceID == c.Payload.NamespaceID &&
		receipt.ReleaseSHA256 == c.Payload.ReleaseSHA256 &&
		receipt.DataReceiptSHA256 == intent.dataReceiptSHA256 &&
		receipt.ProjectID == c.Payload.ProjectID &&
		receipt.DataFormat == c.Payload.DataFormat &&
		receipt.QuotaBytes == c.Payload.QuotaBytes &&
		receipt.QuotaInodes == c.Payload.QuotaInodes &&
		receipt.ExpectedDevice == c.Payload.ExpectedDevice &&
		receipt.ExpectedNamespaceInode ==
			c.Payload.ExpectedNamespaceInode &&
		receipt.WriterFence == c.Payload.WriterFence &&
		((receipt.AlreadyAbsent &&
			receipt.NamespaceDevice == "" &&
			receipt.NamespaceInode == "") ||
			(!receipt.AlreadyAbsent &&
				receipt.NamespaceDevice ==
					c.Payload.ExpectedDevice &&
				receipt.NamespaceInode ==
					c.Payload.ExpectedNamespaceInode))
}

type platformDeleteDataObservation interface {
	DeleteDataReceipt() (journal.DeleteDataReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformDeleteDataObservation(
	observation platformDeleteDataObservation,
	intent platformDeleteDataIntent,
	endpoint platformRuntimeEndpoint,
) (journal.DeleteDataReceipt, bool) {
	receipt, err := observation.DeleteDataReceipt()
	if err != nil ||
		!platformDeleteDataReceiptMatchesIntent(receipt, intent) ||
		observation.CommandSHA256() != platformDigest(intent.raw) ||
		observation.ReceivedAt().IsZero() {
		return journal.DeleteDataReceipt{}, false
	}
	a, b := observation.Controller(), intent.authority
	if a.DeploymentID != b.DeploymentID ||
		a.ControllerEpoch != b.ControllerEpoch ||
		a.ControlRevision != b.ControlRevision ||
		a.Fingerprint != b.Fingerprint ||
		!bytes.Equal(a.PublicKey, b.PublicKey) {
		return journal.DeleteDataReceipt{}, false
	}
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(
		receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(
		receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(
		intent.command.IssuedMS, 10, 64)
	if !peer.RootController || peer.Path != endpoint.path ||
		peer.RunnerUID != 0 ||
		peer.ControllerUID != endpoint.controllerUID ||
		peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second ||
		observedErr != nil || settledErr != nil ||
		issuedErr != nil || observed < issued ||
		observed > settled ||
		settled > observation.ReceivedAt().UnixMilli() {
		return journal.DeleteDataReceipt{}, false
	}
	return receipt, true
}

func (s *Server) settlePlatformDeleteData(
	ctx context.Context, expected platformOperationLease,
	intent platformDeleteDataIntent,
	receipt journal.DeleteDataReceipt,
) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUninstall ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRemoveData ||
		!platformDeleteDataReceiptMatchesIntent(receipt, intent) {
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
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedRemoval(
		e, expected, facts); err != nil {
		return zero, err
	}
	var state, digest, receiptRaw, runtime string
	var observed int64
	if err := e.queryRow(`SELECT state,payload_sha256,receipt_json,
		runtime_id,observed_fence
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='remove_data'
		AND effect_number=1 AND effect_id=?`,
		expected.id, intent.command.EffectID).Scan(
		&state, &digest, &receiptRaw, &runtime,
		&observed); err != nil || state != "prepared" ||
		receiptRaw != "{}" || observed != 0 ||
		digest != intent.command.PayloadSHA256 ||
		runtime != intent.runtime.id {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_data_namespaces
		SET state='deleted',owner_installation_id=NULL,
		observed_bytes=?,deleted_ms=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=?
		AND owner_installation_id=? AND state='active'`,
		mustPlatformCheckpointInt(receipt.UsedBytes), now, now,
		receipt.NamespaceID,
		expected.installationID, expected.installationID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operation_effects
		SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='remove_data'
		AND effect_number=1 AND effect_id=? AND state='prepared'
		AND receipt_json='{}' AND observed_fence=0`,
		expected.fence, string(encoded), now, expected.id,
		intent.command.EffectID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	complete, err := model.Advance(
		expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = complete.Snapshot()
	result.rowRevision++
	changed, err = e.exec(`UPDATE app_platform_installations
		SET phase='removed',revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='removed' AND phase='pending'
		AND generation=? AND revision=? AND control_fence=?
		AND controller_epoch=?`,
		now, expected.installationID,
		expected.snapshot.Generation, facts.installationRevision,
		expected.fence, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if _, err := e.exec(`DELETE FROM app_platform_capability_grants
		WHERE installation_id=?`, expected.installationID); err != nil {
		return zero, err
	}
	if _, err := e.exec(`DELETE FROM app_platform_access_grants
		WHERE installation_id=?`, expected.installationID); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='complete',state_revision=?,row_revision=row_revision+1,
		completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, now,
		expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	never, err := platformOperationNeverStarted(e, expected.id)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if never != nil {
		// The evidence was verified at the top of this transaction, before the namespace was marked deleted.
		if err := closePlatformNeverStartedInstall(
			e, never.OperationID, expected.id, now); err != nil {
			return zero, err
		}
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_deleted',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(mustPlatformJSON(struct {
			NamespaceID   string `json:"namespace_id"`
			Entries       string `json:"entries"`
			AlreadyAbsent bool   `json:"already_absent"`
		}{receipt.NamespaceID, receipt.Entries,
			receipt.AlreadyAbsent}))); err != nil {
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
