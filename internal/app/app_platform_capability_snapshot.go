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
	var planID, raw, digest, action string
	if err := e.queryRow(`SELECT p.id,p.plan_json,p.plan_sha256,o.action FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.installation_id=? AND ((o.action='install') OR (o.action='update' AND o.target_generation=i.generation)) AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.controller_epoch=? AND p.target_release_sha256=? AND p.controller_epoch=o.controller_epoch AND p.consumed_ms IS NOT NULL
		ORDER BY o.completed_ms DESC,o.id DESC LIMIT 1`, installation, epoch, release).Scan(&planID, &raw, &digest, &action); err != nil {
		return fail
	}
	if len(raw) > 65536 || platformDigest([]byte(raw)) != digest {
		return fail
	}
	var projection platformInstallProjection
	switch action {
	case "install":
		if json.Unmarshal([]byte(raw), &projection) != nil {
			return fail
		}
		canonical, err := json.Marshal(projection)
		if err != nil || string(canonical) != raw {
			return fail
		}
	case "update":
		var update platformUpdateProjection
		if json.Unmarshal([]byte(raw), &update) != nil || update.Format != platformUpdateReviewFormat {
			return fail
		}
		canonical, err := json.Marshal(update)
		if err != nil || string(canonical) != raw {
			return fail
		}
		projection = update.Target
	default:
		return fail
	}
	if projection.AppID != m.ID || projection.PublisherID != m.Publisher || projection.ReleaseSHA256 != release || projection.ControllerEpoch != epoch || projection.PlatformAPI != platformDevelopmentAPI {
		return fail
	}
	if installRequirementsForTarget(m, projection.Format, projection.Runtime) != nil {
		return fail
	}
	return verifyPlatformInstallCapabilityRows(e, installation, m, projection, true, policy, planID, fail)
}
