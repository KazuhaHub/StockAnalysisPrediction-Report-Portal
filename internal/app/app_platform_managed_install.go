package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

const platformManagedInstallFormat = "portal.managed-install-review/1-dev.1"
const platformWritableInstallFormat = "portal.writable-install-review/1-dev.1"

func platformManagedFormat(format string) bool {
	return format == platformManagedInstallFormat || format == platformWritableInstallFormat
}

type platformManagedInstallTarget struct {
	RuntimeID       string                          `json:"runtime_id"`
	DeploymentID    string                          `json:"deployment_id"`
	ControllerEpoch string                          `json:"controller_epoch"`
	BindingRevision int64                           `json:"binding_revision"`
	ControlRevision int64                           `json:"control_revision"`
	Fingerprint     string                          `json:"fingerprint"`
	Profile         transport.ManagedRuntimeProfile `json:"profile"`
}

func managedInstallTarget(f platformManagedRuntimeFacts, e platformRuntimeEndpoint) (*platformManagedInstallTarget, error) {
	if !validPlatformManagedRuntimeObservation(f.observation, f.runtime, e) {
		return nil, errAppPlatformRuntimeConflict
	}
	r := f.runtime
	return &platformManagedInstallTarget{RuntimeID: r.id, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, BindingRevision: r.revision, ControlRevision: r.controlRevision, Fingerprint: r.fingerprint, Profile: f.observation.Profile()}, nil
}
func validateManagedInstallTarget(e migExec, p *platformManagedInstallTarget, epoch string) error {
	if p == nil || !platformValidUUID(p.RuntimeID) || !platformValidUUID(p.DeploymentID) || p.ControllerEpoch != epoch || !platformValidSHA(p.Fingerprint) || p.BindingRevision != 2 || p.ControlRevision < 1 || p.Profile.ExecutionReady || (p.Profile.WritableSandboxProfile != "" && p.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile) || p.Profile.SandboxProfile != control.CandidateSandboxProfile || (p.Profile.Platform != "linux/amd64" && p.Profile.Platform != "linux/arm64") || p.Profile.Protocol != transport.ManagedRunnerProtocol || p.Profile.BackendProtocol != transport.ManagedBackendProtocol || !platformValidUUID(p.Profile.RootFilesystemUUID) {
		return errAppPlatformRuntimeConflict
	}
	inode, err := strconv.ParseUint(p.Profile.RootInode, 10, 64)
	if err != nil || inode == 0 || strconv.FormatUint(inode, 10) != p.Profile.RootInode {
		return errAppPlatformRuntimeConflict
	}
	var count int
	err = e.queryRow(`SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE id=? AND deployment_id=? AND controller_epoch=? AND control_revision=? AND revision=? AND enabled=1 AND verification_key_fingerprint=? AND sealed_control_key IS NOT NULL`, p.RuntimeID, p.DeploymentID, p.ControllerEpoch, p.ControlRevision, p.BindingRevision, p.Fingerprint).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return errAppPlatformRuntimeConflict
	}
	return nil
}
func installRequirementsForTarget(m appplatform.Manifest, format string, p *platformManagedInstallTarget) error {
	if !platformManagedFormat(format) {
		if p != nil {
			return errAppPlatformPlanChanged
		}
		return platformInstallRequirements(m)
	}
	if p == nil || m.Backend == nil || len(m.Capabilities.Required) != 0 || (format == platformWritableInstallFormat && p.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile) {
		return errAppPlatformInstallUnavailable
	}
	target := appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI, Runner: &appplatform.RunnerCompatibility{Protocol: p.Profile.Protocol, Platform: p.Profile.Platform, BackendProtocol: p.Profile.BackendProtocol}}
	compatible, err := appplatform.EvaluateCompatibility(m, target)
	if err != nil || !compatible.Compatible {
		return errAppPlatformInstallUnavailable
	}
	return nil
}

// Explicit development entry points; generic frontend HTTP admission stays static.
// A managed install remains pending through data/start/probe, with no access grant.
func (s *Server) createPlatformManagedInstallReview(ctx context.Context, session platformActorSession, release string, revision int64, targets []platformAudienceTarget, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformInstallReviewWithRuntime(ctx, session, release, revision, targets, platformManagedInstallFormat, &facts, endpoint)
}
func (s *Server) consumePlatformManagedInstallReview(ctx context.Context, session platformActorSession, plan, sha, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformInstallReviewWithRuntime(ctx, session, plan, sha, key, platformManagedInstallFormat, &facts, endpoint)
}
func platformManagedTargetForOperation(e migExec, operation string) (*platformManagedInstallTarget, error) {
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, operation).Scan(&raw); err != nil {
		return nil, err
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(raw), &p) != nil || !platformManagedFormat(p.Format) || p.Runtime == nil {
		return nil, errAppPlatformStageEvidence
	}
	if err := validateManagedInstallTarget(e, p.Runtime, p.ControllerEpoch); err != nil {
		return nil, err
	}
	return p.Runtime, nil
}
func (s *Server) checkPlatformManagedRuntimeRoot(ctx context.Context, lease platformOperationLease, runtime platformRuntimeRegistration, authority control.Authority, endpoint platformRuntimeEndpoint) error {
	return s.checkPlatformManagedOperationRoot(ctx, lease.id, runtime, authority, endpoint)
}

func (s *Server) checkPlatformManagedOperationRoot(ctx context.Context, operation string, runtime platformRuntimeRegistration, authority control.Authority, endpoint platformRuntimeEndpoint) error {
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	target, err := platformManagedTargetForOperation(pool, operation)
	if err != nil {
		return err
	}
	observed, err := platformReadManagedRuntime(ctx, endpoint, authority)
	if err != nil {
		return err
	}
	actual, err := managedInstallTarget(platformManagedRuntimeFacts{runtime: runtime, observation: observed}, endpoint)
	if err != nil {
		return err
	}
	if *actual != *target {
		return errAppPlatformRuntimeConflict
	}
	return nil
}
func managedInstallRuntimeID(p platformInstallProjection) sql.NullString {
	if p.Runtime != nil {
		return sql.NullString{String: p.Runtime.RuntimeID, Valid: true}
	}
	return sql.NullString{}
}

func platformManagedInstallFlag(p platformInstallProjection) int {
	if platformManagedFormat(p.Format) {
		return 1
	}
	return 0
}
