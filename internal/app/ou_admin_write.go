package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strings"
)

type ouAdminError struct{ code, message string }

func (e *ouAdminError) Error() string      { return e.message }
func invalidOU(code, message string) error { return &ouAdminError{code: code, message: message} }

type ouWriteResult struct {
	ID      int64
	Changed bool
	Detail  map[string]any
}

func ouGlobalBool(e migExec, key string, baseline bool) (bool, error) {
	var raw string
	err := e.queryRow(`SELECT COALESCE(v,'') FROM meta WHERE k=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return baseline, nil
	}
	if err != nil {
		return false, err
	}
	return settingBool(raw, baseline), nil
}

// prepareOUInput builds a proposed snapshot without persisting anything. Both the mutation and
// admin preview use this path, so a parent's proposed position is part of mandate validation.
func prepareOUInput(snapshot ouPolicySnapshot, id int64, in groupInput, create bool) (ouPolicySnapshot, error) {
	if strings.TrimSpace(in.Name) == "" {
		return ouPolicySnapshot{}, invalidOU("ou_name_required", "group name required")
	}
	if snapshot.graph.defaultErr != nil {
		return ouPolicySnapshot{}, snapshot.graph.defaultErr
	}
	node, exists := snapshot.graph.nodes[id]
	if !create && !exists {
		return ouPolicySnapshot{}, sql.ErrNoRows
	}
	if create {
		node = ouNode{parent: snapshot.graph.defaultID, created: nowStr()}
	}
	if in.ParentID != nil {
		if node.isDefault && *in.ParentID != 0 {
			return ouPolicySnapshot{}, invalidOU("ou_invalid_parent", "the Default OU cannot be moved")
		}
		if !node.isDefault && *in.ParentID <= 0 {
			return ouPolicySnapshot{}, invalidOU("ou_invalid_parent", "an ordinary OU requires a parent")
		}
		node.parent = *in.ParentID
	}
	node.name = strings.TrimSpace(in.Name)
	node.description = strings.TrimSpace(in.Description)
	values := snapshot.values[id]
	values.weight, values.urgent = in.overrides(node.isDefault)
	values.allowUrgent, values.maxQueued, values.runWindow = in.governance(node.isDefault)
	restricted, quota := in.tenancy(node.isDefault)
	if restricted != nil {
		values.restricted = restricted
	}
	values.quota = nil
	if quota != nil {
		period := in.quotaPeriod()
		if !validQuotaPeriod(period) {
			period = QuotaDay
		}
		values.quota = &ouRunQuota{Limit: *quota, Period: period}
	}
	values.totp, values.passkey, values.require2FA = in.TOTPEnroll, in.PasskeyEnroll, in.Require2FA
	values.priority = nil
	if in.Priority != "" && !node.isDefault {
		values.priority = &in.Priority
	}
	if len(in.ForecastScope) != 0 {
		value, err := parseForecastScopeInput(in.ForecastScope, node.isDefault)
		if err != nil {
			return ouPolicySnapshot{}, invalidOU("forecast_bad_scope", "invalid forecast scope")
		}
		values.scope = value
	}
	nodes := maps.Clone(snapshot.graph.nodes)
	nodes[id] = node
	proposed := ouPolicySnapshot{graph: newOUGraph(nodes), values: maps.Clone(snapshot.values)}
	proposed.values[id] = values
	if _, err := proposed.graph.policyChain(id); err != nil {
		return ouPolicySnapshot{}, invalidOU("ou_invalid_parent", err.Error())
	}
	// A move also changes descendant depth and mandates; validate the entire affected subtree.
	if _, err := proposed.graph.subtree(id); err != nil {
		return ouPolicySnapshot{}, invalidOU("ou_invalid_parent", err.Error())
	}
	return proposed, nil
}

func validateOUMandates(e migExec, proposed ouPolicySnapshot) error {
	totp, err := ouGlobalBool(e, setTOTPEnroll, true)
	if err != nil {
		return err
	}
	passkey, err := ouGlobalBool(e, setPasskeyEnroll, true)
	if err != nil {
		return err
	}
	for id := range proposed.values {
		p, err := proposed.resolve(id)
		if err != nil {
			continue
		}
		if p.Require2FA.Effective && !(totp && p.TOTP.Effective) && !(passkey && p.Passkey.Effective) {
			return invalidOU("unsatisfiable_mandate", "requiring a second factor would leave no permitted enrolment method")
		}
	}
	return nil
}

func (s *Server) writeOUInput(id int64, in groupInput, create bool) (ouWriteResult, error) {
	in.Priority = s.groupPriorityValid(in.Priority)
	return s.writeOUChanges(id, create, nil, func(ouPolicySnapshot) (groupInput, error) { return in, nil })
}

// Merge only supplied fields under the same lock that validates and persists OU changes.
// Omission preserves a value; explicit null restores inheritance for nullable policies.
func (s *Server) patchOUInput(id int64, patch map[string]json.RawMessage) (ouWriteResult, error) {
	if patch == nil {
		patch = map[string]json.RawMessage{}
	}
	return s.writeOUChanges(id, false, patch, func(snapshot ouPolicySnapshot) (groupInput, error) {
		node, ok := snapshot.graph.nodes[id]
		if !ok {
			return groupInput{}, sql.ErrNoRows
		}
		value := snapshot.values[id]
		scope, err := json.Marshal(value.scope)
		if err != nil {
			return groupInput{}, err
		}
		in := groupInput{Name: node.name, Description: node.description,
			Weight: value.weight, UrgentUnlimited: value.urgent, AllowUrgent: value.allowUrgent,
			MaxQueued: value.maxQueued, RunWindow: value.runWindow, Priority: ouValue(value.priority, ""),
			Restricted: value.restricted, TOTPEnroll: value.totp, PasskeyEnroll: value.passkey,
			Require2FA: value.require2FA, ForecastScope: scope}
		if value.quota != nil {
			in.DailyRunQuota = &value.quota.Limit
			in.RunQuotaPeriod = &value.quota.Period
		}
		base, err := json.Marshal(in)
		if err != nil {
			return groupInput{}, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(base, &fields); err != nil {
			return groupInput{}, err
		}
		for key, raw := range patch {
			if _, exists := fields[key]; !exists {
				return groupInput{}, invalidOU("ou_invalid_patch", "unknown organizational-unit field")
			}
			fields[key] = raw
		}
		merged, err := json.Marshal(fields)
		if err != nil {
			return groupInput{}, err
		}
		var next groupInput
		if err := json.Unmarshal(merged, &next); err != nil {
			return groupInput{}, invalidOU("ou_invalid_patch", "invalid organizational-unit field value")
		}
		if _, supplied := patch["priority"]; supplied {
			next.Priority = s.groupPriorityValid(next.Priority)
		}
		return next, nil
	})
}

// Full replacement normalization must not materialize root defaults for omitted PATCH fields.
func preserveUnpatchedOUFields(before, proposed ouPolicySnapshot, id int64, patch map[string]json.RawMessage) {
	has := func(key string) bool { _, ok := patch[key]; return ok }
	node, nextNode := before.graph.nodes[id], proposed.graph.nodes[id]
	if has("name") {
		node.name = nextNode.name
	}
	if has("description") {
		node.description = nextNode.description
	}
	if has("parent_id") {
		node.parent = nextNode.parent
	}
	proposed.graph.nodes[id] = node
	value, next := before.values[id], proposed.values[id]
	if has("weight") {
		value.weight = next.weight
	}
	if has("urgent_unlimited") {
		value.urgent = next.urgent
	}
	if has("allow_urgent") {
		value.allowUrgent = next.allowUrgent
	}
	if has("max_queued") {
		value.maxQueued = next.maxQueued
	}
	if has("run_window") {
		value.runWindow = next.runWindow
	}
	if has("priority") {
		value.priority = next.priority
	}
	if has("restricted") {
		value.restricted = next.restricted
	}
	if has("daily_run_quota") || (has("run_quota_period") && value.quota != nil) {
		value.quota = next.quota
	}
	if has("totp_enroll") {
		value.totp = next.totp
	}
	if has("passkey_enroll") {
		value.passkey = next.passkey
	}
	if has("require_2fa") {
		value.require2FA = next.require2FA
	}
	if has("forecast_scope") {
		value.scope = next.scope
	}
	proposed.values[id] = value
}

func (s *Server) writeOUChanges(id int64, create bool, patch map[string]json.RawMessage, input func(ouPolicySnapshot) (groupInput, error)) (ouWriteResult, error) {
	var result ouWriteResult
	err := s.st.withOUMutation(func(e migExec) error {
		snapshot, err := s.st.loadOUPolicySnapshotFrom(e.ex)
		if err != nil {
			return err
		}
		in, err := input(snapshot)
		if err != nil {
			return err
		}
		previewID := id
		if create {
			previewID = 1
			for key := range snapshot.graph.nodes {
				if key >= previewID {
					previewID = key + 1
				}
			}
		}
		proposed, err := prepareOUInput(snapshot, previewID, in, create)
		if err != nil {
			return err
		}
		if patch != nil {
			preserveUnpatchedOUFields(snapshot, proposed, id, patch)
		}
		if err := validateOUMandates(e, proposed); err != nil {
			return err
		}
		node := proposed.graph.nodes[previewID]
		value := proposed.values[previewID]
		beforeNode, beforeValue := snapshot.graph.nodes[id], snapshot.values[id]
		result = ouWriteResult{ID: id, Changed: create || !reflect.DeepEqual(beforeNode, node) || !reflect.DeepEqual(beforeValue, value)}
		result.Detail = map[string]any{"name": node.name, "parent": node.parent, "restricted": value.restricted,
			"priority": value.priority, "totp_enroll": value.totp,
			"passkey_enroll": value.passkey, "require_2fa": value.require2FA}
		if value.quota != nil {
			result.Detail["quota"] = value.quota.Limit
			result.Detail["quota_period"] = value.quota.Period
		}
		if !reflect.DeepEqual(beforeValue.scope, value.scope) {
			result.Detail["forecast_scope"] = map[string]any{"from": beforeValue.scope, "to": value.scope}
		}
		if !result.Changed {
			return nil
		}
		if create {
			if err := e.queryRow(`INSERT INTO user_groups(name,description,created_at,is_default) VALUES(?,?,?,0) RETURNING id`, node.name, node.description, node.created).Scan(&result.ID); err != nil {
				return err
			}
		}
		var parent, quota, period any
		if node.parent != 0 {
			parent = node.parent
		}
		if value.quota != nil {
			quota = value.quota.Limit
			period = value.quota.Period
		}
		var priority, scope, window any
		if value.priority != nil {
			priority = *value.priority
		}
		if value.scope != nil {
			scope = *value.scope
		}
		if value.runWindow != nil {
			window = *value.runWindow
		}
		_, err = e.exec(`UPDATE user_groups SET name=?,description=?,parent_id=?,weight=?,urgent_unlimited=?,
			allow_urgent=?,max_queued=?,run_window=?,priority=?,restricted=?,daily_run_quota=?,run_quota_period=?,
			totp_enroll=?,passkey_enroll=?,require_2fa=?,forecast_scope=? WHERE id=?`,
			node.name, node.description, parent, nullInt(value.weight), nullBoolInt(value.urgent),
			nullBoolInt(value.allowUrgent), nullInt(value.maxQueued), window, priority, nullBoolInt(value.restricted), quota, period,
			nullBoolInt(value.totp), nullBoolInt(value.passkey), nullBoolInt(value.require2FA), scope, result.ID)
		return err
	})
	return result, err
}

func ouWriteHTTPError(w http.ResponseWriter, err error) {
	var invalid *ouAdminError
	switch {
	case errors.As(err, &invalid):
		jsonErrorCode(w, http.StatusBadRequest, invalid.code, invalid.message)
	case errors.Is(err, sql.ErrNoRows):
		jsonError(w, http.StatusNotFound, "OU not found")
	case errors.Is(err, errDefaultOU):
		jsonErrorCode(w, http.StatusConflict, "ou_invalid_topology", err.Error())
	case strings.Contains(strings.ToLower(err.Error()), "unique"):
		jsonErrorCode(w, http.StatusBadRequest, "ou_duplicate_name", "group name already exists")
	default:
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
	}
}
