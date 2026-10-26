package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformPublisherExact(fields map[string]json.RawMessage, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func platformPublisherCounter(fields map[string]json.RawMessage, key string) (int64, error) {
	value, err := platformManagementString(fields, key)
	if err != nil || !platformPositiveCounter(value) {
		return 0, errPlatformManagementInput
	}
	return strconv.ParseInt(value, 10, 64)
}

func platformPublisherPolicy(fields map[string]json.RawMessage) (bool, []appplatform.PublisherNamespace, error) {
	var enabled bool
	if string(fields["enabled"]) != "true" && string(fields["enabled"]) != "false" {
		return false, nil, errPlatformManagementInput
	}
	if json.Unmarshal(fields["enabled"], &enabled) != nil {
		return false, nil, errPlatformManagementInput
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["namespaces"], &items) != nil || items == nil || len(items) > 128 {
		return false, nil, errPlatformManagementInput
	}
	result := make([]appplatform.PublisherNamespace, 0, len(items))
	for _, item := range items {
		object, err := platformManagementObject(item, "kind", "value")
		if err != nil || !platformPublisherExact(object, "kind", "value") {
			return false, nil, errPlatformManagementInput
		}
		kind, kindErr := platformManagementString(object, "kind")
		value, valueErr := platformManagementString(object, "value")
		if kindErr != nil || valueErr != nil {
			return false, nil, errPlatformManagementInput
		}
		result = append(result, appplatform.PublisherNamespace{Kind: kind, Value: value})
	}
	return enabled, result, nil
}

func platformPublisherInputKey(raw json.RawMessage) (*appplatform.PublisherKey, error) {
	fields, err := platformManagementObject(raw, "id", "public_key", "not_before_ms", "not_after_ms")
	if err != nil || !platformPublisherExact(fields, "id", "public_key", "not_before_ms", "not_after_ms") {
		return nil, errPlatformManagementInput
	}
	id, idErr := platformManagementString(fields, "id")
	public, publicErr := platformManagementString(fields, "public_key")
	before, beforeErr := platformManagementString(fields, "not_before_ms")
	n, parseErr := strconv.ParseInt(before, 10, 64)
	if idErr != nil || publicErr != nil || beforeErr != nil || parseErr != nil || n < 0 || strconv.FormatInt(n, 10) != before {
		return nil, errPlatformManagementInput
	}
	var after *string
	if string(fields["not_after_ms"]) != "null" {
		value, err := platformManagementString(fields, "not_after_ms")
		if err != nil || !platformPositiveCounter(value) {
			return nil, errPlatformManagementInput
		}
		after = &value
	}
	return &appplatform.PublisherKey{ID: id, Algorithm: "Ed25519", PublicKey: public, NotBeforeMS: before, NotAfterMS: after, Revision: "1"}, nil
}

func platformPublisherDecode(w http.ResponseWriter, r *http.Request) (platformPublisherChange, error) {
	var zero platformPublisherChange
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		return zero, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, "action", "publisher_id", "expected_revision", "enabled", "namespaces", "key", "key_id", "expected_key_revision")
	if err != nil {
		return zero, err
	}
	action, actionErr := platformManagementString(fields, "action")
	id, idErr := platformManagementString(fields, "publisher_id")
	if actionErr != nil || idErr != nil || len(id) == 0 || len(id) > 64 {
		return zero, errPlatformManagementInput
	}
	change := platformPublisherChange{kind: action, publisherID: id}
	switch action {
	case "register":
		if !platformPublisherExact(fields, "action", "publisher_id", "enabled", "namespaces", "key") {
			return zero, errPlatformManagementInput
		}
		change.enabled, change.namespaces, err = platformPublisherPolicy(fields)
		if err == nil {
			change.key, err = platformPublisherInputKey(fields["key"])
		}
	case "policy":
		if !platformPublisherExact(fields, "action", "publisher_id", "expected_revision", "enabled", "namespaces") {
			return zero, errPlatformManagementInput
		}
		change.expectedRevision, err = platformPublisherCounter(fields, "expected_revision")
		if err == nil {
			change.enabled, change.namespaces, err = platformPublisherPolicy(fields)
		}
	case "add_key":
		if !platformPublisherExact(fields, "action", "publisher_id", "expected_revision", "key") {
			return zero, errPlatformManagementInput
		}
		change.expectedRevision, err = platformPublisherCounter(fields, "expected_revision")
		if err == nil {
			change.key, err = platformPublisherInputKey(fields["key"])
		}
	case "revoke_key":
		if !platformPublisherExact(fields, "action", "publisher_id", "expected_revision", "key_id", "expected_key_revision") {
			return zero, errPlatformManagementInput
		}
		change.expectedRevision, err = platformPublisherCounter(fields, "expected_revision")
		if err == nil {
			change.keyID, err = platformManagementString(fields, "key_id")
		}
		if err == nil {
			change.expectedKeyRevision, err = platformPublisherCounter(fields, "expected_key_revision")
		}
	default:
		return zero, errPlatformManagementInput
	}
	return change, err
}

func (s *Server) apiPlatformPublisherChange(w http.ResponseWriter, r *http.Request, user string) {
	change, err := platformPublisherDecode(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	result, err := s.changePlatformPublisherTrustContext(r.Context(), actor, change)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	out := map[string]string{"publisher_id": result.publisherID, "revision": strconv.FormatInt(result.revision, 10)}
	if result.keyID != "" {
		out["key_id"], out["key_revision"] = result.keyID, strconv.FormatInt(result.keyRevision, 10)
	}
	writeJSON(w, out)
}

func (s *Server) apiPlatformControlInitialize(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "action")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	action, err := platformManagementString(fields, "action")
	if err != nil || action != "initialize" {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	err = s.withAppPlatformActorContext(r.Context(), actor, true, func(e migExec, facts platformActorFacts, control platformActorControl) error {
		if _, err := platformRefreshActor(e, actor, facts, control); err != nil {
			return err
		}
		_, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.control_initialized','app_platform_control','','{}','')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), facts.username, facts.ouID)
		if err != nil {
			return err
		}
		_, err = platformRefreshActor(e, actor, facts, control)
		return err
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"initialized": true})
}

type platformPublisherDirectory struct {
	Initialized bool                           `json:"initialized"`
	Paused      bool                           `json:"paused"`
	Publishers  []appplatform.TrustedPublisher `json:"publishers"`
}

func (s *Server) readPlatformPublisherDirectory(ctx context.Context, actor platformActorSession) (platformPublisherDirectory, error) {
	result := platformPublisherDirectory{Publishers: []appplatform.TrustedPublisher{}}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err = lockOUMutation(e); err != nil {
		return result, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return result, err
	}
	account, err := platformReadActorAccount(e, actor, now, true)
	if err != nil {
		return result, err
	}
	if err = lockPlatformPublisherTrust(e); err != nil {
		return result, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return result, err
	}
	if len(trust.Publishers) > 0 {
		result.Publishers = trust.Publishers
	}
	var epoch, paused, seed sql.NullString
	if err = e.queryRow(`SELECT (SELECT v FROM meta WHERE k='app_platform_controller_epoch'),(SELECT v FROM meta WHERE k='app_platform_restore_paused'),(SELECT v FROM meta WHERE k='app_platform_actor_seed')`).Scan(&epoch, &paused, &seed); err != nil {
		return result, err
	}
	result.Initialized = epoch.Valid || paused.Valid || seed.Valid
	result.Paused = result.Initialized && (!epoch.Valid || !platformValidUUID(epoch.String) || !paused.Valid || paused.String != "0" || !seed.Valid || !strings.HasPrefix(seed.String, sealPrefix))
	if !result.Initialized {
		result.Paused, err = platformControlHasHistory(e)
		if err != nil {
			return result, err
		}
	}
	final, err := platformDatabaseNow(e)
	if err != nil {
		return result, err
	}
	current, err := platformReadActorAccount(e, actor, final, true)
	if err != nil {
		return result, err
	}
	if current != account || final < now {
		return result, errAppPlatformActorUnavailable
	}
	if err = tx.Commit(); err != nil {
		return platformPublisherDirectory{}, err
	}
	return result, nil
}

func (s *Server) apiPlatformPublishers(w http.ResponseWriter, r *http.Request, user string) {
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	result, err := s.readPlatformPublisherDirectory(r.Context(), actor)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, result)
}
