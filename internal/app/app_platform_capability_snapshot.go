package app

import (
	"encoding/json"
	"slices"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The current development profile supports no platform capability providers.
// Its denied rows must be a complete, exact projection of the signed optional
// declaration and the operation's consumed review, not merely a set of zero bits.
// This read grants no authority and does not repair inconsistent records.
func verifyPlatformLaunchCapabilitySnapshot(e migExec, installation, release, epoch string, policy int64, m appplatform.Manifest) error {
	fail := errAppPlatformLaunchUnavailable
	if policy < 1 || len(m.Capabilities.Required) != 0 {
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
	expected := append([]string{}, m.Capabilities.Optional...)
	slices.Sort(expected)
	if !slices.Equal(expected, projection.DeclinedCapabilities) {
		return fail
	}
	return verifyPlatformDeniedCapabilityRows(e, installation, expected, policy, planID, fail)
}

// Call only after the caller has verified the signed declaration/review and
// current installation lifecycle. The worker and reader share this exact check.
func verifyPlatformDeniedCapabilityRows(e migExec, installation string, expected []string, policy int64, planID string, fail error) error {
	if policy < 1 || !platformValidUUID(planID) {
		return fail
	}
	rows, err := e.query(`SELECT capability,granted,policy_json,policy_revision,review_plan_id FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, installation)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var capability, policyJSON, review string
		var granted int
		var revision int64
		if err := rows.Scan(&capability, &granted, &policyJSON, &revision, &review); err != nil {
			return err
		}
		if index >= len(expected) || capability != expected[index] || granted != 0 || policyJSON != "{}" || revision != policy || review != planID {
			return fail
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(expected) {
		return fail
	}
	return nil
}
