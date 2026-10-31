package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
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

func (s *Server) apiPlatformSecretBindingPlan(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "name", "credential_id", "operation")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	name, nameErr := platformManagementString(fields, "name")
	credential, credentialErr := platformManagementString(fields, "credential_id")
	operation, operationErr := platformManagementString(fields, "operation")
	if nameErr != nil || credentialErr != nil || operationErr != nil || !platformSecretBindingNamePattern.MatchString(name) || !platformValidUUID(credential) || (operation != "http_bearer" && operation != "http_api_key") {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	plan, err := s.createPlatformSecretBindingReview(actor, r.PathValue("installation"), name, credential, operation)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var review platformSecretBindingReview
	if json.Unmarshal([]byte(plan.JSON), &review) != nil {
		platformManagementError(w, errAppPlatformCredentialUnavailable)
		return
	}
	writeJSON(w, map[string]any{"plan_id": plan.ID, "plan_sha256": plan.SHA256, "expires_ms": strconv.FormatInt(plan.ExpiresMS, 10), "review": map[string]any{"name": review.Name, "credential_id": review.CredentialID, "credential_label": review.CredentialLabel, "operation": review.Operation, "network_policy_sha256": review.NetworkPolicySHA256}})
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
	fields, err := platformManagementRead(w, r, "name", "credential_id", "operation")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	name, nameErr := platformManagementString(fields, "name")
	credential, credentialErr := platformManagementString(fields, "credential_id")
	operation, operationErr := platformManagementString(fields, "operation")
	if nameErr != nil || credentialErr != nil || operationErr != nil || !platformSecretBindingNamePattern.MatchString(name) || !platformValidUUID(credential) || (operation != "http_bearer" && operation != "http_api_key") {
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
	plan, err := s.createPlatformSecretBindingReview(actor, installation, name, credential, operation)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	var review platformSecretBindingReview
	if json.Unmarshal([]byte(plan.JSON), &review) != nil {
		platformManagementError(w, errAppPlatformCredentialUnavailable)
		return
	}
	writeJSON(w, map[string]any{"installation_id": installation, "plan_id": plan.ID, "plan_sha256": plan.SHA256, "expires_ms": strconv.FormatInt(plan.ExpiresMS, 10), "review": map[string]any{"name": review.Name, "credential_id": review.CredentialID, "credential_label": review.CredentialLabel, "operation": review.Operation, "network_policy_sha256": review.NetworkPolicySHA256}})
}
