package app

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformWritableHistoryEffect struct {
	id, digest, receipt string
	created, updated    int64
}

func loadPlatformWritableHistoryEffect(e migExec, source platformManagedSource, runtime, stage, kind string, number int, completed int64) (platformWritableHistoryEffect, error) {
	var r platformWritableHistoryEffect
	err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=? AND kind=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND observed_fence=? AND state='applied'`, source.operationID, stage, number, kind, runtime, source.epoch, source.fence, source.fence).Scan(&r.id, &r.digest, &r.receipt, &r.created, &r.updated)
	if err != nil || !platformValidUUID(r.id) || !platformValidSHA(r.digest) || r.created < 0 || r.updated < r.created || r.updated > completed {
		return platformWritableHistoryEffect{}, errAppPlatformLaunchUnavailable
	}
	return r, nil
}

// Completed history selects immutable execution identity; it is not a fabricated
// live worker lease. Current session/OU/audience/trust comes from the outer gate.
func (s *Server) loadPlatformWritableBackendHistory(e migExec, b platformBackendHistoryBinding, runtime platformRuntimeRegistration, seed, dek []byte, now int64) (platformWritableRegistrationIntent, error) {
	var zero platformWritableRegistrationIntent
	if len(seed) != ed25519.SeedSize || b.observed < 0 || b.observed > now+1000 || b.observed > time.Now().UnixMilli() {
		return zero, errAppPlatformLaunchUnavailable
	}
	source := platformManagedSource{installationID: b.installation, epoch: b.epoch, generation: 1, fence: b.fence}
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=?`, b.installation).Scan(&count); err != nil || count != 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	var rawPlan, planSHA string
	var descriptor []byte
	var completed, created, updated int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,r.descriptor,o.completed_ms,o.created_ms,o.updated_ms FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.installation_id=? AND o.action='install' AND o.managed=1 AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL AND o.controller_epoch=? AND o.fence=? AND p.action='install' AND p.app_id=r.app_id AND p.publisher_id=r.publisher_id AND p.target_release_sha256=? AND p.controller_epoch=? AND p.consumed_ms IS NOT NULL`, b.installation, b.epoch, b.fence, b.release, b.epoch).Scan(&source.operationID, &rawPlan, &planSHA, &descriptor, &completed, &created, &updated); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if !platformValidUUID(source.operationID) || created < 0 || completed < created || b.observed > completed+1000 || updated != completed || updated > now || platformDigest([]byte(rawPlan)) != planSHA {
		return zero, errAppPlatformLaunchUnavailable
	}
	var plan platformInstallProjection
	if json.Unmarshal([]byte(rawPlan), &plan) != nil || plan.Format != platformWritableInstallFormat || plan.ControllerEpoch != b.epoch || plan.ReleaseSHA256 != b.release || plan.Runtime == nil || plan.DataQuotaBytes != 256<<20 {
		return zero, errAppPlatformLaunchUnavailable
	}
	canonical, _ := json.Marshal(plan)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || !bytes.Equal(canonical, []byte(rawPlan)) || parsed.SHA256 != b.release || parsed.Manifest.ID != plan.AppID || parsed.Manifest.Publisher != plan.PublisherID || int64(parsed.Manifest.Data.Schema) != b.format || installRequirementsForTarget(parsed.Manifest, plan.Format, plan.Runtime) != nil || validateManagedInstallTarget(e, plan.Runtime, b.epoch) != nil || plan.Runtime.RuntimeID != runtime.id {
		return zero, errAppPlatformLaunchUnavailable
	}
	var w platformWriterIntent
	w.namespaceID, w.resourceID, w.runtime, w.authority, w.descriptor = b.namespace, b.activation, runtime, platformRuntimeAuthority(runtime), bytes.Clone(descriptor)
	var owner sql.NullString
	var origin, state string
	var format, quota, used, writer, revision int64
	var retained, deleted sql.NullInt64
	if err := e.queryRow(`SELECT origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms FROM app_platform_data_namespaces WHERE id=?`, b.namespace).Scan(&origin, &owner, &state, &format, &quota, &used, &writer, &revision, &retained, &deleted, &w.namespaceCreated, &w.namespaceUpdated); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if origin != b.installation || !owner.Valid || owner.String != b.installation || state != "active" || format != b.format || quota != 256<<20 || used < 0 || used > quota || writer != 1 || revision != 3 || retained.Valid || deleted.Valid || w.namespaceCreated < 0 || w.namespaceUpdated < w.namespaceCreated || w.namespaceUpdated > completed {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.namespaceRevision = revision
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?`, b.installation, b.installation).Scan(&count); err != nil || count != 1 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, source.operationID).Scan(&count); err != nil || count != 4 {
		return zero, errAppPlatformLaunchUnavailable
	}
	if err := e.queryRow(`SELECT created_ms,updated_ms FROM app_platform_activations WHERE id=? AND installation_id=? AND release_sha256=? AND data_namespace_id=? AND generation=1 AND data_format=? AND state='active' AND readiness_evidence=? AND observed_ms=?`, b.activation, b.installation, b.release, b.namespace, b.format, b.readiness, b.observed).Scan(&w.activationCreated, &w.activationUpdated); err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	if w.activationCreated < 0 || w.activationUpdated < completed || w.activationUpdated > now {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.activationReadiness, w.activationObserved = b.readiness, sql.NullInt64{Valid: true, Int64: b.observed}
	dataEffect, err := loadPlatformWritableHistoryEffect(e, source, runtime.id, "prepare_data", "prepare-data", 1, completed)
	if err != nil {
		return zero, err
	}
	dataCommand, err := platformDataCommandForSource(e, source, runtime, b.namespace, dataEffect.id)
	if err != nil || dataCommand.PayloadSHA256 != dataEffect.digest {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.data, err = journal.ParseDataPreparationReceipt([]byte(dataEffect.receipt))
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	original := dataCommand
	original.ControlRevision = w.data.ControlRevision
	dataRevision, _ := strconv.ParseInt(original.ControlRevision, 10, 64)
	dataClaimed, _ := strconv.ParseInt(w.data.Claim.ClaimedMS, 10, 64)
	dataObserved, _ := strconv.ParseInt(w.data.ObservedMS, 10, 64)
	if dataRevision > runtime.controlRevision || !w.data.MatchesCommand(original) || w.data.Claim.RootFilesystemUUID != plan.Runtime.Profile.RootFilesystemUUID || w.data.Claim.RootInode != plan.Runtime.Profile.RootInode || w.namespaceCreated != dataEffect.created || dataEffect.created > dataClaimed+1000 || dataObserved > dataEffect.updated+1000 || dataObserved > time.Now().UnixMilli() {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.dataCreated, w.dataUpdated, w.dataSHA = dataEffect.created, dataEffect.updated, platformDigest([]byte(dataEffect.receipt))
	writerEffect, err := loadPlatformWritableHistoryEffect(e, source, runtime.id, "prepare_data", "activate", 2, completed)
	if err != nil {
		return zero, err
	}
	if writerEffect.created < dataEffect.updated || w.namespaceUpdated != writerEffect.updated || w.activationCreated != writerEffect.created || b.activation == dataEffect.id || b.activation == b.namespace || writerEffect.id == b.namespace {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.effectID, w.effectCreated, w.effectUpdated = writerEffect.id, writerEffect.created, writerEffect.updated
	w.receipt, err = journal.ParseDataWriterReceipt([]byte(writerEffect.receipt))
	if err != nil || w.receipt.Native.UsedBytes != strconv.FormatInt(used, 10) {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.applied = true
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLaunchUnavailable
	}
	dataCommand.IssuedMS, dataCommand.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+30000, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	dataRaw, dataSig, err := control.Sign(dataCommand, key, w.authority, issued)
	if err != nil {
		return zero, err
	}
	w.command = control.DataWriterCommand{Format: control.DataWriterFormat, EffectID: writerEffect.id, DataRaw: dataRaw, DataSignature: dataSig, DataReceiptSHA256: w.dataSHA, ResourceID: b.activation, Platform: plan.Runtime.Profile.Platform, SandboxProfile: control.DataWriterSandboxProfile}
	w.command.PayloadSHA256, err = control.DataWriterLogicalDigest(w.command)
	writerClaimed, _ := strconv.ParseInt(w.receipt.Claim.ClaimedMS, 10, 64)
	writerSettled, _ := strconv.ParseInt(w.receipt.SettledMS, 10, 64)
	if err != nil || w.command.PayloadSHA256 != writerEffect.digest || writerClaimed+1000 < writerEffect.created || writerSettled > writerEffect.updated+1000 || !platformWriterReceiptMatchesFence(w.receipt, w, b.fence, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	w.raw, w.signature, err = control.SignDataWriter(w.command, key, w.authority, issued)
	if err != nil {
		return zero, err
	}
	startEffect, err := loadPlatformWritableHistoryEffect(e, source, runtime.id, "start", "start-candidate", 1, completed)
	if err != nil {
		return zero, err
	}
	if startEffect.created < writerEffect.updated {
		return zero, errAppPlatformLaunchUnavailable
	}
	start := platformWritableStartIntent{writer: w, effectCreated: startEffect.created, effectUpdated: startEffect.updated, applied: true}
	start.command = control.WritableCandidateCommand{Format: control.WritableCandidateFormat, EffectID: startEffect.id, WriterRaw: bytes.Clone(w.raw), WriterSignature: bytes.Clone(w.signature), WriterReceiptSHA256: platformDigest([]byte(writerEffect.receipt)), SandboxProfile: control.WritableCandidateSandboxProfile}
	start.command.PayloadSHA256, err = control.WritableCandidateLogicalDigest(start.command)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	start.running, err = journal.ParseWritableCandidateRunningReceipt([]byte(startEffect.receipt))
	if err != nil || start.command.PayloadSHA256 != startEffect.digest || !platformWritableRunningSourceMatches(start.running, start, source, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	startSettled, _ := strconv.ParseInt(start.running.SettledMS, 10, 64)
	if startSettled > startEffect.updated+1000 || !validStoredPlatformWritableReadiness(start, now) {
		return zero, errAppPlatformLaunchUnavailable
	}
	start.raw, start.signature, err = control.SignWritableCandidate(start.command, key, w.authority, issued)
	if err != nil {
		return zero, err
	}
	registration, err := platformBackendRegistrationForRelease(e, source, runtime, descriptor, b.release, dek)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			registration.Close()
		}
	}()
	document, err := registration.Document()
	if err != nil {
		return zero, err
	}
	defer clear(document)
	regEffect, err := loadPlatformWritableHistoryEffect(e, source, runtime.id, "activate", "activate", 1, completed)
	if err != nil {
		return zero, err
	}
	if regEffect.created < startEffect.updated {
		return zero, errAppPlatformLaunchUnavailable
	}
	result := platformWritableRegistrationIntent{start: start, registration: registration, applied: true, created: regEffect.created, updated: regEffect.updated}
	result.command = control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: regEffect.id, CandidateRaw: bytes.Clone(start.raw), CandidateSignature: bytes.Clone(start.signature), RunningReceiptSHA256: platformDigest([]byte(startEffect.receipt)), DocumentSHA256: platformDigest(document)}
	result.command.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(result.command)
	if err != nil || result.command.PayloadSHA256 != regEffect.digest {
		return zero, errAppPlatformLaunchUnavailable
	}
	result.receipt, err = journal.ParseWritableBackendRegistrationReceipt([]byte(regEffect.receipt))
	if err != nil || !platformWritableRegistrationReceiptMatches(result.receipt, result, completed) {
		return zero, errAppPlatformLaunchUnavailable
	}
	regSettled, _ := strconv.ParseInt(result.receipt.SettledMS, 10, 64)
	if regSettled > regEffect.updated+1000 {
		return zero, errAppPlatformLaunchUnavailable
	}
	result.raw, result.signature, err = control.SignWritableBackendRegistration(result.command, key, w.authority, issued)
	if err != nil {
		return zero, err
	}
	failed = false
	return result, nil
}
