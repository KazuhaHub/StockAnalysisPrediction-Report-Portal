package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Private repository seam for transaction tests. The endpoint entry point below
// is its only production producer; no request body can provide this observation.
type platformDataObservation struct {
	receipt                journal.DataPreparationReceipt
	authority              control.Authority
	endpoint               transport.DataReceiptEndpoint
	receivedAt             time.Time
	requestSHA, commandSHA string
}

func (s *Server) completePlatformInstallData(ctx context.Context, expected platformOperationLease, intent platformDataIntent, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	proof, err := platformReadDataReceipt(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := ctx.Err(); err != nil {
		return platformOperationLease{}, err
	}
	observation := platformDataObservation{receipt: proof.Receipt(), authority: proof.Controller(), endpoint: proof.Endpoint(), receivedAt: proof.ReceivedAt(), requestSHA: proof.RequestSHA256(), commandSHA: proof.CommandSHA256()}
	return s.commitPlatformObservedData(ctx, expected, intent, endpoint, observation)
}

// dispatchPlatformInstallData persists/revalidates the exact intent before
// requesting native execution. Uncertain responses retain this durable pair;
// a later call can only reconcile its same identity under current authority.
func (s *Server) dispatchPlatformInstallData(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformInstallData(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformPrepareData(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err = ctx.Err(); err != nil {
		return platformOperationLease{}, err
	}
	observation := platformDataObservation{receipt: proof.Receipt(), authority: proof.Controller(), endpoint: proof.Endpoint(), receivedAt: proof.ReceivedAt(), requestSHA: proof.RequestSHA256(), commandSHA: proof.CommandSHA256()}
	return s.commitPlatformObservedData(ctx, expected, intent, endpoint, observation)
}

func validPlatformDataObservation(observation platformDataObservation, intent platformDataIntent, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(observation.receivedAt)
	a, b := observation.authority, intent.authority
	peer := observation.endpoint
	return !observation.receivedAt.IsZero() && age >= 0 && age <= transport.MaxDataReceiptAge && peer.RootController && peer.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && peer.Path == endpoint.path && peer.ControllerUID == endpoint.controllerUID && peer.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && platformValidSHA(observation.requestSHA) && observation.commandSHA == platformDigest(intent.raw) && observation.receipt.MatchesCommand(intent.command)
}

// Only an authenticated observed result can advance prepare_data. Resource,
// effect and operation updates commit together; this is not app activation.
func (s *Server) commitPlatformObservedData(ctx context.Context, expected platformOperationLease, intent platformDataIntent, endpoint platformRuntimeEndpoint, observation platformDataObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionInstall || expected.snapshot.Managed || expected.snapshot.Generation != 1 || expected.snapshot.Stage != appplatform.StagePrepareData || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	observation.authority.PublicKey = bytes.Clone(observation.authority.PublicKey)
	if !validPlatformDataObservation(observation, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
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
	clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	current, _, err := loadPlatformDataIntent(e, expected, runtime, facts.now, false)
	if err != nil {
		return zero, err
	}
	if runtime.id != intent.runtime.id || runtime.revision != intent.runtime.revision || current.namespaceID != intent.namespaceID || current.effectID != intent.effectID {
		return zero, errAppPlatformStageEvidence
	}
	current.command.IssuedMS, current.command.ExpiresMS = intent.command.IssuedMS, intent.command.ExpiresMS
	if current.command != intent.command || current.authority.DeploymentID != intent.authority.DeploymentID || current.authority.ControllerEpoch != intent.authority.ControllerEpoch || current.authority.ControlRevision != intent.authority.ControlRevision || current.authority.Fingerprint != intent.authority.Fingerprint || !bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := control.Verify(intent.raw, intent.signature, current.authority, time.Now().UnixMilli())
	if err != nil || verified.Command() != current.command {
		return zero, errAppPlatformStageEvidence
	}
	if !validPlatformDataObservation(observation, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	observed, err := strconv.ParseInt(observation.receipt.ObservedMS, 10, 64)
	if err != nil || observed > time.Now().UnixMilli() {
		return zero, errAppPlatformStageEvidence
	}
	raw, err := json.Marshal(observation.receipt)
	if err != nil || len(raw) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := journal.ParseDataPreparationReceipt(raw); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	schema, _ := strconv.ParseInt(current.command.Payload.DataFormat, 10, 64)
	used, _ := strconv.ParseInt(observation.receipt.UsedBytes, 10, 64)
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_data_namespaces SET state='active',data_format=?,observed_bytes=?,writer_fence=1,revision=2,updated_ms=? WHERE id=? AND owner_installation_id=? AND state='review_required' AND data_format=0 AND writer_fence=0 AND revision=1`, schema, used, now, current.namespaceID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='prepare_data' AND effect_number=1 AND effect_id=? AND state='prepared' AND receipt_json='{}' AND admitted_fence=? AND observed_fence=0`, expected.fence, string(raw), now, expected.id, current.effectID, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageActivate)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = next.Snapshot()
	result.rowRevision++
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='activate',state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='prepare_data' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage     string `json:"stage"`
		Namespace string `json:"namespace_id"`
		Effect    string `json:"effect_id"`
	}{"activate", current.namespaceID, current.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if !validPlatformDataObservation(observation, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.Verify(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
