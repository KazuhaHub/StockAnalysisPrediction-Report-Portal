package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The two administrator routes of the capability policy review (doc 175 section 2.2). Both sit
// under the admin management route pattern, so a non-administrator, a cross-origin request or a
// body that is not closed JSON is refused before any plan is written.

func platformManagementInt(raw json.RawMessage) (int64, bool) {
	var value int64
	if json.Unmarshal(raw, &value) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || value < 0 {
		return 0, false
	}
	return value, true
}

func platformManagementStrings(raw json.RawMessage) ([]string, bool) {
	var list []string
	if json.Unmarshal(raw, &list) != nil || list == nil {
		return nil, false
	}
	return list, true
}

func platformCapabilityTargetFromJSON(raw json.RawMessage) (appplatform.HTTPTarget, error) {
	var t appplatform.HTTPTarget
	fields, err := platformManagementObject(raw, "id", "origin", "paths", "methods", "network", "requestBytes", "responseBytes", "deadlineMs")
	if err != nil {
		return t, err
	}
	for _, key := range []string{"id", "origin", "paths", "methods", "network"} {
		if _, ok := fields[key]; !ok {
			return t, errPlatformManagementInput
		}
	}
	if t.ID, err = platformManagementString(fields, "id"); err != nil {
		return t, err
	}
	if t.Origin, err = platformManagementString(fields, "origin"); err != nil {
		return t, err
	}
	if t.Network, err = platformManagementString(fields, "network"); err != nil {
		return t, err
	}
	var ok bool
	if t.Paths, ok = platformManagementStrings(fields["paths"]); !ok {
		return t, errPlatformManagementInput
	}
	if t.Methods, ok = platformManagementStrings(fields["methods"]); !ok {
		return t, errPlatformManagementInput
	}
	for key, dst := range map[string]*int64{"requestBytes": &t.RequestBytes, "responseBytes": &t.ResponseBytes} {
		if raw, present := fields[key]; present {
			if *dst, ok = platformManagementInt(raw); !ok || *dst == 0 {
				return t, errPlatformManagementInput
			}
		}
	}
	if raw, present := fields["deadlineMs"]; present {
		ms, ok := platformManagementInt(raw)
		if !ok || ms == 0 {
			return t, errPlatformManagementInput
		}
		t.Deadline = time.Duration(ms) * time.Millisecond
	}
	return t, nil
}

func platformCapabilityBoundsFromJSON(raw json.RawMessage) (platformCapabilityBounds, error) {
	var b platformCapabilityBounds
	fields, err := platformManagementObject(raw, "requestBytes", "responseBytes", "deadlineMs", "concurrency")
	if err != nil || len(fields) == 0 {
		return b, errPlatformManagementInput
	}
	for key, dst := range map[string]*int64{"requestBytes": &b.RequestBytes, "responseBytes": &b.ResponseBytes} {
		if raw, present := fields[key]; present {
			var ok bool
			if *dst, ok = platformManagementInt(raw); !ok || *dst == 0 {
				return b, errPlatformManagementInput
			}
		}
	}
	if raw, present := fields["deadlineMs"]; present {
		ms, ok := platformManagementInt(raw)
		if !ok || ms == 0 {
			return b, errPlatformManagementInput
		}
		b.Deadline = time.Duration(ms) * time.Millisecond
	}
	if raw, present := fields["concurrency"]; present {
		n, ok := platformManagementInt(raw)
		if !ok || n == 0 || n > 1<<20 {
			return b, errPlatformManagementInput
		}
		b.Concurrency = int(n)
	}
	return b, nil
}

func platformCapabilityRequestFromBody(w http.ResponseWriter, r *http.Request) (platformCapabilityRequest, error) {
	var req platformCapabilityRequest
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		return req, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, "capability", "policyRevision", "previousPolicySha256", "add", "remove", "bounds")
	if err != nil {
		return req, err
	}
	for _, key := range []string{"capability", "policyRevision", "previousPolicySha256", "add", "remove"} {
		if _, ok := fields[key]; !ok {
			return req, errPlatformManagementInput
		}
	}
	capability, err := platformManagementString(fields, "capability")
	if err != nil || capability != platformCapabilityNetworkHTTP {
		return req, errPlatformManagementInput
	}
	revision, err := platformManagementString(fields, "policyRevision")
	if err != nil || !platformPositiveCounter(revision) {
		return req, errPlatformManagementInput
	}
	req.PolicyRevision, _ = strconv.ParseInt(revision, 10, 64)
	if req.PreviousPolicySHA256, err = platformManagementString(fields, "previousPolicySha256"); err != nil || !platformValidSHA(req.PreviousPolicySHA256) {
		return req, errPlatformManagementInput
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["add"], &items) != nil || items == nil || len(items) > platformCapabilityTargetsMax {
		return req, errPlatformManagementInput
	}
	for _, item := range items {
		t, err := platformCapabilityTargetFromJSON(item)
		if err != nil {
			return req, err
		}
		req.Add = append(req.Add, t)
	}
	var ok bool
	if req.Remove, ok = platformManagementStrings(fields["remove"]); !ok || len(req.Remove) > appplatform.MaxHTTPPolicyTargets {
		return req, errPlatformManagementInput
	}
	if raw, present := fields["bounds"]; present {
		if req.Bounds, err = platformCapabilityBoundsFromJSON(raw); err != nil {
			return req, err
		}
	}
	return req, nil
}

type platformCapabilityDiffTarget struct {
	platformCapabilityTarget
	PlainHTTP bool                        `json:"plain_http"`
	Allowlist *appplatform.PrivateNetwork `json:"allowlist"`
}

// platformCapabilityDiff is the server-computed difference the administrator confirms: the
// targets the plan adds with their class and the allowlist entry that admits them, the targets it
// removes, and the bindings the consume will pause.
func (s *Server) platformCapabilityDiff(session platformActorSession, plan platformCapabilityPolicyPlan) (map[string]any, error) {
	policy, err := appplatform.ParseHTTPPolicyV2([]byte(plan.Runtime.NetworkPolicy))
	if err != nil {
		return nil, errPlatformCapabilityPolicy
	}
	added := make([]platformCapabilityDiffTarget, 0, len(plan.TargetsAdded))
	for _, view := range plan.TargetsAdded {
		item := platformCapabilityDiffTarget{platformCapabilityTarget: view}
		for _, t := range policy.Policy.Targets {
			if t.ID != view.ID || t.Network != appplatform.HTTPNetworkPrivate {
				continue
			}
			if entry, ok := platformAllowlistEntry(plan.PrivateNetworks, policy.Policy, t); ok {
				item.Allowlist = &entry
			}
			item.PlainHTTP = len(t.Origin) > 7 && t.Origin[:7] == "http://"
		}
		added = append(added, item)
	}
	targets := map[string]bool{}
	for _, t := range policy.Policy.Targets {
		sum, err := appplatform.HTTPTargetSHA256(t)
		if err != nil {
			return nil, errPlatformCapabilityPolicy
		}
		targets[sum] = true
	}
	paused := []string{}
	err = s.withAppPlatformActorRead(session, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		rows, err := e.query(`SELECT name,policy_json FROM app_platform_secret_bindings WHERE installation_id=? AND state='active' AND operation IN ('http_bearer','http_api_key') ORDER BY name`, plan.InstallationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, raw string
			if err := rows.Scan(&name, &raw); err != nil {
				return err
			}
			if platformCapabilityBindingPauses(raw, targets) {
				paused = append(paused, name)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"added": added, "removed": plan.TargetsRemoved, "paused_bindings": paused}, nil
}

func (s *Server) apiPlatformCapabilityPolicyPlan(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	req, err := platformCapabilityRequestFromBody(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	result, err := s.createPlatformCapabilityPolicyReview(r.Context(), session, endpoint, installation, req)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	diff, err := s.platformCapabilityDiff(session, result.Plan)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"plan_id": result.ID, "plan_sha256": result.SHA256, "expires_ms": strconv.FormatInt(result.ExpiresMS, 10),
		"policy_revision": strconv.FormatInt(result.Plan.PolicyRevision, 10), "diff": diff,
	})
}

func (s *Server) apiPlatformCapabilityPolicyConsume(w http.ResponseWriter, r *http.Request, user string) {
	planID := r.PathValue("id")
	if !platformValidUUID(planID) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	planSHA, err := platformManagementString(fields, "sha256")
	if err != nil || !platformValidSHA(planSHA) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	change, err := s.consumePlatformCapabilityPolicyReview(r.Context(), session, endpoint, planID, planSHA)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "policy_revision": strconv.FormatInt(change.PolicyRevision, 10), "paused_bindings": platformNonNilStrings(change.Paused)})
}
