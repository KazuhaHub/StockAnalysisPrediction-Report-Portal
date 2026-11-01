package app

import (
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
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
		WHERE o.installation_id=? AND ((o.action='install') OR ((o.action='update' OR o.action='enable') AND o.target_generation=i.generation)) AND o.stage='complete' AND o.completed_ms IS NOT NULL AND o.controller_epoch=? AND p.target_release_sha256=? AND p.controller_epoch=o.controller_epoch AND p.consumed_ms IS NOT NULL
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
		var formatProbe struct {
			Format string `json:"format"`
		}
		if json.Unmarshal([]byte(raw), &formatProbe) != nil {
			return fail
		}
		switch formatProbe.Format {
		case platformManagedUpdateFormat:
			var update platformManagedUpdatePlan
			if json.Unmarshal([]byte(raw), &update) != nil {
				return fail
			}
			canonical, err := json.Marshal(update)
			if err != nil || string(canonical) != raw ||
				update.TargetRuntime == nil ||
				update.AppID != m.ID || update.PublisherID != m.Publisher ||
				update.TargetReleaseSHA256 != release ||
				update.ControllerEpoch != epoch ||
				installRequirementsForTarget(
					m, platformWritableInstallFormat,
					update.TargetRuntime) != nil {
				return fail
			}
			expected, err := platformInstallCapabilitySelection(
				m, update.TargetRuntime)
			if err != nil {
				return fail
			}
			return verifyPlatformSelectedCapabilityRows(
				e, installation, expected, true, policy, planID, fail)
		case platformUpdateReviewFormat:
			var update platformUpdateProjection
			if json.Unmarshal([]byte(raw), &update) != nil {
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
	case "enable":
		var formatProbe struct {
			Format string `json:"format"`
		}
		if json.Unmarshal([]byte(raw), &formatProbe) != nil {
			return fail
		}
		if formatProbe.Format == platformFrontendEnableFormat {
			var enable platformFrontendEnablePlan
			if json.Unmarshal([]byte(raw), &enable) != nil {
				return fail
			}
			canonical, err := json.Marshal(enable)
			if err != nil || string(canonical) != raw ||
				enable.Action != appplatform.ActionEnable ||
				enable.AppID != m.ID ||
				enable.PublisherID != m.Publisher ||
				enable.SourceReleaseSHA256 != release ||
				enable.ControllerEpoch != epoch ||
				enable.DeclaredDataSchema != int64(m.Data.Schema) ||
				m.Backend != nil || m.Data.WritesOnStartup {
				return fail
			}
			compatibility, err := appplatform.EvaluateCompatibility(
				m,
				appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI})
			if err != nil || !compatibility.Compatible {
				return fail
			}
			expected, err := platformInstallCapabilitySelection(m, nil)
			if err != nil {
				return fail
			}
			return verifyPlatformRetainedCapabilityRows(
				e, installation, expected, policy, fail)
		}
		if formatProbe.Format != platformManagedEnableFormat {
			return fail
		}
		var enable platformManagedUpdatePlan
		if json.Unmarshal([]byte(raw), &enable) != nil {
			return fail
		}
		if enable.TargetRuntime == nil {
			return fail
		}
		format := platformManagedInstallFormat
		if enable.TargetRuntime.Profile.WritableSandboxProfile ==
			control.WritableCandidateSandboxProfile {
			format = platformWritableInstallFormat
		}
		canonical, err := json.Marshal(enable)
		if err != nil || string(canonical) != raw ||
			enable.Format != platformManagedEnableFormat ||
			enable.Action != appplatform.ActionEnable ||
			enable.TargetRuntime == nil ||
			enable.AppID != m.ID || enable.PublisherID != m.Publisher ||
			enable.TargetReleaseSHA256 != release ||
			enable.ControllerEpoch != epoch ||
			installRequirementsForTarget(
				m, format,
				enable.TargetRuntime) != nil {
			return fail
		}
		expected, err := platformInstallCapabilitySelection(
			m, enable.TargetRuntime)
		if err != nil {
			return fail
		}
		return verifyPlatformRetainedCapabilityRows(
			e, installation, expected, policy, fail)
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
