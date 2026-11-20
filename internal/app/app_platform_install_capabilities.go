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

// platformGrantMatches compares a stored grant bit with the reviewed selection. principals.read is
// optional and defaults off at install; an administrator may switch it on or off afterwards
// (doc 181 section 4), so either bit is a valid state for it when the review left it off. The same
// holds for public.projection and network.postgres (doc 178), each granted by its own review.
func platformGrantMatches(capability string, stored int, reviewed bool) bool {
	if stored == boolInt(reviewed) {
		return true
	}
	return (capability == "principals.read" || capability == "public.projection" || capability == platformCapabilityNetworkPostgres || capability == "ingress.webhook") && !reviewed && stored == 1
}

// platformPolicyMatches compares a stored policy with the reviewed one. public.projection is the
// one capability whose policy an administrator chooses after install (doc 179 amendment C2): a
// granted row may carry a validated public policy in place of the install-time "{}". An ungranted
// row is always exactly the reviewed policy, so withdrawing resets it.
//
// network.postgres (doc 178) is the second: its granted row carries the validated probe policy an
// administrator's own review wrote (platformPostgresPolicyReview), and withdrawing resets it to
// "{}" with granted 0.
func platformPolicyMatches(capability string, granted int, stored, reviewed string) bool {
	return stored == reviewed ||
		(capability == "public.projection" && granted == 1 && reviewed == "{}" && appplatform.ValidPublicPolicyShape(stored)) ||
		(capability == platformCapabilityNetworkPostgres && granted == 1 && reviewed == "{}" && appplatform.ValidPostgresPolicy(stored))
}

// platformNetworkRowAccepted is the row test of one capability: the reviewed selection, or, for a
// granted network.http row the selection also grants, a policy and review a valid latest consumed
// capability plan backs (doc 175 section 2.5). overlay is nil where only the selection counts.
func platformNetworkRowAccepted(name string, granted int, stored, review string, expected platformSelectedCapability, planID string, overlay platformNetworkRowOverlay) bool {
	if platformPolicyMatches(name, granted, stored, expected.policy) && (planID == "" || review == planID) {
		return true
	}
	return overlay != nil && name == platformCapabilityNetworkHTTP && granted == 1 && expected.granted && overlay(review, stored)
}

type platformSelectedCapability struct {
	capability string
	granted    bool
	policy     string
}

func platformNetworkTargetPolicies(target *platformManagedInstallTarget) (string, string, error) {
	if target == nil {
		return "", "", nil
	}
	if target.NetworkPolicy == "" && target.NetworkStreamPolicy == "" {
		if target.NetworkProfile != (transport.NetworkRuntimeProfile{}) {
			return "", "", errAppPlatformInstallUnavailable
		}
		return "", "", nil
	}
	if transport.ValidateNetworkRuntimeProfile(target.NetworkProfile) != nil || !target.NetworkProfile.WritableBackend {
		return "", "", errAppPlatformInstallUnavailable
	}
	if target.NetworkPolicy != "" {
		// A v2 policy at install carries public targets only: a private target is granted by a
		// capability review, which can bind the runner allowlist digest an install review cannot.
		if parsed, err := appplatform.ParseHTTPPolicyAny([]byte(target.NetworkPolicy)); err != nil || parsed.Policy.HasPrivate() {
			return "", "", errAppPlatformInstallUnavailable
		}
	}
	if target.NetworkStreamPolicy != "" {
		if _, err := appplatform.ParseHTTPStreamPolicy([]byte(target.NetworkStreamPolicy)); err != nil ||
			target.NetworkProfile.StreamFormat != appplatform.HTTPStreamPolicyFormat {
			return "", "", errAppPlatformInstallUnavailable
		}
	}
	return target.NetworkPolicy, target.NetworkStreamPolicy, nil
}

// Derive the entire exact capability set from a parsed signed declaration and
// explicit host-reviewed restrictions. Optional capabilities default off. This
// function does not prove that target metadata came from a live OS observation.
func platformInstallCapabilitySelection(m appplatform.Manifest, target *platformManagedInstallTarget) ([]platformSelectedCapability, error) {
	policy, streamPolicy, err := platformNetworkTargetPolicies(target)
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
			grant := (name == "network.http" && policy != "") ||
				(name == "network.stream" && streamPolicy != "") ||
				(required && (name == "reports.read" || name == "storage.files" || name == "secrets.use" || name == "background.run" || name == "notifications.publish" || name == "tasks.schedule" || name == "events.subscribe"))
			if required && !grant {
				return nil, errAppPlatformInstallUnavailable
			}
			restriction := "{}"
			if grant && name == "network.http" {
				restriction = policy
			}
			if grant && name == "network.stream" {
				restriction = streamPolicy
			}
			result = append(result, platformSelectedCapability{capability: name, granted: grant, policy: restriction})
		}
	}
	if policy != "" && !seen["network.http"] {
		return nil, errAppPlatformInstallUnavailable
	}
	if streamPolicy != "" && !seen["network.stream"] {
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

func verifyPlatformInstallCapabilityRows(e migExec, installation string, m appplatform.Manifest, p platformInstallProjection, active bool, policy int64, planID string, overlay platformNetworkRowOverlay, fail error) error {
	expected, err := platformInstallCapabilitySelection(m, p.Runtime)
	if err != nil || policy < 1 || !platformValidUUID(planID) {
		return fail
	}
	if !slices.Equal(p.DeclinedCapabilities, platformDeclinedCapabilities(expected)) {
		return fail
	}
	return verifyPlatformSelectedCapabilityRows(e, installation, expected, active, policy, planID, overlay, fail)
}

// platformCapabilityRow is one stored grant row. The rows are read in full before any is judged: the
// network overlay reads the database itself, which a driver cannot do while a row set is still open.
type platformCapabilityRow struct {
	name, restriction, review string
	granted                   int
	revision                  int64
}

func readPlatformCapabilityRows(e migExec, installation string) ([]platformCapabilityRow, error) {
	rows, err := e.query(`SELECT capability,granted,policy_json,policy_revision,review_plan_id FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []platformCapabilityRow
	for rows.Next() {
		var r platformCapabilityRow
		if err := rows.Scan(&r.name, &r.granted, &r.restriction, &r.revision, &r.review); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func verifyPlatformSelectedCapabilityRows(e migExec, installation string, expected []platformSelectedCapability, active bool, policy int64, planID string, overlay platformNetworkRowOverlay, fail error) error {
	if policy < 1 || !platformValidUUID(planID) {
		return fail
	}
	rows, err := readPlatformCapabilityRows(e, installation)
	if err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return fail
	}
	for index, r := range rows {
		if expected[index].capability != r.name || !platformGrantMatches(r.name, r.granted, active && expected[index].granted) || !platformNetworkRowAccepted(r.name, r.granted, r.restriction, r.review, expected[index], planID, overlay) || r.revision != policy {
			return fail
		}
	}
	return nil
}

func verifyPlatformRetainedCapabilityRows(e migExec, installation string, expected []platformSelectedCapability, policy int64, overlay platformNetworkRowOverlay, fail error) error {
	if policy < 1 {
		return fail
	}
	rows, err := readPlatformCapabilityRows(e, installation)
	if err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return fail
	}
	for index, r := range rows {
		if expected[index].capability != r.name ||
			!platformGrantMatches(r.name, r.granted, expected[index].granted) ||
			!platformNetworkRowAccepted(r.name, r.granted, r.restriction, r.review, expected[index], "", overlay) ||
			r.revision != policy || !platformValidUUID(r.review) {
			return fail
		}
	}
	return nil
}

func platformSupportedRequiredCapabilities(m appplatform.Manifest) bool {
	seen := make(map[string]bool, len(m.Capabilities.Required))
	for _, capability := range m.Capabilities.Required {
		if seen[capability] || (capability != "network.http" && capability != "network.stream" && capability != "reports.read" && capability != "storage.files" && capability != "secrets.use" && capability != "background.run" && capability != "notifications.publish" && capability != "tasks.schedule" && capability != "events.subscribe") {
			return false
		}
		seen[capability] = true
	}
	return len(seen) <= 10
}
