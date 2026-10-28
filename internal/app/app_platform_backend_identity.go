package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Prepares only private registration material after the actual readiness stage.
// It writes no file/registration, delivers no key and grants no app access. The
// future root delivery must carry its own signed, current durable effect.
func (s *Server) preparePlatformBackendRegistration(ctx context.Context, expected platformOperationLease) (appplatform.BackendRegistration, error) {
	var zero appplatform.BackendRegistration
	if expected.snapshot.Action != appplatform.ActionInstall || !expected.snapshot.Managed || expected.snapshot.Generation != 1 || expected.snapshot.Stage != appplatform.StageActivate {
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
	runtime, controlSeed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	clear(controlSeed)
	if err != nil {
		return zero, err
	}
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
	candidate, _, err := loadPlatformCandidateIntentState(e, expected, runtime, facts.now, false, true)
	if err != nil {
		return zero, err
	}
	registration, err := platformBackendRegistrationMaterial(e, expected, runtime, candidate, dek)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			registration.Close()
		}
	}()
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if _, _, err := loadPlatformCandidateIntentState(e, expected, runtime, now, false, true); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return registration, nil
}

// Caller holds current operation/runtime/publisher authority in its transaction.
func platformBackendRegistrationMaterial(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, candidate platformCandidateIntent, dek []byte) (appplatform.BackendRegistration, error) {
	return platformBackendRegistrationForSource(e, platformManagedSource{operationID: expected.id, installationID: expected.installationID, epoch: expected.controllerEpoch, generation: expected.snapshot.Generation, fence: expected.fence}, runtime, candidate, dek)
}

func platformBackendRegistrationForSource(e migExec, source platformManagedSource, runtime platformRuntimeRegistration, candidate platformCandidateIntent, dek []byte) (appplatform.BackendRegistration, error) {
	var zero appplatform.BackendRegistration
	parsed, err := appplatform.ParseDescriptor(candidate.descriptor)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var sealed string
	if err := e.queryRow(`SELECT sealed_identity_seed FROM app_platform_installations WHERE id=?`, source.installationID).Scan(&sealed); err != nil {
		return zero, err
	}
	if !strings.HasPrefix(sealed, sealPrefix) {
		return zero, errAppPlatformStageEvidence
	}
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed, sealPrefix), aad("app-platform", source.installationID))
	defer clear(seed)
	if err != nil || len(seed) != 32 {
		return zero, errAppPlatformStageEvidence
	}
	registration, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, InstallationID: source.installationID, Generation: strconv.FormatUint(source.generation, 10), PolicyRevision: "1", ReleaseSHA256: candidate.command.Payload.ReleaseSHA256}, seed)
	if err != nil {
		return zero, err
	}

	return registration, nil
}
