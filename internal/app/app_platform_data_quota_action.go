package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The reviewed set_data_quota action (doc 185 section 3.2): raise the quota of one installation's namespace in one
// fenced effect, online. Stages: queued, validate, prepare_data, complete. It changes no generation. The operation
// that completes the raise also updates the namespace row, so the row and the quota chain never disagree.

const platformSetDataQuotaFormat = "portal.managed-data-quota-review/1-dev.1"

type platformDataQuotaPlan struct {
	Format             string                        `json:"format"`
	Action             appplatform.Action            `json:"action"`
	InstallationID     string                        `json:"installation_id"`
	AppID              string                        `json:"app_id"`
	PublisherID        string                        `json:"publisher_id"`
	ReleaseSHA256      string                        `json:"release_sha256"`
	Generation         int64                         `json:"generation"`
	PolicyRevision     int64                         `json:"policy_revision"`
	TrustRevision      int64                         `json:"trust_revision"`
	ControllerEpoch    string                        `json:"controller_epoch"`
	DataNamespaceID    string                        `json:"data_namespace_id"`
	DataFormat         int64                         `json:"data_format"`
	WriterFence        int64                         `json:"writer_fence"`
	ProjectID          string                        `json:"project_id"`
	NamespaceInode     string                        `json:"namespace_inode"`
	Device             string                        `json:"device"`
	DataEffectID       string                        `json:"data_effect_id"`
	DataReceiptSHA256  string                        `json:"data_receipt_sha256"`
	CurrentQuotaBytes  int64                         `json:"current_quota_bytes"`
	CurrentQuotaInodes int64                         `json:"current_quota_inodes"`
	NewQuotaBytes      int64                         `json:"new_quota_bytes"`
	NewQuotaInodes     int64                         `json:"new_quota_inodes"`
	ObservedBytes      int64                         `json:"observed_bytes"`
	Capacity           string                        `json:"capacity"`
	CapacityDimension  string                        `json:"capacity_dimension,omitempty"`
	CapacityRequested  int64                         `json:"capacity_requested,omitempty"`
	CapacityRemaining  int64                         `json:"capacity_remaining,omitempty"`
	TargetRuntime      *platformManagedInstallTarget `json:"target_runtime"`
}

// platformBuildDataQuotaPlan reads the installation and its namespace and builds the review, without the runner's
// capacity answer. pending is how many unfinished operations the installation may hold: none while reviewing, the
// raise itself while it runs.
func platformBuildDataQuotaPlan(e migExec, installation string, target *platformManagedInstallTarget, runtime platformRuntimeRegistration, newBytes, newInodes, now int64, pending int) (platformDataQuotaPlan, int64, platformCheckpointSource, error) {
	var zero platformDataQuotaPlan
	var noSource platformCheckpointSource
	var appID, publisher, desired, phase, epoch, runtimeID string
	var generation, revision, policyRevision, trustRevision int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`, installation).
		Scan(&appID, &publisher, &desired, &phase, &generation, &revision, &policyRevision, &epoch, &runtimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, 0, noSource, errAppPlatformPlanChanged
		}
		return zero, 0, noSource, err
	}
	allowDisabled := desired == "disabled" && phase == "disabled"
	if runtimeID != runtime.id || epoch != runtime.controllerEpoch || policyRevision < 1 || generation < 1 || generation == math.MaxInt64 ||
		!((desired == "enabled" && phase == "ready") || allowDisabled) {
		return zero, 0, noSource, errAppPlatformPlanChanged
	}
	lease := platformOperationLease{
		installationID: installation, controllerEpoch: epoch,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionSetDataQuota, Managed: true, Generation: uint64(generation + 1), Revision: 1, Stage: appplatform.StageQueued},
	}
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, allowDisabled, pending)
	if err != nil {
		return zero, 0, noSource, fmt.Errorf("data quota source: %w", err)
	}
	if source.runtimeTarget == nil || target == nil || !samePlatformManagedUpdateRuntime(source.runtimeTarget, target) {
		return zero, 0, noSource, errAppPlatformPlanChanged
	}
	currentBytes, currentInodes := mustPlatformCheckpointInt(source.quotaBytes), mustPlatformCheckpointInt(source.quotaInodes)
	switch {
	case newBytes < currentBytes || newInodes < currentInodes:
		return zero, 0, noSource, errPlatformDataQuotaLower
	case (newBytes == currentBytes && newInodes == currentInodes) || !appplatform.ValidDataQuota(newBytes, newInodes):
		return zero, 0, noSource, errPlatformDataQuotaInvalid
	}
	var observed int64
	if err := e.queryRow(`SELECT observed_bytes FROM app_platform_data_namespaces WHERE id=?`, source.namespaceID).Scan(&observed); err != nil {
		return zero, 0, noSource, errAppPlatformPlanChanged
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, publisher).Scan(&trustRevision); err != nil || trustRevision < 1 {
		return zero, 0, noSource, errAppPlatformPlanChanged
	}
	writer := mustPlatformCheckpointInt(source.writerFence)
	if source.currentWriterFence != "" {
		writer = mustPlatformCheckpointInt(source.currentWriterFence)
	}
	return platformDataQuotaPlan{
		Format: platformSetDataQuotaFormat, Action: appplatform.ActionSetDataQuota, InstallationID: installation,
		AppID: appID, PublisherID: publisher, ReleaseSHA256: source.releaseSHA256, Generation: generation,
		PolicyRevision: policyRevision, TrustRevision: trustRevision, ControllerEpoch: epoch,
		DataNamespaceID: source.namespaceID, DataFormat: mustPlatformCheckpointInt(source.dataFormat), WriterFence: writer,
		ProjectID: source.projectID, NamespaceInode: source.namespaceInode, Device: source.device,
		DataEffectID: source.dataEffectID, DataReceiptSHA256: source.sourceDataReceiptSHA256,
		CurrentQuotaBytes: currentBytes, CurrentQuotaInodes: currentInodes, NewQuotaBytes: newBytes, NewQuotaInodes: newInodes,
		ObservedBytes: observed, Capacity: "fits", TargetRuntime: target,
	}, revision, source, nil
}

// platformDataQueryRequest is a signed query ready to send.
type platformDataQueryRequest struct {
	command        control.DataQueryCommand
	authority      control.Authority
	raw, signature []byte
	descriptor     []byte
}

// signPlatformDataQuery signs a query for the installation, inside a transaction that holds the controller key.
func (s *Server) signPlatformDataQuery(e migExec, epoch, releaseSHA string, descriptor []byte, installation string, payload control.DataQueryPayload) (platformDataQueryRequest, error) {
	var zero platformDataQueryRequest
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	operationID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	effectID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	var fence int64
	if err := e.queryRow(`SELECT control_fence FROM app_platform_installations WHERE id=?`, installation).Scan(&fence); err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	payload.ReleaseSHA256 = releaseSHA
	command := control.DataQueryCommand{CommandHeader: control.CommandHeader{
		Format: control.DataQueryFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: epoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: installation, OperationID: operationID,
		TargetGeneration: "1", Stage: control.DataQueryStage, EffectNumber: control.DataQueryEffectNumber, EffectID: effectID,
		Kind: control.DataQueryKind, InstallationFence: strconv.FormatInt(max(fence, 1), 10),
		IssuedMS: strconv.FormatInt(issued, 10), ExpiresMS: strconv.FormatInt(issued+25000, 10),
	}, Payload: payload}
	digest, err := control.DataQueryLogicalDigest(command)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	authority := platformRuntimeAuthority(runtime)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignDataQuery(command, key, authority, issued)
	if err != nil {
		return zero, err
	}
	return platformDataQueryRequest{command: command, authority: authority, raw: raw, signature: signature, descriptor: bytes.Clone(descriptor)}, nil
}

type platformDataQueryObservation interface {
	DataQueryReport() (journal.DataQueryReport, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

// platformObservedBinding checks that an observation came over the bound endpoint, from the controller that signed
// the command, a moment ago.
func platformObservedBinding(controller control.Authority, peer transport.CandidateReceiptEndpoint, received time.Time, commandSHA string, raw []byte, authority control.Authority, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(received)
	return commandSHA == platformDigest(raw) && !received.IsZero() &&
		controller.DeploymentID == authority.DeploymentID && controller.ControllerEpoch == authority.ControllerEpoch &&
		controller.ControlRevision == authority.ControlRevision && controller.Fingerprint == authority.Fingerprint &&
		bytes.Equal(controller.PublicKey, authority.PublicKey) &&
		peer.RootController && peer.Path == endpoint.path && peer.RunnerUID == 0 &&
		peer.ControllerUID == endpoint.controllerUID && peer.ControllerGID == endpoint.controllerGID && age >= 0 && age <= 3*time.Second
}

func validPlatformDataQueryObservation(observation platformDataQueryObservation, request platformDataQueryRequest, endpoint platformRuntimeEndpoint) (journal.DataQueryReport, bool) {
	report, err := observation.DataQueryReport()
	if err != nil || !journal.DataQueryReportMatchesCommand(report, request.command) ||
		!platformObservedBinding(observation.Controller(), observation.Endpoint(), observation.ReceivedAt(), observation.CommandSHA256(), request.raw, request.authority, endpoint) {
		return journal.DataQueryReport{}, false
	}
	return report, true
}

// platformDataQueryRunner sends a signed query. Tests replace it with a runner that answers from memory.
var platformDataQueryRunner = func(ctx context.Context, endpoint platformRuntimeEndpoint, request platformDataQueryRequest) (platformDataQueryObservation, error) {
	observation, err := platformRunDataQuery(ctx, endpoint, request)
	if err != nil {
		return nil, err
	}
	return observation, nil
}

// queryPlatformData sends a signed query to the runner and returns its validated report.
func (s *Server) queryPlatformData(ctx context.Context, endpoint platformRuntimeEndpoint, request platformDataQueryRequest) (journal.DataQueryReport, error) {
	observation, err := platformDataQueryRunner(ctx, endpoint, request)
	if err != nil {
		return journal.DataQueryReport{}, err
	}
	report, ok := validPlatformDataQueryObservation(observation, request, endpoint)
	if !ok {
		return journal.DataQueryReport{}, errAppPlatformStageEvidence
	}
	return report, nil
}

// createPlatformSetDataQuotaReview writes the review of a raise: the current and new limits, the usage and the
// runner's answer to whether its capacity can hold the new reservation.
func (s *Server) createPlatformSetDataQuotaReview(ctx context.Context, session platformActorSession, installation string, newBytes, newInodes int64, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformSetDataQuotaReviewWithRuntimeContext(ctx, session, installation, newBytes, newInodes, target, facts.runtime, endpoint)
}

func (s *Server) createPlatformSetDataQuotaReviewWithRuntimeContext(ctx context.Context, session platformActorSession, installation string, newBytes, newInodes int64, target *platformManagedInstallTarget, observedRuntime platformRuntimeRegistration, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	var query platformDataQueryRequest
	var base platformDataQuotaPlan
	// First: build the review's facts and sign the capacity question.
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		if err := lockOUMutation(e); err != nil {
			return err
		}
		runtime, err := s.platformRuntimeFor(e, control.epoch)
		if err != nil || !samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		plan, _, source, err := platformBuildDataQuotaPlan(e, installation, target, runtime, newBytes, newInodes, control.nowMS, 0)
		if err != nil {
			return err
		}
		base = plan
		query, err = s.signPlatformDataQuery(e, control.epoch, source.releaseSHA256, source.descriptor, installation, platformDataQueryPayload(plan))
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	report, err := s.queryPlatformData(ctx, endpoint, query)
	if err != nil {
		return platformRemovalReview{}, err
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
		plan, revision, _, err := platformBuildDataQuotaPlan(e, installation, target, runtime, newBytes, newInodes, control.nowMS, 0)
		if err != nil {
			return err
		}
		if !samePlatformDataQuotaFacts(plan, base) {
			return errAppPlatformPlanChanged
		}
		if !report.CapacityFits {
			plan.Capacity, plan.CapacityDimension = "short", report.CapacityDim
			plan.CapacityRequested, plan.CapacityRemaining = mustPlatformCheckpointInt(report.CapacityRequired), mustPlatformCheckpointInt(report.CapacityLeft)
		}
		verified, err := platformVerifyStoredRelease(e, plan.ReleaseSHA256, plan.TrustRevision, trust, control.nowMS)
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
			id, installation, plan.AppID, plan.PublisherID, string(appplatform.ActionSetDataQuota), plan.ReleaseSHA256, plan.ReleaseSHA256,
			revision, plan.PolicyRevision, plan.TrustRevision, plan.ControllerEpoch, actor.scope, actor.username, actor.created,
			actor.generation, actor.authorityFingerprint, result.SHA256, result.JSON, result.ExpiresMS, control.nowMS, control.nowMS); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan  string `json:"plan_id"`
			Bytes int64  `json:"quota_bytes"`
			Inode int64  `json:"quota_inodes"`
		}{id, plan.NewQuotaBytes, plan.NewQuotaInodes})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,?,'app_platform.data_quota_review','app_platform_installation',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, installation, string(detail))
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func platformDataQueryPayload(plan platformDataQuotaPlan) control.DataQueryPayload {
	return control.DataQueryPayload{
		Operation: control.DataQueryQuotaCapacity, NamespaceID: plan.DataNamespaceID,
		QuotaBytes: strconv.FormatInt(plan.NewQuotaBytes, 10), QuotaInodes: strconv.FormatInt(plan.NewQuotaInodes, 10),
	}
}

// samePlatformDataQuotaFacts compares two builds of a review without the runner's capacity answer.
func samePlatformDataQuotaFacts(a, b platformDataQuotaPlan) bool {
	a.Capacity, a.CapacityDimension, a.CapacityRequested, a.CapacityRemaining = "", "", 0, 0
	b.Capacity, b.CapacityDimension, b.CapacityRequested, b.CapacityRemaining = "", "", 0, 0
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ra, rb)
}

func (s *Server) consumePlatformSetDataQuotaReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
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
	return s.consumePlatformSetDataQuotaReviewWithRuntimeContext(ctx, session, planID, planSHA, key, target, facts.runtime)
}

func (s *Server) consumePlatformSetDataQuotaReviewWithRuntimeContext(ctx context.Context, session platformActorSession, planID, planSHA, key string, target *platformManagedInstallTarget, observedRuntime platformRuntimeRegistration) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		found, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			result = found
			return nil
		}
		var installation, username sql.NullString
		var raw, digest, storedScope, actionText, created string
		var expires, accountGeneration int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT installation_id,plan_json,plan_sha256,actor_scope,username,account_created,account_generation,expires_ms,consumed_ms,action
			FROM app_platform_review_plans WHERE id=?`, planID).
			Scan(&installation, &raw, &digest, &storedScope, &username, &created, &accountGeneration, &expires, &consumed, &actionText); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if storedScope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !installation.Valid || appplatform.Action(actionText) != appplatform.ActionSetDataQuota {
			return errAppPlatformPlanChanged
		}
		if err := lockOUMutation(e); err != nil {
			return err
		}
		runtime, err := s.platformRuntimeFor(e, control.epoch)
		if err != nil || !samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		var stored platformDataQuotaPlan
		if json.Unmarshal([]byte(raw), &stored) != nil || len(raw) > 65536 || platformDigest([]byte(raw)) != digest || digest != planSHA {
			return errAppPlatformPlanChanged
		}
		if stored.Capacity != "fits" {
			return errPlatformDataQuotaCapacity
		}
		current, revision, _, err := platformBuildDataQuotaPlan(e, installation.String, target, runtime, stored.NewQuotaBytes, stored.NewQuotaInodes, control.nowMS, 0)
		if err != nil {
			return err
		}
		current.Capacity = stored.Capacity
		currentRaw, err := json.Marshal(current)
		if err != nil || string(currentRaw) != raw {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS || !username.Valid || username.String != actor.username || created != actor.created || accountGeneration != actor.generation {
			return errAppPlatformPlanChanged
		}
		verified, err := platformVerifyStoredRelease(e, current.ReleaseSHA256, current.TrustRevision, trust, control.nowMS)
		if err != nil || verified.appID != current.AppID || verified.publisherID != current.PublisherID {
			return errAppPlatformPlanChanged
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`,
			control.nowMS, control.nowMS, planID, planSHA, control.nowMS)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		// The installation is only observed, not changed: the raise is online. The compare-and-set loses to any
		// operation that moved the installation since the review.
		changed, err = e.exec(`UPDATE app_platform_installations SET updated_ms=updated_ms WHERE id=? AND generation=? AND revision=? AND policy_revision=? AND controller_epoch=? AND runtime_id=?`,
			installation.String, current.Generation, revision, current.PolicyRevision, control.epoch, runtime.id)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		if err := insertPlatformOperation(e, platformOperationRow{
			ID: id, InstallationID: installation.String, ReviewPlanID: planID, ActorScope: actor.scope, IdempotencySHA: keyDigest,
			RequestSHA: requestDigest, Action: string(appplatform.ActionSetDataQuota), Managed: true,
			SourceGeneration: current.Generation, TargetGeneration: current.Generation + 1, Stage: "queued", StateRevision: 1,
			ControllerEpoch: control.epoch, NowMS: control.nowMS}); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan  string `json:"plan_id"`
			Bytes int64  `json:"quota_bytes"`
			Inode int64  `json:"quota_inodes"`
		}{planID, current.NewQuotaBytes, current.NewQuotaInodes})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: id, InstallationID: installation.String, Generation: current.Generation + 1, Revision: 1, Stage: appplatform.StageQueued}
		return nil
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}

// loadPlatformAdmittedDataQuota reads the consumed plan of a raise and proves it is this operation's.
func loadPlatformAdmittedDataQuota(e migExec, lease platformOperationLease) (platformDataQuotaPlan, error) {
	var zero platformDataQuotaPlan
	if lease.snapshot.Action != appplatform.ActionSetDataQuota || !lease.snapshot.Managed || lease.snapshot.Generation < 2 {
		return zero, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest string
	var source, target, managed int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.source_generation,o.target_generation,o.managed,p.consumed_ms
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='set_data_quota' AND p.action='set_data_quota'`+lock, lease.id, lease.installationID).
		Scan(&raw, &digest, &source, &target, &managed, &consumed); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var plan platformDataQuotaPlan
	canonical, marshalErr := json.Marshal(plan)
	if json.Unmarshal([]byte(raw), &plan) != nil || len(raw) > 65536 || platformDigest([]byte(raw)) != digest || managed != 1 || !consumed.Valid ||
		plan.Format != platformSetDataQuotaFormat || plan.Action != appplatform.ActionSetDataQuota || plan.InstallationID != lease.installationID ||
		plan.ControllerEpoch != lease.controllerEpoch || plan.Generation != source || target != source+1 || target != int64(lease.snapshot.Generation) ||
		plan.Capacity != "fits" || plan.NewQuotaBytes < plan.CurrentQuotaBytes || plan.NewQuotaInodes < plan.CurrentQuotaInodes ||
		!platformValidUUID(plan.DataNamespaceID) || !platformValidUUID(plan.DataEffectID) || !platformValidSHA(plan.DataReceiptSHA256) {
		return zero, errAppPlatformStageEvidence
	}
	canonical, marshalErr = json.Marshal(plan)
	if marshalErr != nil || string(canonical) != raw {
		return zero, errAppPlatformStageEvidence
	}
	return plan, nil
}

func (s *Server) claimNextPlatformSetDataQuota(ctx context.Context, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if !platformValidUUID(worker) {
		return zero, errAppPlatformLeaseConflict
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
	var epoch, paused string
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil || paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='set_data_quota' AND o.managed=1 AND o.completed_ms IS NULL
		AND o.stage IN ('queued','validate','prepare_data')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND ((i.desired_state='enabled' AND i.phase='ready') OR (i.desired_state='disabled' AND i.phase='disabled'))
		AND i.runtime_id IS NOT NULL AND i.generation=o.source_generation
		AND i.controller_epoch=? AND o.controller_epoch=? AND p.action=o.action AND p.controller_epoch=o.controller_epoch
		ORDER BY o.created_ms,o.id LIMIT 32`, now, epoch, epoch)
	if err != nil {
		return zero, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return zero, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	for _, id := range ids {
		facts, err := loadPlatformOperationLease(e, id)
		if err != nil {
			if errors.Is(err, errAppPlatformLeaseConflict) {
				continue
			}
			return zero, err
		}
		if facts.lease.leaseUntilMS > facts.now || facts.lease.snapshot.Action != appplatform.ActionSetDataQuota || !facts.lease.snapshot.Managed {
			continue
		}
		if _, err := loadPlatformAdmittedDataQuota(e, facts.lease); err != nil {
			return zero, err
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.data_quota_claimed','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

// advancePlatformSetDataQuotaStage moves a raise through its stages that carry no effect.
func (s *Server) advancePlatformSetDataQuotaStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	next := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageQueued: appplatform.StageValidate, appplatform.StageValidate: appplatform.StagePrepareData,
	}[expected.snapshot.Stage]
	if next == "" || expected.snapshot.Action != appplatform.ActionSetDataQuota || !expected.snapshot.Managed || expected.rowRevision == math.MaxInt64 {
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
	if _, err := loadPlatformAdmittedDataQuota(e, expected); err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`,
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

// platformSetDataQuotaIntent is the signed raise and its durable effect row.
type platformSetDataQuotaIntent struct {
	effectID                      string
	runtime                       platformRuntimeRegistration
	authority                     control.Authority
	command                       control.SetDataQuotaCommand
	plan                          platformDataQuotaPlan
	descriptor                    []byte
	raw, signature, readSignature []byte
}

func platformSetDataQuotaCommand(expected platformOperationLease, runtime platformRuntimeRegistration, plan platformDataQuotaPlan, effectID string) (control.SetDataQuotaCommand, error) {
	command := control.SetDataQuotaCommand{CommandHeader: control.CommandHeader{
		Format: control.SetDataQuotaFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10), Stage: control.SetDataQuotaStage,
		EffectNumber: control.SetDataQuotaEffectNumber, EffectID: effectID, Kind: control.SetDataQuotaKind,
		InstallationFence: strconv.FormatInt(expected.fence, 10),
	}, Payload: control.SetDataQuotaPayload{
		NamespaceID: plan.DataNamespaceID, ReleaseSHA256: plan.ReleaseSHA256, DataFormat: strconv.FormatInt(plan.DataFormat, 10),
		WriterFence: strconv.FormatInt(plan.WriterFence, 10), ProjectID: plan.ProjectID, NamespaceInode: plan.NamespaceInode, Device: plan.Device,
		DataEffectID: plan.DataEffectID, DataReceiptSHA256: plan.DataReceiptSHA256,
		OldBytes: strconv.FormatInt(plan.CurrentQuotaBytes, 10), OldInodes: strconv.FormatInt(plan.CurrentQuotaInodes, 10),
		NewBytes: strconv.FormatInt(plan.NewQuotaBytes, 10), NewInodes: strconv.FormatInt(plan.NewQuotaInodes, 10),
	}}
	digest, err := control.SetDataQuotaLogicalDigest(command)
	if err != nil {
		return control.SetDataQuotaCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

// preparePlatformSetDataQuota records the effect, once, and signs the raise. A retry reuses the same effect.
func (s *Server) preparePlatformSetDataQuota(ctx context.Context, expected platformOperationLease) (platformSetDataQuotaIntent, error) {
	var zero platformSetDataQuotaIntent
	if expected.snapshot.Action != appplatform.ActionSetDataQuota || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StagePrepareData {
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	plan, err := loadPlatformAdmittedDataQuota(e, expected)
	if err != nil {
		return zero, err
	}
	sourcePlan, _, source, err := platformBuildDataQuotaPlan(e, expected.installationID, plan.TargetRuntime, runtime, plan.NewQuotaBytes, plan.NewQuotaInodes, facts.now, 1)
	if err != nil {
		return zero, fmt.Errorf("data quota evidence: %w", err)
	}
	sourcePlan.Capacity = plan.Capacity
	if !samePlatformDataQuotaFacts(sourcePlan, plan) {
		return zero, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effectID, kind, digest, effectState, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,admitted_fence,observed_fence,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`+lock, expected.id).Scan(&effectID, &kind, &digest, &admitted, &observed, &effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	if fresh {
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, err
		}
	}
	command, err := platformSetDataQuotaCommand(expected, runtime, plan, effectID)
	if err != nil {
		return zero, err
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'prepare_data',1,?,'set-data-quota',?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now, facts.now); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID string `json:"effect_id"`
		}{effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.data_quota_intent','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	} else if kind != "set-data-quota" || digest != command.PayloadSHA256 || admitted != expected.fence || observed != 0 || effectState != "prepared" || receipt != "{}" {
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
	intent := platformSetDataQuotaIntent{effectID: effectID, runtime: runtime, authority: authority, command: command, plan: plan, descriptor: bytes.Clone(source.descriptor)}
	if intent.raw, intent.signature, err = control.SignSetDataQuota(command, key, authority, issued); err != nil {
		return zero, err
	}
	if intent.readSignature, err = control.SignSetDataQuotaRead(intent.raw, key, authority, issued); err != nil {
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

type platformSetDataQuotaObservation interface {
	SetDataQuotaReceipt() (journal.SetDataQuotaReceipt, error)
	DataEffectStatus() (journal.MigrateDataStatus, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformSetDataQuotaObservation(observation platformSetDataQuotaObservation, intent platformSetDataQuotaIntent, endpoint platformRuntimeEndpoint) (journal.SetDataQuotaReceipt, bool) {
	receipt, err := observation.SetDataQuotaReceipt()
	if err != nil || !journal.SetDataQuotaReceiptMatchesCommand(receipt, intent.command) ||
		!platformObservedBinding(observation.Controller(), observation.Endpoint(), observation.ReceivedAt(), observation.CommandSHA256(), intent.raw, intent.authority, endpoint) {
		return journal.SetDataQuotaReceipt{}, false
	}
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if observedErr != nil || settledErr != nil || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.SetDataQuotaReceipt{}, false
	}
	return receipt, true
}

// dispatchPlatformSetDataQuota sends the raise and commits its receipt. When the answer is a typed status (the
// runner is still working, or knows nothing of the effect) it is read back on the next attempt.
func (s *Server) dispatchPlatformSetDataQuota(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformSetDataQuota(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	observation, err := platformExecuteSetDataQuota(ctx, endpoint, intent)
	if err != nil {
		var refusal *journal.CapacityRefusalError
		if errors.As(err, &refusal) {
			return platformOperationLease{}, err
		}
		// The effect may have run: read it back, never send it again under another identity.
		var readErr error
		observation, readErr = platformReadSetDataQuota(ctx, endpoint, intent)
		if readErr != nil {
			return platformOperationLease{}, errors.Join(err, readErr)
		}
	}
	return s.commitPlatformSetDataQuota(ctx, expected, intent, endpoint, observation)
}

// commitPlatformSetDataQuota settles the effect, updates the namespace row and completes the operation in one
// fenced transaction: the row and the quota chain change together.
func (s *Server) commitPlatformSetDataQuota(ctx context.Context, expected platformOperationLease, intent platformSetDataQuotaIntent, endpoint platformRuntimeEndpoint, observation platformSetDataQuotaObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionSetDataQuota || expected.snapshot.Stage != appplatform.StagePrepareData || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	receipt, ok := validPlatformSetDataQuotaObservation(observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return zero, errAppPlatformStageEvidence
	}
	if parsed, parseErr := journal.ParseSetDataQuotaReceipt(encoded); parseErr != nil || parsed != receipt {
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	plan, err := loadPlatformAdmittedDataQuota(e, expected)
	if err != nil || plan.NewQuotaBytes != mustPlatformCheckpointInt(receipt.NewBytes) || plan.NewQuotaInodes != mustPlatformCheckpointInt(receipt.NewInodes) ||
		!samePlatformManagedUpdateRuntime(plan.TargetRuntime, intent.plan.TargetRuntime) || runtime.id != intent.runtime.id {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='prepare_data' AND effect_number=1 AND effect_id=? AND kind='set-data-quota' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, intent.effectID, intent.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	// The namespace row takes the new limits only if it still holds the ones the review saw.
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET quota_bytes=?,quota_inodes=?,observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active' AND quota_bytes=? AND quota_inodes=?`,
		plan.NewQuotaBytes, plan.NewQuotaInodes, mustPlatformCheckpointInt(receipt.UsedBytes), now, plan.DataNamespaceID,
		expected.installationID, expected.installationID, plan.CurrentQuotaBytes, plan.CurrentQuotaInodes)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	result.owner, result.leaseUntilMS = "", 0
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='prepare_data' AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		NewBytes   string `json:"new_quota_bytes"`
		NewInodes  string `json:"new_quota_inodes"`
		OldBytes   string `json:"old_quota_bytes"`
		OldInodes  string `json:"old_quota_inodes"`
		UsedBytes  string `json:"used_bytes"`
		UsedInodes string `json:"used_inodes"`
	}{receipt.NewBytes, receipt.NewInodes, receipt.OldBytes, receipt.OldInodes, receipt.UsedBytes, receipt.UsedInodes})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_quota_raised','app_platform_operation',?,?,'')`,
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

func (s *Server) runPlatformSetDataQuota(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	result := expected
	for result.snapshot.Stage != appplatform.StageComplete {
		var next platformOperationLease
		var err error
		switch result.snapshot.Stage {
		case appplatform.StageQueued, appplatform.StageValidate:
			next, err = s.advancePlatformSetDataQuotaStage(ctx, result)
		case appplatform.StagePrepareData:
			next, err = s.dispatchPlatformSetDataQuota(ctx, result, endpoint)
		default:
			return result, errAppPlatformStageEvidence
		}
		if err != nil {
			return result, err
		}
		result = next
	}
	return result, nil
}

// finishPlatformSetDataQuotaForReview ends a raise that could not complete. It changes nothing but the operation:
// the installation keeps running and the namespace keeps its quota. A refusal for capacity, or a failure before the
// effect, ends it failed; a raise whose effect is unknown ends recovery_required, and the runner's own evidence
// decides the limits (a limit above the journal evidence refuses a start until a raise is reviewed again).
func (s *Server) finishPlatformSetDataQuotaForReview(ctx context.Context, expected platformOperationLease, capacityCode string) error {
	if expected.snapshot.Action != appplatform.ActionSetDataQuota || !expected.snapshot.Managed {
		return errAppPlatformStageEvidence
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	outcome, code := appplatform.EffectsUnknown, "data_quota_recovery_required"
	if expected.snapshot.Stage != appplatform.StagePrepareData || capacityCode != "" {
		outcome, code = appplatform.EffectsNone, "data_quota_unavailable"
	}
	if capacityCode != "" {
		code = capacityCode
	}
	failed, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), code, now, now, expected.id, expected.installationID,
		expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence,
		expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_quota_failed','app_platform_operation',?,'{}','')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
