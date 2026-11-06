package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var (
	errAppPlatformEventConflict    = errors.New("application event subscription state changed")
	errAppPlatformEventUnavailable = errors.New("application event subscription unavailable")
	errAppPlatformEventQuota       = errors.New("application event subscription quota exhausted")
)

type platformEventSubscription struct {
	ID              string
	InstallationID  string
	AppID           string
	PublisherID     string
	EventType       string
	ResourceType    string
	ResourceID      string
	State           string
	Generation      int64
	PolicyRevision  int64
	ReleaseSHA256   string
	ControllerEpoch string
	MaxInflight     int64
	Revision        int64
	CreatedMS       int64
	UpdatedMS       int64
}

func platformEventSubscriptionResources(eventType string) []string {
	resources := []string{"events.subscribe"}
	if eventType == appplatform.EventReportIngested {
		resources = append(resources, "reports.read")
	}
	return resources
}

func (s *Server) createPlatformEventSubscription(
	ctx context.Context,
	installation string,
	spec appplatform.EventSubscriptionSpec,
) (string, bool, error) {
	if s == nil || s.st == nil || !platformValidUUID(installation) ||
		appplatform.ValidateEventSubscription(spec) != nil {
		return "", false, errAppPlatformEventUnavailable
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	facts, err := loadPlatformServiceFacts(
		e, installation, platformEventSubscriptionResources(spec.EventType), true)
	if err != nil {
		return "", false, errAppPlatformEventUnavailable
	}
	if err := lockPlatformEventSubscriptionAdmission(e); err != nil {
		return "", false, err
	}
	var existing platformEventSubscription
	err = readPlatformEventSubscription(e,
		`installation_id=? AND event_type=? AND resource_type=? AND resource_id=?`,
		[]any{installation, spec.EventType, spec.ResourceType, spec.ResourceID},
		&existing, true)
	if err == nil {
		if existing.State != "active" ||
			existing.Generation != int64Value(facts.generation) ||
			existing.PolicyRevision != int64Value(facts.policyRevision) ||
			existing.ReleaseSHA256 != facts.release ||
			existing.ControllerEpoch != facts.epoch ||
			existing.MaxInflight != int64(spec.MaxInflight) {
			return "", false, errAppPlatformEventConflict
		}
		if err := tx.Commit(); err != nil {
			return "", false, err
		}
		return existing.ID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	var installationCount, globalCount int64
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_event_subscriptions
		WHERE installation_id=?`, installation).Scan(&installationCount); err != nil {
		return "", false, err
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_event_subscriptions`).
		Scan(&globalCount); err != nil {
		return "", false, err
	}
	if installationCount >= 16 || globalCount >= 1000 {
		return "", false, errAppPlatformEventQuota
	}
	id, err := platformNewUUID()
	if err != nil {
		return "", false, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return "", false, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_event_subscriptions(
		id,installation_id,app_id,publisher_id,event_type,resource_type,
		resource_id,state,generation,policy_revision,release_sha256,
		controller_epoch,max_inflight,revision,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,'active',?,?,?,?,?,1,?,?)`,
		id, installation, facts.appID, facts.publisherID, spec.EventType,
		spec.ResourceType, spec.ResourceID, facts.generation,
		facts.policyRevision, facts.release, facts.epoch, spec.MaxInflight,
		now, now); err != nil {
		return "", false, err
	}
	detail, _ := json.Marshal(map[string]any{
		"event_type": spec.EventType, "resource_type": spec.ResourceType,
		"resource_id": spec.ResourceID, "max_inflight": spec.MaxInflight,
	})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.event_subscribed',
		'app_platform_event_subscription',?,?,'')`,
		timeUnixMilliUTC(now), id, string(detail)); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func lockPlatformEventSubscriptionAdmission(e migExec) error {
	if e.s.driver == "postgres" {
		_, err := e.exec(`SELECT pg_advisory_xact_lock(780720)`)
		return err
	}
	return nil
}

func int64Value(value string) int64 {
	out, _ := strconv.ParseInt(value, 10, 64)
	return out
}

func readPlatformEventSubscription(
	e migExec, where string, args []any, subscription *platformEventSubscription,
	lock bool,
) error {
	lockSQL := ""
	if lock && e.s.driver == "postgres" {
		lockSQL = " FOR UPDATE"
	}
	return e.queryRow(`SELECT id,installation_id,app_id,publisher_id,
		event_type,resource_type,resource_id,state,generation,policy_revision,
		release_sha256,controller_epoch,max_inflight,revision,created_ms,
		updated_ms FROM app_platform_event_subscriptions WHERE `+where+lockSQL,
		args...).Scan(
		&subscription.ID, &subscription.InstallationID, &subscription.AppID,
		&subscription.PublisherID, &subscription.EventType,
		&subscription.ResourceType, &subscription.ResourceID,
		&subscription.State, &subscription.Generation,
		&subscription.PolicyRevision, &subscription.ReleaseSHA256,
		&subscription.ControllerEpoch, &subscription.MaxInflight,
		&subscription.Revision, &subscription.CreatedMS,
		&subscription.UpdatedMS)
}

func (s *Server) transitionPlatformEventSubscription(
	ctx context.Context, id, installation, state string, expectedRevision int64,
) error {
	if !platformValidUUID(id) || !platformValidUUID(installation) ||
		expectedRevision < 1 ||
		(state != "active" && state != "paused" && state != "revoked") {
		return errAppPlatformEventConflict
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	var current platformEventSubscription
	if err := readPlatformEventSubscription(e,
		`id=? AND installation_id=?`, []any{id, installation},
		&current, true); err != nil {
		return errAppPlatformEventConflict
	}
	if current.Revision != expectedRevision || current.State == "revoked" {
		return errAppPlatformEventConflict
	}
	if state == "active" {
		if _, err := platformEventSubscriptionCurrentAuthority(e, current); err != nil {
			return err
		}
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_event_subscriptions
		SET state=?,revision=revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND revision=?`,
		state, now, id, installation, expectedRevision); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func platformEventSubscriptionCurrentAuthority(
	e migExec, subscription platformEventSubscription,
) (platformServiceFacts, error) {
	facts, err := loadPlatformServiceFacts(
		e, subscription.InstallationID,
		platformEventSubscriptionResources(subscription.EventType), true)
	if err != nil ||
		facts.appID != subscription.AppID ||
		facts.publisherID != subscription.PublisherID ||
		facts.generation != strconv.FormatInt(subscription.Generation, 10) ||
		facts.policyRevision != strconv.FormatInt(subscription.PolicyRevision, 10) ||
		facts.release != subscription.ReleaseSHA256 ||
		facts.epoch != subscription.ControllerEpoch {
		return platformServiceFacts{}, errAppPlatformEventUnavailable
	}
	return facts, nil
}

func (s *Server) rebindPlatformEventSubscription(
	ctx context.Context, id, installation string, expectedRevision int64,
) error {
	if !platformValidUUID(id) || !platformValidUUID(installation) ||
		expectedRevision < 1 {
		return errAppPlatformEventConflict
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	var current platformEventSubscription
	if err := readPlatformEventSubscription(e,
		`id=? AND installation_id=?`, []any{id, installation},
		&current, true); err != nil || current.Revision != expectedRevision ||
		current.State == "revoked" {
		return errAppPlatformEventConflict
	}
	facts, err := loadPlatformServiceFacts(
		e, installation,
		platformEventSubscriptionResources(current.EventType), true)
	if err != nil {
		return errAppPlatformEventUnavailable
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_event_subscriptions
		SET state='active',generation=?,policy_revision=?,release_sha256=?,
		    controller_epoch=?,revision=revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND revision=?`,
		facts.generation, facts.policyRevision, facts.release, facts.epoch,
		now, id, installation, expectedRevision); err != nil {
		return err
	}
	return tx.Commit()
}

func cancelPlatformEventSubscriptionsForInstallation(
	e migExec, installation, state string, now int64,
) error {
	if state != "paused" && state != "revoked" && state != "review_required" {
		return fmt.Errorf("invalid event subscription state %q", state)
	}
	_, err := e.exec(`UPDATE app_platform_event_subscriptions
		SET state=?,revision=revision+1,updated_ms=?
		WHERE installation_id=? AND state<>? AND state<>'revoked'`,
		state, now, installation, state)
	return err
}

func (s *Store) capturePlatformEventDeliveries(e migExec, event NotificationEvent) error {
	if event.ResourceType != "report" && event.ResourceType != "job" {
		return nil
	}
	rows, err := e.query(`SELECT id,installation_id,app_id,publisher_id,
		event_type,resource_type,resource_id,state,generation,policy_revision,
		release_sha256,controller_epoch,max_inflight,revision,created_ms,
		updated_ms FROM app_platform_event_subscriptions
		WHERE event_type=? AND resource_type=? AND resource_id=? AND state='active'
		ORDER BY installation_id,id`, event.Type, event.ResourceType,
		event.ResourceID)
	if err != nil {
		return err
	}
	var subscriptions []platformEventSubscription
	for rows.Next() {
		var subscription platformEventSubscription
		if err := rows.Scan(
			&subscription.ID, &subscription.InstallationID, &subscription.AppID,
			&subscription.PublisherID, &subscription.EventType,
			&subscription.ResourceType, &subscription.ResourceID,
			&subscription.State, &subscription.Generation,
			&subscription.PolicyRevision, &subscription.ReleaseSHA256,
			&subscription.ControllerEpoch, &subscription.MaxInflight,
			&subscription.Revision, &subscription.CreatedMS,
			&subscription.UpdatedMS); err != nil {
			rows.Close()
			return err
		}
		subscriptions = append(subscriptions, subscription)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		if _, err := platformEventSubscriptionCurrentAuthority(
			e, subscription); err != nil {
			if err := fencePlatformEventSubscriptionForReview(
				e, subscription, now, "authority_lost"); err != nil {
				return err
			}
			continue
		}
		activeDeliveries, err := countPlatformEventDeliveries(
			e, subscription.InstallationID, subscription.ID)
		if err != nil {
			return err
		}
		if activeDeliveries >= subscription.MaxInflight {
			if err := fencePlatformEventSubscriptionForReview(
				e, subscription, now, "inflight_limit"); err != nil {
				return err
			}
			continue
		}
		occurrence, err := appplatform.EventOccurrence(
			event.ID, uint64(subscription.Revision))
		if err != nil {
			return errNotificationConflict
		}
		payload, _, err := appplatform.BuildEventDelivery(
			subscription.ID, event.ID, event.Type, event.ResourceType,
			event.ResourceID, event.CreatedAt, []byte(event.Payload))
		if err != nil {
			return errNotificationConflict
		}
		expires := event.ExpiresAt
		if expires == 0 {
			expires = now + (24 * time.Hour).Milliseconds()
		}
		if expires <= now {
			continue
		}
		if expires > now+platformTaskMaxLifetime.Milliseconds() {
			expires = now + platformTaskMaxLifetime.Milliseconds()
		}
		_, _, err = s.admitPlatformTaskTx(e, subscription.InstallationID,
			platformTaskAdmission{
				OccurrenceID: occurrence,
				Handler:      appplatform.TaskHandlerEvent,
				Payload:      payload,
				ExpiresMS:    expires,
			})
		if errors.Is(err, errAppPlatformTaskConflict) {
			return errNotificationConflict
		}
		if errors.Is(err, errAppPlatformTaskQuota) {
			if err := fencePlatformEventSubscriptionForReview(
				e, subscription, now, "task_capacity"); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, errAppPlatformTaskUnavailable) {
			if err := fencePlatformEventSubscriptionForReview(
				e, subscription, now, "task_authority_lost"); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func countPlatformEventDeliveries(
	e migExec, installation, subscription string,
) (int64, error) {
	rows, err := e.query(`SELECT payload FROM app_platform_tasks
		WHERE installation_id=? AND handler='app.event.delivery'
		AND state NOT IN ('succeeded','failed','cancelled')`,
		installation)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return 0, err
		}
		delivery, err := appplatform.ParseEventDelivery(payload)
		if err != nil {
			return 0, err
		}
		if delivery.Subscription == subscription {
			count++
		}
	}
	return count, rows.Err()
}

func fencePlatformEventSubscriptionForReview(
	e migExec, subscription platformEventSubscription, now int64, reason string,
) error {
	updated, err := e.exec(`UPDATE app_platform_event_subscriptions
		SET state='review_required',revision=revision+1,updated_ms=?
		WHERE id=? AND revision=? AND state='active'`,
		now, subscription.ID, subscription.Revision)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil || affected != 1 {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"reason": reason})
	_, err = e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.event_review_required',
		'app_platform_event_subscription',?,?,'')`,
		timeUnixMilliUTC(now), subscription.ID, string(detail))
	return err
}
