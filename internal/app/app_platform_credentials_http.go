package app

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformCredentialValueField(raw []byte, required bool) ([]byte, error) {
	if len(raw) == 0 {
		if required {
			return nil, errPlatformManagementInput
		}
		return nil, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, errPlatformManagementInput
	}
	if encoded == "" && !required {
		return nil, nil
	}
	value, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(value) != encoded || len(value) < 1 || len(value) > platformCredentialValueLimit {
		clear(value)
		return nil, errPlatformManagementInput
	}
	return value, nil
}

func (s *Server) apiPlatformCredentialList(w http.ResponseWriter, r *http.Request, user string) {
	after := r.URL.Query().Get("after")
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		limit = parsed
	}
	if len(r.URL.Query()) > 2 || r.URL.Query().Get("cursor") != "" {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items, err := s.listPlatformCredentials(actor, after, limit)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	writeJSON(w, map[string]any{"credentials": items, "next_after": next})
}

func (s *Server) apiPlatformCredentialCreate(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "label", "value_base64")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	label, err := platformManagementString(fields, "label")
	if err != nil || !validPlatformCredentialLabel(label) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	value, err := platformCredentialValueField(fields["value_base64"], true)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	defer clear(value)
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	item, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: label, value: value})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, item)
}

func (s *Server) apiPlatformCredentialPreview(w http.ResponseWriter, r *http.Request, user string) {
	id := r.PathValue("id")
	fields, err := platformManagementRead(w, r, "action", "revision", "value_revision")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	revision, revisionErr := platformManagementString(fields, "revision")
	valueRevision, valueRevisionErr := platformManagementString(fields, "value_revision")
	rev, revErr1 := strconv.ParseInt(revision, 10, 64)
	valueRev, revErr2 := strconv.ParseInt(valueRevision, 10, 64)
	if actionErr != nil || revisionErr != nil || valueRevisionErr != nil || revErr1 != nil || revErr2 != nil || !platformPositiveCounter(revision) || !platformPositiveCounter(valueRevision) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	item, err := s.previewPlatformCredentialChange(actor, id, rev, valueRev, action)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	result := map[string]any{"direct": item.Direct, "affected": item.Affected}
	if !item.Direct {
		result["plan_id"] = item.ID
		result["plan_sha256"] = item.SHA256
		result["expires_ms"] = strconv.FormatInt(item.ExpiresMS, 10)
	}
	writeJSON(w, result)
}

func (s *Server) apiPlatformCredentialCommit(w http.ResponseWriter, r *http.Request, user string) {
	id := r.PathValue("id")
	fields, err := platformManagementRead(w, r, "action", "revision", "value_revision", "value_base64", "plan_id", "plan_sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, actionErr := platformManagementString(fields, "action")
	revision, revisionErr := platformManagementString(fields, "revision")
	valueRevision, valueRevisionErr := platformManagementString(fields, "value_revision")
	planID, planIDErr := platformManagementString(fields, "plan_id")
	planSHA, planSHAErr := platformManagementString(fields, "plan_sha256")
	rev, revErr1 := strconv.ParseInt(revision, 10, 64)
	valueRev, revErr2 := strconv.ParseInt(valueRevision, 10, 64)
	if actionErr != nil || revisionErr != nil || valueRevisionErr != nil || !platformPositiveCounter(revision) || !platformPositiveCounter(valueRevision) || revErr1 != nil || revErr2 != nil || (action != "replace" && action != "revoke") {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	value, err := platformCredentialValueField(fields["value_base64"], action == "replace")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	defer clear(value)
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if planIDErr == nil && planSHAErr == nil && planID != "" && planSHA != "" {
		if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
			platformManagementError(w, errPlatformManagementInput)
			return
		}
		item, err := s.consumePlatformCredentialReview(actor, planID, planSHA, value)
		if err != nil {
			platformManagementError(w, err)
			return
		}
		writeJSON(w, item)
		return
	}
	item, err := s.changePlatformCredential(actor, platformCredentialChange{kind: action, id: id, revision: rev, valueRevision: valueRev, value: value})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, item)
}

// platformSecretBindingPlanRequest is a decoded binding review request.
type platformSecretBindingPlanRequest struct {
	name, credential, operation, principals string
	acs3                                    []platformACS3Action
}

// platformSecretBindingPlanFields reads a binding review request: name,
// credential_id and operation, and optionally principals, a sorted list of
// "viewer" and "service" (absent means the viewer only), and, for an
// acs3_hmac_sha256 binding, acs3_actions: the (action, version) pairs it may sign.
func platformSecretBindingPlanFields(w http.ResponseWriter, r *http.Request) (platformSecretBindingPlanRequest, error) {
	var zero, out platformSecretBindingPlanRequest
	raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if readErr != nil {
		return zero, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, "name", "credential_id", "operation", "principals", "acs3_actions")
	_, hasPrincipals := fields["principals"]
	_, hasActions := fields["acs3_actions"]
	if err != nil || len(fields) != 3+boolInt(hasPrincipals)+boolInt(hasActions) {
		return zero, errPlatformManagementInput
	}
	name, nameErr := platformManagementString(fields, "name")
	credential, credentialErr := platformManagementString(fields, "credential_id")
	operation, operationErr := platformManagementString(fields, "operation")
	if nameErr != nil || credentialErr != nil || operationErr != nil || !platformSecretBindingNamePattern.MatchString(name) || !platformValidUUID(credential) ||
		(operation != "http_bearer" && operation != "http_api_key" && operation != "raw_read" && operation != "acs3_hmac_sha256") {
		return zero, errPlatformManagementInput
	}
	out.name, out.credential, out.operation = name, credential, operation
	if hasPrincipals {
		var list []string
		if json.Unmarshal(fields["principals"], &list) != nil || list == nil {
			return zero, errPlatformManagementInput
		}
		switch strings.Join(list, ",") {
		case platformPrincipalViewer:
			out.principals = ""
		case platformPrincipalsServiceOnly:
			out.principals = platformPrincipalsServiceOnly
		case platformPrincipalsBoth:
			out.principals = platformPrincipalsBoth
		default:
			return zero, errPlatformManagementInput
		}
	}
	if hasActions {
		var items []json.RawMessage
		if json.Unmarshal(fields["acs3_actions"], &items) != nil || items == nil {
			return zero, errPlatformManagementInput
		}
		for _, item := range items {
			pair, err := platformManagementObject(item, "action", "version")
			if err != nil || len(pair) != 2 {
				return zero, errPlatformManagementInput
			}
			action, actionErr := platformManagementString(pair, "action")
			version, versionErr := platformManagementString(pair, "version")
			if actionErr != nil || versionErr != nil {
				return zero, errPlatformManagementInput
			}
			out.acs3 = append(out.acs3, platformACS3Action{Action: action, Version: version})
		}
	}
	// Whether the list belongs to the operation (and is a valid one) is the review's to decide.
	return out, nil
}

// platformBindingNetworkOrigin is the origin of the installation's reviewed network policy, which is
// where a binding's credential will be sent, or "" when it cannot be read. It is shown to the
// reviewer; it decides nothing.
func (s *Server) platformBindingNetworkOrigin(session platformActorSession, installation string) string {
	var origin string
	_ = s.withAppPlatformActor(session, false, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		var raw string
		if err := e.queryRow(`SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http' AND granted=1`, installation).Scan(&raw); err != nil {
			return err
		}
		policy, err := appplatform.ParseHTTPPolicy([]byte(raw))
		if err != nil {
			return err
		}
		origin = policy.Policy.Origin
		return nil
	})
	return origin
}

// platformSecretBindingReviewResponse is the review a plan response shows.
func platformSecretBindingReviewResponse(review platformSecretBindingReview, principals, origin string) map[string]any {
	out := map[string]any{"network_origin": origin, "name": review.Name, "credential_id": review.CredentialID, "credential_label": review.CredentialLabel, "operation": review.Operation, "network_policy_sha256": review.NetworkPolicySHA256, "principals": platformSecretBindingPrincipalList(principals)}
	if len(review.ACS3Actions) > 0 {
		out["acs3_actions"] = review.ACS3Actions
	}
	return out
}

func (s *Server) apiPlatformSecretBindingPlan(w http.ResponseWriter, r *http.Request, user string) {
	request, err := platformSecretBindingPlanFields(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	plan, err := s.createPlatformSecretBindingReviewWith(actor, r.PathValue("installation"), request.name, request.credential, request.operation, request.principals, request.acs3)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var review platformSecretBindingReview
	if json.Unmarshal([]byte(plan.JSON), &review) != nil {
		platformManagementError(w, errAppPlatformCredentialUnavailable)
		return
	}
	writeJSON(w, map[string]any{"plan_id": plan.ID, "plan_sha256": plan.SHA256, "expires_ms": strconv.FormatInt(plan.ExpiresMS, 10), "review": platformSecretBindingReviewResponse(review, request.principals, s.platformBindingNetworkOrigin(actor, review.InstallationID))})
}

func (s *Server) apiPlatformSecretBindingCommit(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "plan_id", "plan_sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	planID, err1 := platformManagementString(fields, "plan_id")
	planSHA, err2 := platformManagementString(fields, "plan_sha256")
	if err1 != nil || err2 != nil || !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	item, err := s.consumePlatformSecretBindingReview(actor, planID, planSHA)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, item)
}

func (s *Server) apiPlatformSecretBindingList(w http.ResponseWriter, r *http.Request, user string) {
	if len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items, err := s.listPlatformSecretBindings(actor, r.PathValue("installation"))
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"bindings": items})
}

func (s *Server) apiPlatformSecretBindingRevoke(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "revision")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	raw, err := platformManagementString(fields, "revision")
	revision, parseErr := strconv.ParseInt(raw, 10, 64)
	if err != nil || parseErr != nil || !platformPositiveCounter(raw) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err := s.revokePlatformSecretBinding(actor, r.PathValue("id"), revision); err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": r.PathValue("id"), "state": "revoked"})
}

func (s *Server) platformInstallationForApp(session platformActorSession, appID string) (string, error) {
	if !platformDirectoryAppID.MatchString(appID) {
		return "", errPlatformManagementInput
	}
	var installation string
	err := s.withAppPlatformActor(session, false, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		return e.queryRow(`SELECT id FROM app_platform_installations WHERE app_id=? AND desired_state<>'removed'`, appID).Scan(&installation)
	})
	if err != nil || !platformValidUUID(installation) {
		return "", errPlatformOperationMissing
	}
	return installation, nil
}

func (s *Server) apiPlatformAppSecretBindingList(w http.ResponseWriter, r *http.Request, user string) {
	if len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	installation, err := s.platformInstallationForApp(actor, r.PathValue("app"))
	if err != nil {
		platformManagementError(w, err)
		return
	}
	items, err := s.listPlatformSecretBindings(actor, installation)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"installation_id": installation, "bindings": items})
}

func (s *Server) apiPlatformAppSecretBindingPlan(w http.ResponseWriter, r *http.Request, user string) {
	request, err := platformSecretBindingPlanFields(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	installation, err := s.platformInstallationForApp(actor, r.PathValue("app"))
	if err != nil {
		platformManagementError(w, err)
		return
	}
	plan, err := s.createPlatformSecretBindingReviewWith(actor, installation, request.name, request.credential, request.operation, request.principals, request.acs3)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var review platformSecretBindingReview
	if json.Unmarshal([]byte(plan.JSON), &review) != nil {
		platformManagementError(w, errAppPlatformCredentialUnavailable)
		return
	}
	writeJSON(w, map[string]any{"installation_id": installation, "plan_id": plan.ID, "plan_sha256": plan.SHA256, "expires_ms": strconv.FormatInt(plan.ExpiresMS, 10), "review": platformSecretBindingReviewResponse(review, request.principals, s.platformBindingNetworkOrigin(actor, review.InstallationID))})
}
