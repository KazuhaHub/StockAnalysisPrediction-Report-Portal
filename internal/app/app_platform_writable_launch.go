package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Frontend serving is an authenticated local read, not a live backend health
// assertion or an execution permit. The full completed writable source and current
// viewer/runtime/trust transaction must succeed before any bytes are released.
func (s *Server) withPlatformWritableLaunchReadContext(ctx context.Context, session platformActorSession, installation string, read func(migExec, platformLaunchAccess, appplatform.Manifest, platformActorAccount) error) (platformLaunchAccess, error) {
	var binding platformLaunchAccess
	access, err := withPlatformBackendRead(s, ctx, session, installation, s.loadPlatformWritableBackendHistory, func(e migExec, a platformWritableBackendAccess, _ []byte) error {
		parsed, err := appplatform.ParseDescriptor(a.intent.start.writer.descriptor)
		if err != nil || parsed.Manifest.Backend == nil {
			return errAppPlatformLaunchUnavailable
		}
		m := parsed.Manifest
		binding = platformLaunchAccess{installationID: installation, generation: strconv.FormatInt(a.intent.generation, 10), policyRevision: strconv.FormatInt(a.policy, 10), entry: m.Presentation.Entry, releaseSHA: parsed.SHA256, frontendSHA: m.Frontend.SHA256}
		if read != nil {
			return read(e, binding, m, a.account)
		}
		return nil
	})
	if err != nil {
		return platformLaunchAccess{}, err
	}
	access.Close()
	return binding, nil
}

// Directory projection only. It does not decrypt keys, inspect ZIPs, attest
// health or grant launch authority. The actual launch/document transaction always
// reconstructs the complete typed source and repeats current viewer checks.
func platformWritableLaunchEligible(e migExec, installation, runtime, epoch string) (bool, error) {
	var generation int64
	if err := e.queryRow(`SELECT generation
		FROM app_platform_installations
		WHERE id=? AND controller_epoch=? AND runtime_id=?`,
		installation, epoch, runtime).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if generation > 1 {
		eligible, err := platformWritableUpdateLaunchEligible(
			e, installation, runtime, epoch, generation)
		if err != nil || eligible {
			return eligible, err
		}
		return platformWritableRollbackToInstallEligible(e, installation, runtime, epoch, generation)
	}
	var raw, digest, planID, request string
	err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.id,o.request_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.installation_id=? AND o.action='install' AND o.managed=1 AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.error_code IS NULL AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.fence=i.control_fence AND o.controller_epoch=? AND i.generation=1 AND p.controller_epoch=? AND p.action='install' AND p.installation_id IS NULL AND p.consumed_ms IS NOT NULL`, installation, epoch, epoch).Scan(&raw, &digest, &planID, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p platformInstallProjection
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &p) != nil {
		return false, nil
	}
	canonical, _ := json.Marshal(p)
	requestBytes, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	if string(canonical) != raw || platformDigest(canonical) != digest || platformDigest(requestBytes) != request || p.Format != platformWritableInstallFormat || p.ControllerEpoch != epoch || p.Runtime == nil || p.Runtime.RuntimeID != runtime || p.Runtime.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile || p.PlatformAPI != platformDevelopmentAPI {
		return false, nil
	}
	if err := validateManagedInstallTarget(e, p.Runtime, epoch); err != nil {
		if errors.Is(err, errAppPlatformRuntimeConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func platformWritableUpdateLaunchEligible(e migExec, installation, runtime, epoch string, generation int64) (bool, error) {
	var raw, digest, actionText string
	var targetGeneration int64
	var descriptor []byte
	err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,
		o.target_generation,r.descriptor,o.action
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.installation_id=?
		AND o.action IN ('update','enable','repair','import_data') AND o.managed=1
		AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND o.error_code IS NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.fence=i.control_fence AND o.controller_epoch=?
		AND i.generation=? AND i.controller_epoch=?
		AND i.runtime_id=? AND p.controller_epoch=?
		AND p.action=o.action AND p.consumed_ms IS NOT NULL`,
		installation, generation, epoch, generation, epoch,
		runtime, epoch).Scan(&raw, &digest, &targetGeneration, &descriptor, &actionText)
	planGeneration := generation
	if errors.Is(err, sql.ErrNoRows) {
		// An ordinary roll back that landed on this generation restarted the release of the operation that completed
		// the generation it restored: that operation's reviewed target is what runs, under the roll back's fence.
		err = e.queryRow(`SELECT p.plan_json,p.plan_sha256,
			o.target_generation,rel.descriptor,o.action
			FROM app_platform_operations r
			JOIN app_platform_review_plans rp ON rp.id=r.review_plan_id
			JOIN app_platform_installations i ON i.id=r.installation_id
			JOIN app_platform_operations o ON o.installation_id=r.installation_id AND o.target_generation=r.source_generation
			JOIN app_platform_review_plans p ON p.id=o.review_plan_id
			JOIN app_platform_releases rel ON rel.sha256=p.target_release_sha256
			WHERE r.installation_id=? AND r.action='restore' AND r.managed=1
			AND r.target_generation=? AND r.stage='complete'
			AND r.completed_ms IS NOT NULL AND r.error_code IS NULL
			AND r.lease_owner IS NULL AND r.lease_until_ms IS NULL
			AND r.fence=i.control_fence AND r.controller_epoch=?
			AND rp.action='restore' AND rp.plan_json LIKE ? AND rp.consumed_ms IS NOT NULL
			AND EXISTS (SELECT 1 FROM app_platform_operation_effects ra WHERE ra.operation_id=r.id AND ra.stage='activate' AND ra.state='applied')
			AND i.generation=? AND i.controller_epoch=? AND i.runtime_id=?
			AND o.action IN ('update','enable','repair','import_data') AND o.managed=1
			AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.error_code IS NULL
			AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND rp.controller_epoch=r.controller_epoch
			AND o.controller_epoch=? AND p.controller_epoch=? AND p.action=o.action AND p.consumed_ms IS NOT NULL
			ORDER BY r.completed_ms DESC,r.id DESC,o.completed_ms DESC,o.id DESC LIMIT 1`,
			installation, generation, epoch, platformOrdinaryRestorePlanPrefix(),
			generation, epoch, runtime, epoch, epoch).Scan(&raw, &digest, &targetGeneration, &descriptor, &actionText)
		planGeneration = targetGeneration
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var plan platformManagedUpdatePlan
	// The same operation actions and plan formats the update history loader accepts as a source.
	action := appplatform.Action(actionText)
	validFormat := false
	var planProbe platformManagedUpdatePlan
	if len(raw) <= 65536 && json.Unmarshal([]byte(raw), &planProbe) == nil {
		validFormat = action == appplatform.ActionUpdate &&
			planProbe.Format == platformManagedUpdateFormat ||
			platformSameReleaseLifecycle(action) &&
				planProbe.Format == platformSameReleaseLifecycleFormat(action)
	}
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &plan) != nil ||
		targetGeneration != planGeneration || planGeneration < 1 ||
		!validFormat ||
		plan.InstallationID != installation ||
		plan.ControllerEpoch != epoch ||
		plan.TargetGeneration != planGeneration ||
		plan.TargetRuntime == nil ||
		plan.TargetRuntime.RuntimeID != runtime ||
		plan.TargetRuntime.Profile.WritableSandboxProfile !=
			control.WritableCandidateSandboxProfile {
		return false, nil
	}
	canonical, _ := json.Marshal(plan)
	if string(canonical) != raw || platformDigest(canonical) != digest {
		return false, nil
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil ||
		parsed.SHA256 != plan.TargetReleaseSHA256 ||
		parsed.Manifest.ID != plan.AppID ||
		parsed.Manifest.Publisher != plan.PublisherID {
		return false, nil
	}
	if err := validateManagedInstallTarget(
		e, plan.TargetRuntime, epoch); err != nil {
		if errors.Is(err, errAppPlatformRuntimeConflict) {
			return false, nil
		}
		return false, err
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'
		AND generation=? AND release_sha256=?
		AND policy_revision>=?`,
		installation, generation, plan.TargetReleaseSHA256,
		plan.TargetPolicyRevision).Scan(&active); err != nil {
		return false, err
	}
	return active == 1, nil
}

// platformWritableRollbackToInstallEligible admits a generation an ordinary roll back landed on when the generation it
// restored was the install's: the install's reviewed target runs again, under the roll back's fence, as the active
// activation of this generation.
func platformWritableRollbackToInstallEligible(e migExec, installation, runtime, epoch string, generation int64) (bool, error) {
	var raw, digest, planID, request string
	err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.id,o.request_sha256
		FROM app_platform_operations r
		JOIN app_platform_review_plans rp ON rp.id=r.review_plan_id
		JOIN app_platform_installations i ON i.id=r.installation_id
		JOIN app_platform_operations o ON o.installation_id=r.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE r.installation_id=? AND r.action='restore' AND r.managed=1
		AND r.source_generation=1 AND r.target_generation=? AND r.stage='complete'
		AND r.completed_ms IS NOT NULL AND r.error_code IS NULL
		AND r.lease_owner IS NULL AND r.lease_until_ms IS NULL
		AND r.fence=i.control_fence AND r.controller_epoch=?
		AND rp.action='restore' AND rp.plan_json LIKE ? AND rp.consumed_ms IS NOT NULL
		AND EXISTS (SELECT 1 FROM app_platform_operation_effects ra WHERE ra.operation_id=r.id AND ra.stage='activate' AND ra.state='applied')
		AND i.generation=? AND i.controller_epoch=? AND i.runtime_id=?
		AND o.action='install' AND o.managed=1 AND o.source_generation=0 AND o.target_generation=1
		AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.error_code IS NULL AND o.controller_epoch=?
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND rp.controller_epoch=r.controller_epoch
		AND p.controller_epoch=? AND p.action='install' AND p.installation_id IS NULL AND p.consumed_ms IS NOT NULL
		ORDER BY r.completed_ms DESC,r.id DESC LIMIT 1`,
		installation, generation, epoch, platformOrdinaryRestorePlanPrefix(), generation, epoch, runtime, epoch, epoch).
		Scan(&raw, &digest, &planID, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p platformInstallProjection
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &p) != nil {
		return false, nil
	}
	canonical, _ := json.Marshal(p)
	requestBytes, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	if string(canonical) != raw || platformDigest(canonical) != digest || platformDigest(requestBytes) != request || p.Format != platformWritableInstallFormat || p.ControllerEpoch != epoch || p.Runtime == nil || p.Runtime.RuntimeID != runtime || p.Runtime.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile || p.PlatformAPI != platformDevelopmentAPI {
		return false, nil
	}
	if err := validateManagedInstallTarget(e, p.Runtime, epoch); err != nil {
		if errors.Is(err, errAppPlatformRuntimeConflict) {
			return false, nil
		}
		return false, err
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations
		WHERE installation_id=? AND state='active' AND generation=? AND release_sha256=?`,
		installation, generation, p.ReleaseSHA256).Scan(&active); err != nil {
		return false, err
	}
	return active == 1, nil
}
