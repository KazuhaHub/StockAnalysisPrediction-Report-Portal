package app

import (
	"database/sql"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The failure transition grants no execution authority. It still pins the exact
// consumed writable review and installation runtime; a revoked runtime need not
// be usable before its pending work can be denied and retained for recovery.
func platformInstallReviewRuntime(e migExec, lease platformOperationLease) (string, error) {
	var actual sql.NullString
	if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&actual); err != nil {
		return "", err
	}
	if !lease.snapshot.Managed {
		if actual.Valid {
			return "", errAppPlatformStageEvidence
		}
		return "", nil
	}
	var raw, digest, planID, request string
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.id,o.request_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=? AND o.installation_id=? AND p.installation_id IS NULL AND p.controller_epoch=? AND p.action='install' AND p.consumed_ms IS NOT NULL`, lease.id, lease.installationID, lease.controllerEpoch).Scan(&raw, &digest, &planID, &request); err != nil {
		return "", err
	}
	var p platformInstallProjection
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &p) != nil {
		return "", errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(p)
	requestBytes, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	if err != nil || string(canonical) != raw || platformDigest(canonical) != digest || platformDigest(requestBytes) != request || p.Format != platformWritableInstallFormat || p.ControllerEpoch != lease.controllerEpoch || p.Runtime == nil || !platformValidUUID(p.Runtime.RuntimeID) || !actual.Valid || actual.String != p.Runtime.RuntimeID || p.Runtime.ControllerEpoch != lease.controllerEpoch || p.Runtime.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile {
		return "", errAppPlatformStageEvidence
	}
	return actual.String, nil
}
