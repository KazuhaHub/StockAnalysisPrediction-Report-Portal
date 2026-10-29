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
		binding = platformLaunchAccess{installationID: installation, generation: "1", policyRevision: strconv.FormatInt(a.policy, 10), entry: m.Presentation.Entry, releaseSHA: parsed.SHA256, frontendSHA: m.Frontend.SHA256}
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
