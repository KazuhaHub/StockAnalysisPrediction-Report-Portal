package app

import (
	"slices"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

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
			grant := name == "network.http" && policy != ""
			if required && !grant {
				return nil, errAppPlatformInstallUnavailable
			}
			restriction := "{}"
			if grant {
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
	return len(m.Capabilities.Required) == 0 || (len(m.Capabilities.Required) == 1 && m.Capabilities.Required[0] == "network.http")
}
