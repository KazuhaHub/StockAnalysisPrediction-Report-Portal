package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// What a raise of the data quota is, wherever it runs (doc 185 section 3.2): one signed set-data-quota command to the
// runner, one Portal effect row that tracks it, and one fenced transaction that applies the runner's receipt to the
// row and to the namespace together. A reviewed set_data_quota operation and an approved update run the same
// command; they differ in where the Portal keeps the row and in what else the transaction that applies it does.
//
// The runner's command header always names stage prepare_data, effect number 1, under the operation that raises:
// the runner's journal keeps one row per (operation, stage, effect number), and neither operation uses that slot for
// anything else. The Portal's own row sits in its operation's stage (prepare_data/1 for a reviewed raise,
// migrate/platformUpdateRaiseEffectNumber for an update), which is where its readers look for it.

// platformRaiseSlot is where a raise's Portal effect row lives.
type platformRaiseSlot struct {
	stage  string
	number int
}

var (
	platformStandaloneRaiseSlot = platformRaiseSlot{"prepare_data", 1}
	platformUpdateRaiseSlot     = platformRaiseSlot{"migrate", platformUpdateRaiseEffectNumber}
)

// platformRaiseFacts is what a raise command binds: the namespace, the evidence of its install, and the limits.
type platformRaiseFacts struct {
	namespaceID, releaseSHA256, projectID, namespaceInode, device string
	dataEffectID, dataReceiptSHA256                               string
	dataFormat, writerFence                                       int64
	oldBytes, oldInodes, newBytes, newInodes                      int64
}

func (f platformRaiseFacts) fromPlan(plan platformDataQuotaPlan) platformRaiseFacts {
	return platformRaiseFacts{
		namespaceID: plan.DataNamespaceID, releaseSHA256: plan.ReleaseSHA256, projectID: plan.ProjectID,
		namespaceInode: plan.NamespaceInode, device: plan.Device, dataEffectID: plan.DataEffectID,
		dataReceiptSHA256: plan.DataReceiptSHA256, dataFormat: plan.DataFormat, writerFence: plan.WriterFence,
		oldBytes: plan.CurrentQuotaBytes, oldInodes: plan.CurrentQuotaInodes, newBytes: plan.NewQuotaBytes, newInodes: plan.NewQuotaInodes,
	}
}

// platformRaiseCommand builds the signed-command body of a raise. fence is the installation fence the effect was
// admitted under, so a command signed again for the same effect describes it exactly.
func platformRaiseCommand(runtime platformRuntimeRegistration, epoch, installation, operation string, generation uint64, fence int64, f platformRaiseFacts, effectID string) (control.SetDataQuotaCommand, error) {
	command := control.SetDataQuotaCommand{CommandHeader: control.CommandHeader{
		Format: control.SetDataQuotaFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: epoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: installation, OperationID: operation,
		TargetGeneration: strconv.FormatUint(generation, 10), Stage: control.SetDataQuotaStage,
		EffectNumber: control.SetDataQuotaEffectNumber, EffectID: effectID, Kind: control.SetDataQuotaKind,
		InstallationFence: strconv.FormatInt(fence, 10),
	}, Payload: control.SetDataQuotaPayload{
		NamespaceID: f.namespaceID, ReleaseSHA256: f.releaseSHA256, DataFormat: strconv.FormatInt(f.dataFormat, 10),
		WriterFence: strconv.FormatInt(f.writerFence, 10), ProjectID: f.projectID, NamespaceInode: f.namespaceInode, Device: f.device,
		DataEffectID: f.dataEffectID, DataReceiptSHA256: f.dataReceiptSHA256,
		OldBytes: strconv.FormatInt(f.oldBytes, 10), OldInodes: strconv.FormatInt(f.oldInodes, 10),
		NewBytes: strconv.FormatInt(f.newBytes, 10), NewInodes: strconv.FormatInt(f.newInodes, 10),
	}}
	digest, err := control.SetDataQuotaLogicalDigest(command)
	if err != nil {
		return control.SetDataQuotaCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

// platformDataEffectFailedError is errPlatformDataEffectFailed with the runner's failure code.
type platformDataEffectFailedError struct{ code string }

func (e *platformDataEffectFailedError) Error() string {
	return errPlatformDataEffectFailed.Error() + ": " + e.code
}
func (e *platformDataEffectFailedError) Is(target error) bool {
	return target == errPlatformDataEffectFailed
}

// platformRaiseSeams are the runner calls of a raise. Tests replace them with a runner that answers from memory.
var (
	platformSetDataQuotaExecute = func(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		observation, err := platformExecuteSetDataQuota(ctx, endpoint, intent)
		if err != nil {
			return nil, err
		}
		return observation, nil
	}
	platformSetDataQuotaRead = func(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		observation, err := platformReadSetDataQuota(ctx, endpoint, intent)
		if err != nil {
			return nil, err
		}
		return observation, nil
	}
)

// sendPlatformSetDataQuota sends the raise and returns what the runner answered: a receipt to commit, or an error. A
// typed capacity refusal is returned as it came. A definite failure is a *platformDataEffectFailedError. When the
// acknowledgement is lost the effect is read back, never sent again under another identity; an answer that is
// neither a receipt nor a failure (the runner is still working, or knows nothing of the effect) is returned for the
// caller to commit, which refuses it, and the next attempt reads it again.
func sendPlatformSetDataQuota(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
	observation, err := platformSetDataQuotaExecute(ctx, endpoint, intent)
	if err != nil {
		var refusal *journal.CapacityRefusalError
		if errors.As(err, &refusal) {
			return nil, err
		}
		var readErr error
		observation, readErr = platformSetDataQuotaRead(ctx, endpoint, intent)
		if readErr != nil {
			return nil, errors.Join(err, readErr)
		}
	}
	if failure := platformRaiseFailure(observation, intent, endpoint); failure != nil {
		return nil, failure
	}
	return observation, nil
}

// platformRaiseFailure reports the runner's typed failed status for exactly this command; nil for anything else.
func platformRaiseFailure(observation platformSetDataQuotaObservation, intent platformSetDataQuotaIntent, endpoint platformRuntimeEndpoint) error {
	return platformDataEffectFailure(observation, intent.command.CommandHeader, intent.raw, intent.authority, endpoint)
}

// platformRaiseFailedReceipt is what a reverted raise row keeps: the runner's definite answer.
func platformRaiseFailedReceipt(intent platformSetDataQuotaIntent, code string) string {
	raw, _ := json.Marshal(struct {
		State         string `json:"state"`
		Code          string `json:"code"`
		EffectID      string `json:"effect_id"`
		PayloadSHA256 string `json:"payload_sha256"`
	}{"failed", code, intent.command.EffectID, intent.command.PayloadSHA256})
	return string(raw)
}

// platformRaiseAbsentReceipt is what a raise row keeps when the runner proved it never claimed the effect.
const platformRaiseAbsentReceipt = `{"state":"absent"}`

// platformRaiseRow is the Portal's row of a raise as read from the database.
type platformRaiseRow struct {
	effectID, kind, digest, runtimeID, epoch, state, receipt string
	admitted, observed                                       int64
	exists                                                   bool
}

func loadPlatformRaiseRow(e migExec, operation string, slot platformRaiseSlot, forUpdate bool) (platformRaiseRow, error) {
	lock := ""
	if forUpdate && e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var r platformRaiseRow
	var runtime sql.NullString
	err := e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?`+lock, operation, slot.stage, slot.number).
		Scan(&r.effectID, &r.kind, &r.digest, &runtime, &r.epoch, &r.admitted, &r.observed, &r.state, &r.receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return platformRaiseRow{}, nil
	}
	if err != nil {
		return platformRaiseRow{}, err
	}
	r.exists, r.runtimeID = true, runtime.String
	return r, nil
}

// signedUntil is the latest expiry of any command signed for the row, from its pending receipt.
func (r platformRaiseRow) signedUntil() (int64, bool) {
	if r.state != "prepared" && r.state != "unknown" {
		return 0, false
	}
	return platformImportSignedUntil(r.receipt)
}

// platformSignRaise signs a raise for the row and records the new signed window on it first, in the caller's
// transaction, so that no command exists outside the transaction whose window the row does not know. An absence
// answer is final only once every window has passed.
func platformSignRaise(e migExec, seed []byte, runtime platformRuntimeRegistration, row platformRaiseRow, slot platformRaiseSlot, operation string,
	command control.SetDataQuotaCommand, descriptor []byte, issued, expires int64, record bool) (platformSetDataQuotaIntent, error) {
	var zero platformSetDataQuotaIntent
	prior, ok := row.signedUntil()
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	if record {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND state=? AND observed_fence=0 AND receipt_json=?`,
			platformImportPendingReceipt(max(prior, expires)), operation, slot.stage, slot.number, row.effectID, row.state, row.receipt)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	}
	authority := platformRuntimeAuthority(runtime)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	intent := platformSetDataQuotaIntent{
		effectID: row.effectID, runtime: runtime, authority: authority, command: command, descriptor: bytes.Clone(descriptor),
		slot: slot, admittedFence: row.admitted, priorSignedUntil: prior, rowState: row.state, rowReceipt: row.receipt, recorded: record,
	}
	var err error
	if intent.raw, intent.signature, err = control.SignSetDataQuota(command, key, authority, issued); err != nil {
		return zero, err
	}
	if intent.readSignature, err = control.SignSetDataQuotaRead(intent.raw, key, authority, issued); err != nil {
		return zero, err
	}
	return intent, nil
}

// platformRaiseWindow is the signed window of a raise signed now inside a lease: it must start within a second of the
// database clock and end before the lease does.
func platformRaiseWindow(now, leaseUntil int64) (issued, expires int64, err error) {
	if now > math.MaxInt64-25000 {
		return 0, 0, errAppPlatformLeaseConflict
	}
	issued = time.Now().UnixMilli()
	expires = issued + 25000
	if leaseUntil > 0 {
		expires = min(expires, leaseUntil-1000)
	}
	if issued < now-1000 || issued > now+1000 || expires <= issued {
		return 0, 0, errAppPlatformLeaseConflict
	}
	return issued, expires, nil
}

// platformRaiseCommitRef names the row a receipt settles.
type platformRaiseCommitRef struct {
	operation, installation, namespace string
	slot                               platformRaiseSlot
	effectID, digest, runtimeID, epoch string
	admittedFence, observedFence       int64
}

// applyPlatformRaise applies a verified receipt in the caller's transaction: the effect row goes prepared (or
// unknown) to applied with the receipt, and the namespace row takes the new limits only if it still holds the old
// ones. Both changes or neither: the row and the quota chain never disagree. The row is written once: nothing
// rewrites an applied raise row, because the chain is derived from it.
func applyPlatformRaise(e migExec, ref platformRaiseCommitRef, receipt journal.SetDataQuotaReceipt, encoded string, now int64) error {
	newBytes, newInodes := mustPlatformCheckpointInt(receipt.NewBytes), mustPlatformCheckpointInt(receipt.NewInodes)
	oldBytes, oldInodes := mustPlatformCheckpointInt(receipt.OldBytes), mustPlatformCheckpointInt(receipt.OldInodes)
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind='set-data-quota' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state IN ('prepared','unknown') AND observed_fence=0`,
		ref.observedFence, encoded, now, ref.operation, ref.slot.stage, ref.slot.number, ref.effectID, ref.digest,
		ref.runtimeID, ref.epoch, ref.admittedFence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET quota_bytes=?,quota_inodes=?,observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active' AND quota_bytes=? AND quota_inodes=?`,
		newBytes, newInodes, mustPlatformCheckpointInt(receipt.UsedBytes), now, ref.namespace,
		ref.installation, ref.installation, oldBytes, oldInodes)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		NewBytes   string `json:"new_quota_bytes"`
		NewInodes  string `json:"new_quota_inodes"`
		OldBytes   string `json:"old_quota_bytes"`
		OldInodes  string `json:"old_quota_inodes"`
		UsedBytes  string `json:"used_bytes"`
		UsedInodes string `json:"used_inodes"`
	}{receipt.NewBytes, receipt.NewInodes, receipt.OldBytes, receipt.OldInodes, receipt.UsedBytes, receipt.UsedInodes})
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_quota_raised','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), ref.operation, string(detail))
	return err
}

// platformEncodeRaiseReceipt re-encodes a runner receipt canonically and proves the encoding parses back to it.
func platformEncodeRaiseReceipt(receipt journal.SetDataQuotaReceipt) (string, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return "", errAppPlatformStageEvidence
	}
	if parsed, parseErr := journal.ParseSetDataQuotaReceipt(encoded); parseErr != nil || parsed != receipt {
		return "", errAppPlatformStageEvidence
	}
	return string(encoded), nil
}
