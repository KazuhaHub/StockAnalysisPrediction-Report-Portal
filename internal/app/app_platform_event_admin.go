package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) apiPlatformEventSubscriptionList(
	w http.ResponseWriter, r *http.Request, _ string,
) {
	limit := int64(100)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 200 {
			jsonErrorCode(w, 400, "invalid_param", "limit must be 1..200")
			return
		}
		limit = value
	}
	installation := r.URL.Query().Get("installation")
	state := r.URL.Query().Get("state")
	if installation != "" && !platformValidUUID(installation) {
		jsonErrorCode(w, 400, "invalid_param", "invalid installation")
		return
	}
	if state != "" && state != "active" && state != "paused" &&
		state != "revoked" && state != "review_required" {
		jsonErrorCode(w, 400, "invalid_param", "invalid subscription state")
		return
	}
	query := `SELECT id,installation_id,app_id,publisher_id,event_type,
		resource_type,resource_id,state,generation,policy_revision,
		release_sha256,controller_epoch,max_inflight,revision,created_ms,
		updated_ms FROM app_platform_event_subscriptions`
	var where []string
	var args []any
	if installation != "" {
		where = append(where, "installation_id=?")
		args = append(args, installation)
	}
	if state != "" {
		where = append(where, "state=?")
		args = append(args, state)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_ms DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.st.query(query, args...)
	if err != nil {
		jsonErrorCode(w, 500, "db_error", "event subscriptions unavailable")
		return
	}
	defer rows.Close()
	subscriptions := []map[string]any{}
	for rows.Next() {
		var sub platformEventSubscription
		if err := rows.Scan(
			&sub.ID, &sub.InstallationID, &sub.AppID, &sub.PublisherID,
			&sub.EventType, &sub.ResourceType, &sub.ResourceID, &sub.State,
			&sub.Generation, &sub.PolicyRevision, &sub.ReleaseSHA256,
			&sub.ControllerEpoch, &sub.MaxInflight, &sub.Revision,
			&sub.CreatedMS, &sub.UpdatedMS); err != nil {
			jsonErrorCode(w, 500, "db_error", "event subscriptions unavailable")
			return
		}
		subscriptions = append(subscriptions, map[string]any{
			"id": sub.ID, "installation_id": sub.InstallationID,
			"app_id": sub.AppID, "event_type": sub.EventType,
			"resource_type": sub.ResourceType, "resource_id": sub.ResourceID,
			"state": sub.State, "generation": sub.Generation,
			"policy_revision": sub.PolicyRevision,
			"release_sha256":  sub.ReleaseSHA256,
			"max_inflight":    sub.MaxInflight, "revision": sub.Revision,
			"created_ms": sub.CreatedMS, "updated_ms": sub.UpdatedMS,
		})
	}
	if err := rows.Err(); err != nil {
		jsonErrorCode(w, 500, "db_error", "event subscriptions unavailable")
		return
	}
	writeJSON(w, map[string]any{"subscriptions": subscriptions})
}

func (s *Server) apiPlatformEventSubscriptionCreate(
	w http.ResponseWriter, r *http.Request, _ string,
) {
	raw, err := readPlatformEventBody(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	fields, err := platformManagementObject(raw,
		"installationId", "eventType", "resourceType", "resourceId",
		"maxInflight")
	if err != nil || len(fields) != 5 {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	installation, err := platformManagementString(fields, "installationId")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	eventType, err := platformManagementString(fields, "eventType")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	resourceType, err := platformManagementString(fields, "resourceType")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	resourceID, err := platformManagementString(fields, "resourceId")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	var maxInflight int
	if err := decodePlatformEventNumber(fields["maxInflight"], &maxInflight); err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	spec := appplatform.EventSubscriptionSpec{
		EventType: eventType, ResourceType: resourceType,
		ResourceID: resourceID, MaxInflight: uint64(maxInflight),
	}
	if appplatform.ValidateEventSubscription(spec) != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
		return
	}
	id, created, err := s.createPlatformEventSubscription(
		r.Context(), installation, spec)
	if err != nil {
		platformEventFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": id, "created": created})
}

func (s *Server) apiPlatformEventSubscriptionState(
	w http.ResponseWriter, r *http.Request, _ string,
) {
	raw, err := readPlatformEventBody(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription state")
		return
	}
	fields, err := platformManagementObject(raw, "state", "expectedRevision")
	if err != nil || len(fields) != 2 {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription state")
		return
	}
	state, err := platformManagementString(fields, "state")
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription state")
		return
	}
	var revision int64
	if err := decodePlatformEventNumber(
		fields["expectedRevision"], &revision); err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription state")
		return
	}
	installation := r.URL.Query().Get("installation")
	if !platformValidUUID(installation) {
		jsonErrorCode(w, 400, "invalid_param", "installation is required")
		return
	}
	if err := s.transitionPlatformEventSubscription(
		r.Context(), r.PathValue("id"), installation, state, revision); err != nil {
		platformEventFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiPlatformEventSubscriptionRebind(
	w http.ResponseWriter, r *http.Request, _ string,
) {
	raw, err := readPlatformEventBody(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid rebind request")
		return
	}
	fields, err := platformManagementObject(raw, "expectedRevision")
	if err != nil || len(fields) != 1 {
		jsonErrorCode(w, 400, "invalid_param", "invalid rebind request")
		return
	}
	var revision int64
	if err := decodePlatformEventNumber(
		fields["expectedRevision"], &revision); err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid rebind request")
		return
	}
	installation := r.URL.Query().Get("installation")
	if !platformValidUUID(installation) {
		jsonErrorCode(w, 400, "invalid_param", "installation is required")
		return
	}
	if err := s.rebindPlatformEventSubscription(
		r.Context(), r.PathValue("id"), installation, revision); err != nil {
		platformEventFailure(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func readPlatformEventBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
}

func decodePlatformEventNumber(raw []byte, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errPlatformManagementInput
	}
	return json.Unmarshal(raw, target)
}

func platformEventFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformManagementInput):
		jsonErrorCode(w, 400, "invalid_param", "invalid event subscription")
	case errors.Is(err, errAppPlatformEventConflict):
		jsonErrorCode(w, 409, "event_subscription_conflict",
			"event subscription changed or is not active")
	case errors.Is(err, errAppPlatformEventUnavailable):
		jsonErrorCode(w, 404, "event_subscription_unavailable",
			"event subscription authority unavailable")
	case errors.Is(err, errAppPlatformEventQuota):
		jsonErrorCode(w, 429, "event_subscription_capacity",
			"event subscription capacity unavailable")
	default:
		jsonErrorCode(w, 500, "db_error", "event subscription failed")
	}
}
