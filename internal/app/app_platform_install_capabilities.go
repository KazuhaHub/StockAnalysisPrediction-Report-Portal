package app

import (
	"encoding/json"
	"slices"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func activatePlatformInstallCapabilities(e migExec, installation, operation string, now int64) error {
	var rawPlan, rawDescriptor, planID string
	if err := e.queryRow(`SELECT p.plan_json,r.descriptor,p.id FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=? AND o.installation_id=?`, operation, installation).Scan(&rawPlan, &rawDescriptor, &planID); err != nil {
		return err
	}
	var projection platformInstallProjection
	if json.Unmarshal([]byte(rawPlan), &projection) != nil {
		return errAppPlatformInstallUnavailable
	}
	parsed, err := appplatform.ParseDescriptor([]byte(rawDescriptor))
	if err != nil {
		return errAppPlatformInstallUnavailable
	}
	selection, err := platformInstallCapabilitySelection(parsed.Manifest, projection.Runtime)
	if err != nil {
		return err
	}
	for _, capability := range selection {
		if !capability.granted {
			continue
		}
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=1,updated_ms=? WHERE installation_id=? AND capability=? AND granted=0 AND policy_json=? AND policy_revision=1 AND review_plan_id=?`, now, installation, capability.capability, capability.policy, planID)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	return nil
}

type platformSelectedCapability struct {
	capability string
	granted    bool
	policy     string
}

func platformNetworkTargetPolicy(target *platformManagedInstallTarget) (string, error) {
	if target == nil {
		return "", nil
	}
	if target.NetworkPolicy == "" {
		if target.NetworkProfile != (transport.NetworkRuntimeProfile{}) {
			return "", errAppPlatformInstallUnavailable
		}
		return "", nil
	}
	if _, err := appplatform.ParseHTTPPolicy([]byte(target.NetworkPolicy)); err != nil || transport.ValidateNetworkRuntimeProfile(target.NetworkProfile) != nil || !target.NetworkProfile.WritableBackend {
		return "", errAppPlatformInstallUnavailable
	}
	return target.NetworkPolicy, nil
}

// Derive the entire exact capability set from a parsed signed declaration and
// explicit host-reviewed restrictions. Optional capabilities default off. This
// function does not prove that target metadata came from a live OS observation.
func platformInstallCapabilitySelection(m appplatform.Manifest, target *platformManagedInstallTarget) ([]platformSelectedCapability, error) {
	policy, err := platformNetworkTargetPolicy(target)
	if err != nil {
		return nil, err
	}
	if len(m.Capabilities.Required)+len(m.Capabilities.Optional) > 32 {
		return nil, errAppPlatformInstallUnavailable
	}
	var result []platformSelectedCapability
	seen := map[string]bool{}
	for _, required := range []bool{true, false} {
		names := m.Capabilities.Optional
		if required {
			names = m.Capabilities.Required
		}
		for _, name := range names {
			if name == "" || len(name) > 96 || seen[name] {
				return nil, errAppPlatformInstallUnavailable
			}
			seen[name] = true
			grant := (name == "network.http" && policy != "") || (required && (name == "reports.read" || name == "storage.files" || name == "secrets.use" || name == "background.run" || name == "notifications.publish"))
			if required && !grant {
				return nil, errAppPlatformInstallUnavailable
			}
			restriction := "{}"
			if grant && name == "network.http" {
				restriction = policy
			}
			result = append(result, platformSelectedCapability{capability: name, granted: grant, policy: restriction})
		}
	}
	if policy != "" && !seen["network.http"] {
		return nil, errAppPlatformInstallUnavailable
	}
	slices.SortFunc(result, func(a, b platformSelectedCapability) int {
		if a.capability < b.capability {
			return -1
		}
		if a.capability > b.capability {
			return 1
		}
		return 0
	})
	return result, nil
}
func platformDeclinedCapabilities(selection []platformSelectedCapability) []string {
	result := []string{}
	for _, c := range selection {
		if !c.granted {
			result = append(result, c.capability)
		}
	}
	return result
}

func verifyPlatformInstallCapabilityRows(e migExec, installation string, m appplatform.Manifest, p platformInstallProjection, active bool, policy int64, planID string, fail error) error {
	expected, err := platformInstallCapabilitySelection(m, p.Runtime)
	if err != nil || policy < 1 || !platformValidUUID(planID) {
		return fail
	}
	if !slices.Equal(p.DeclinedCapabilities, platformDeclinedCapabilities(expected)) {
		return fail
	}
	return verifyPlatformSelectedCapabilityRows(e, installation, expected, active, policy, planID, fail)
}
func verifyPlatformSelectedCapabilityRows(e migExec, installation string, expected []platformSelectedCapability, active bool, policy int64, planID string, fail error) error {
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
		var name, restriction, review string
		var granted int
		var revision int64
		if err := rows.Scan(&name, &granted, &restriction, &revision, &review); err != nil {
			return err
		}
		if index >= len(expected) || expected[index].capability != name || granted != boolInt(active && expected[index].granted) || restriction != expected[index].policy || revision != policy || review != planID {
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

func platformSupportedRequiredCapabilities(m appplatform.Manifest) bool {
	seen := make(map[string]bool, len(m.Capabilities.Required))
	for _, capability := range m.Capabilities.Required {
		if seen[capability] || (capability != "network.http" && capability != "reports.read" && capability != "storage.files" && capability != "secrets.use" && capability != "background.run" && capability != "notifications.publish") {
			return false
		}
		seen[capability] = true
	}
	return len(seen) <= 3
}
