package app

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var errNotificationReview = errors.New("possible duplicate delivery requires review")

func notificationReplayKey(id, requestID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(id+"\x00"+requestID)))
}

func (s *Store) NotificationDelivery(id string) (NotificationDelivery, error) {
	return scanNotificationDelivery(s.queryRow(`SELECT `+notificationDeliveryCols+` FROM notification_deliveries WHERE id=?`, id))
}

// ReplayNotification enqueues only a new delivery generation for an existing
// event. It does not invoke a source handler or mutate the original evidence.
// effectiveRevision is resolved by the server; the worker checks it again.
func (s *Store) ReplayNotification(id, requestID string, channelRevision, effectiveRevision, routeRevision int64, confirmDuplicate bool, now time.Time) (NotificationDelivery, bool, error) {
	var none NotificationDelivery
	if len(requestID) < 8 || len(requestID) > 128 {
		return none, false, errNotificationConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return none, false, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
			return none, false, err
		}
	}
	key := notificationReplayKey(id, requestID)
	existing, err := scanNotificationDelivery(e.queryRow(`SELECT `+notificationDeliveryCols+` FROM notification_deliveries WHERE replay_key=?`, key))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return none, false, err
	}
	original, err := scanNotificationDelivery(e.queryRow(`SELECT `+notificationDeliveryCols+` FROM notification_deliveries WHERE id=?`, id))
	if err != nil {
		return none, false, err
	}
	if original.LastError == "account_deleted" {
		return none, false, errNotificationConflict
	}
	if original.LastError == "reviewed_uncertain" && !confirmDuplicate {
		return none, false, errNotificationReview
	}
	switch original.State {
	case "failed", "expired", "canceled":
	case "accepted", "uncertain":
		if !confirmDuplicate {
			return none, false, errNotificationReview
		}
	default:
		return none, false, errNotificationConflict
	}
	var current int64
	var enabled int
	if err := e.queryRow(`SELECT revision,enabled FROM notification_channels WHERE id=?`, original.ChannelID).Scan(&current, &enabled); err != nil {
		return none, false, err
	}
	if current != channelRevision || enabled == 0 {
		return none, false, errNotificationConflict
	}
	if original.RouteID != "" {
		if err := e.queryRow(`SELECT revision,enabled FROM notification_routes WHERE id=?`, original.RouteID).Scan(&current, &enabled); err != nil {
			return none, false, err
		}
		if current != routeRevision || enabled == 0 {
			return none, false, errNotificationConflict
		}
	}
	if original.Username != "" {
		query := `SELECT username FROM users WHERE username=? AND active=1`
		if s.driver == "postgres" {
			query += ` FOR SHARE`
		}
		var username string
		if err := e.queryRow(query, original.Username).Scan(&username); err != nil {
			return none, false, errNotificationConflict
		}
		var reason string
		if err := e.queryRow(`SELECT last_error FROM notification_deliveries WHERE id=?`, id).Scan(&reason); err != nil || reason == "account_deleted" {
			return none, false, errNotificationConflict
		}
	}
	var active, channelActive int
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE state IN ('pending','retry_wait','leased')`).Scan(&active); err != nil {
		return none, false, err
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel_id=? AND state IN ('pending','retry_wait','leased')`, original.ChannelID).Scan(&channelActive); err != nil {
		return none, false, err
	}
	if active >= 10000 || channelActive >= 1000 {
		return none, false, errNotificationCapacity
	}
	var eventExists int
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_events WHERE id=?`, original.EventID).Scan(&eventExists); err != nil {
		return none, false, err
	}
	if eventExists != 1 {
		return none, false, sql.ErrNoRows
	}
	var generation int
	if err := e.queryRow(`SELECT COALESCE(MAX(generation),0)+1 FROM notification_deliveries WHERE event_id=? AND channel_id=? AND route_id=? AND username=?`, original.EventID, original.ChannelID, original.RouteID, original.Username).Scan(&generation); err != nil {
		return none, false, err
	}
	d := NotificationDelivery{ID: randToken(), EventID: original.EventID, ChannelID: original.ChannelID, RouteID: original.RouteID, Username: original.Username, State: "pending", ChannelRevision: effectiveRevision, RouteRevision: routeRevision, Generation: generation, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), ExpiresAt: now.Add(24 * time.Hour).UnixMilli(), ReplayOf: id}
	if _, err := e.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,route_id,username,state,channel_revision,route_revision,generation,replay_of,replay_key,created_at,updated_at,expires_at) VALUES(?,?,?,?,?,'pending',?,?,?,?,?,?,?,?)`, d.ID, d.EventID, d.ChannelID, d.RouteID, d.Username, d.ChannelRevision, d.RouteRevision, d.Generation, id, key, d.CreatedAt, d.UpdatedAt, d.ExpiresAt); err != nil {
		return none, false, err
	}
	if err := tx.Commit(); err != nil {
		return none, false, err
	}
	return d, true, nil
}
