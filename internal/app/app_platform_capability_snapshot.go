package app

import (
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Capability rows must exactly match the signed declaration and consumed review,
// including explicit network restrictions. A grant bit alone proves no scope.
// This read grants no authority and does not repair inconsistent records.
func verifyPlatformLaunchCapabilitySnapshot(e migExec, installation, release, epoch string, policy int64, m appplatform.Manifest) error {
	fail := errAppPlatformLaunchUnavailable
	if policy < 1 {
		return fail
	}
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.action='install' AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.controller_epoch=? AND p.action='install' AND p.target_release_sha256=? AND p.controller_epoch=o.controller_epoch AND p.consumed_ms IS NOT NULL`, installation, epoch, release).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fail
	}
	var planID, raw, digest string
	if err := e.queryRow(`SELECT p.id,p.plan_json,p.plan_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.action='install' AND o.source_generation=0 AND o.target_generation=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.controller_epoch=? AND p.action='install' AND p.target_release_sha256=? AND p.controller_epoch=o.controller_epoch AND p.consumed_ms IS NOT NULL`, installation, epoch, release).Scan(&planID, &raw, &digest); err != nil {
		return fail
	}
	if len(raw) > 65536 || platformDigest([]byte(raw)) != digest {
		return fail
	}
	var projection platformInstallProjection
	if json.Unmarshal([]byte(raw), &projection) != nil {
		return fail
	}
	canonical, err := json.Marshal(projection)
	if err != nil || string(canonical) != raw || projection.AppID != m.ID || projection.PublisherID != m.Publisher || projection.ReleaseSHA256 != release || projection.ControllerEpoch != epoch || projection.PlatformAPI != platformDevelopmentAPI {
		return fail
	}
	if installRequirementsForTarget(m, projection.Format, projection.Runtime) != nil {
		return fail
	}
	return verifyPlatformInstallCapabilityRows(e, installation, m, projection, true, policy, planID, fail)
}
