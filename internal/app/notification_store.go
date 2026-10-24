package app

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

var errNotificationConflict = errors.New("notification state changed")
var errNotificationCapacity = errors.New("notification capacity unavailable")

type NotificationEvent struct {
	ID                   string `json:"id"`
	SourceKey            string `json:"-"`
	Type                 string `json:"event_type"`
	Payload              string `json:"-"`
	ResourceType         string `json:"-"`
	ResourceID           string `json:"-"`
	CreatedAt            int64  `json:"occurred_at"`
	ExpiresAt            int64  `json:"-"`
	candidateGenerations map[string]int64
}

type NotificationDelivery struct {
	ID              string `json:"id"`
	EventID         string `json:"event_id"`
	ChannelID       string `json:"channel_id"`
	RouteID         string `json:"route_id"`
	Username        string `json:"-"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	NextAt          int64  `json:"next_at"`
	ExpiresAt       int64  `json:"expires_at"`
	LeaseToken      string `json:"-"`
	LeaseUntil      int64  `json:"-"`
	ChannelRevision int64  `json:"-"`
	RouteRevision   int64  `json:"-"`
	Generation      int    `json:"generation"`
	LastError       string `json:"error_code"`
	StatusCode      int    `json:"status_code"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	ReplayOf        string `json:"replay_of"`
}

// CaptureNotification commits one occurrence and its bounded fan-out atomically.
// It is called after business commit; errors must not reverse business success.
func (s *Store) CaptureNotification(event NotificationEvent, deliveries []NotificationDelivery, recipients []string) error {
	return s.captureNotification(event, deliveries, recipients, 0, "")
}

func (s *Store) captureNotification(event NotificationEvent, deliveries []NotificationDelivery, recipients []string, jobID int64, sourceCycle string) error {
	if event.ID == "" || event.SourceKey == "" || event.Type == "" || len(event.Payload) > 65536 || len(deliveries) > 1000 || len(recipients) > 10000 {
		return fmt.Errorf("invalid notification event")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		// Serialize bounded capture across processes, not just goroutines. SQLite
		// already serializes this store's single write connection.
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
			return err
		}
	}
	if jobID != 0 {
		var pending string
		if err := e.queryRow(`SELECT signature FROM notification_source_cycles WHERE source_key=?`, sourceCycle).Scan(&pending); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if pending == "" {
			return nil
		}
		var username string
		if err := e.queryRow(`SELECT username FROM job_notification_intents WHERE job_id=? AND consumed_at=0`, jobID).Scan(&username); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			filtered := deliveries[:0]
			for _, d := range deliveries {
				if d.Username == "" || d.RouteID != "" {
					filtered = append(filtered, d)
				}
			}
			deliveries = filtered
		} else {
			filtered := deliveries[:0]
			for _, d := range deliveries {
				if d.Username == "" || d.Username == username || d.RouteID != "" {
					filtered = append(filtered, d)
				}
			}
			deliveries = filtered
			recipients = append(recipients, username)
		}
	}
	// Lock candidate account rows before writing reusable-username references.
	// DeleteUser deletes the account first, so it must either wait for this
	// capture and sweep its rows, or make this capture see no account. SQLite's
	// single writer already gives that ordering.
	eligible := map[string]bool{}
	users := append([]string(nil), recipients...)
	for _, d := range deliveries {
		if d.Username != "" {
			users = append(users, d.Username)
		}
	}
	sort.Strings(users)
	for _, user := range users {
		if _, seen := eligible[user]; seen {
			continue
		}
		q := `SELECT username FROM users WHERE username=? AND active=1`
		if s.driver == "postgres" {
			q += ` FOR SHARE`
		}
		var name string
		err := e.queryRow(q, user).Scan(&name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		eligible[user] = err == nil
		if expected, ok := event.candidateGenerations[user]; ok && eligible[user] {
			generation, err := notificationAccountGeneration(e, user)
			if err != nil {
				return err
			}
			eligible[user] = generation == expected
		}
	}
	var existingID, existingPayload, existingType, existingResourceType, existingResourceID string
	err = e.queryRow(`SELECT id,payload,event_type,resource_type,resource_id FROM notification_events WHERE source_key=?`, event.SourceKey).Scan(&existingID, &existingPayload, &existingType, &existingResourceType, &existingResourceID)
	if err == nil {
		payloadMatches := existingPayload == event.Payload
		if existingPayload == "" {
			var digest string
			if err := e.queryRow(`SELECT signature FROM notification_source_cycles WHERE source_key=?`, "event-tombstone:"+existingID).Scan(&digest); err != nil {
				return err
			}
			payloadMatches = digest == notificationPayloadDigest(event.Payload)
		}
		if !payloadMatches || existingType != event.Type || existingResourceType != event.ResourceType || existingResourceID != event.ResourceID {
			return errNotificationConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE state IN ('pending','leased','retry_wait')`).Scan(&active); err != nil {
		return err
	}
	if active+len(deliveries) > 10000 {
		return errNotificationCapacity
	}
	if event.ResourceType == "extension" {
		var recent int
		if err := e.queryRow(`SELECT COUNT(*) FROM notification_events WHERE event_type=? AND created_at>=?`, event.Type, time.Now().Add(-time.Hour).UnixMilli()).Scan(&recent); err != nil {
			return err
		}
		if recent >= 120 {
			return errNotificationCapacity
		}
	}
	res, err := e.exec(`INSERT INTO notification_events(id,source_key,event_type,payload,resource_type,resource_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_key) DO NOTHING`, event.ID, event.SourceKey, event.Type, event.Payload, event.ResourceType, event.ResourceID, event.CreatedAt, event.ExpiresAt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	for _, d := range deliveries {
		if d.Username != "" && !eligible[d.Username] {
			continue
		}
		if d.ID == "" {
			d.ID = randToken()
		}
		if d.CreatedAt == 0 {
			d.CreatedAt = event.CreatedAt
		}
		if d.ExpiresAt == 0 {
			d.ExpiresAt = event.CreatedAt + int64(24*time.Hour/time.Millisecond)
		}
		var pending int
		if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel_id=? AND state IN ('pending','leased','retry_wait')`, d.ChannelID).Scan(&pending); err != nil {
			return err
		}
		if pending >= 1000 {
			return errNotificationCapacity
		}
		if _, err := e.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,route_id,username,state,expires_at,channel_revision,route_revision,created_at) VALUES(?,?,?,?,?,'pending',?,?,?,?) ON CONFLICT(event_id,channel_id,route_id,username,generation) DO NOTHING`, d.ID, event.ID, d.ChannelID, d.RouteID, d.Username, d.ExpiresAt, d.ChannelRevision, d.RouteRevision, d.CreatedAt); err != nil {
			return err
		}
	}
	for _, user := range recipients {
		if !eligible[user] {
			continue
		}
		if _, err := e.exec(`INSERT INTO notification_recipients(id,event_id,username) SELECT ?,?,username FROM users WHERE username=? AND active=1 ON CONFLICT(event_id,username) DO NOTHING`, randToken(), event.ID, user); err != nil {
			return err
		}
	}
	if jobID != 0 {
		if _, err := e.exec(`UPDATE job_notification_intents SET consumed_at=? WHERE job_id=? AND consumed_at=0`, time.Now().UTC().UnixMilli(), jobID); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE notification_source_cycles SET signature='' WHERE source_key=?`, sourceCycle); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const notificationDeliveryCols = `id,event_id,channel_id,route_id,username,state,attempts,next_at,expires_at,lease_token,lease_until,channel_revision,route_revision,generation,last_error,status_code,created_at,updated_at,replay_of`

func scanNotificationDelivery(row interface{ Scan(...any) error }, extra ...any) (NotificationDelivery, error) {
	var d NotificationDelivery
	args := []any{&d.ID, &d.EventID, &d.ChannelID, &d.RouteID, &d.Username, &d.State, &d.Attempts, &d.NextAt, &d.ExpiresAt, &d.LeaseToken, &d.LeaseUntil, &d.ChannelRevision, &d.RouteRevision, &d.Generation, &d.LastError, &d.StatusCode, &d.CreatedAt, &d.UpdatedAt, &d.ReplayOf}
	err := row.Scan(append(args, extra...)...)
	return d, err
}

func (s *Store) ClaimNotification(now time.Time) (*NotificationDelivery, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		// Competing rows can belong to the same channel. Serialize only the short
		// claim transaction so two processes cannot both observe an idle channel.
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780715)`); err != nil {
			return nil, err
		}
	}
	token := randToken()
	ms := now.UnixMilli()
	// A single conditional update works on both drivers without holding a lock
	// over network I/O. A competing claim may lose; the next tick tries again.
	res, err := e.exec(`UPDATE notification_deliveries SET state='leased',lease_token=?,lease_until=?,updated_at=? WHERE id=(SELECT d.id FROM notification_deliveries d WHERE d.state IN ('pending','retry_wait') AND d.next_at<=? AND d.expires_at>? AND NOT EXISTS (SELECT 1 FROM notification_deliveries active WHERE active.channel_id=d.channel_id AND active.state='leased') ORDER BY d.updated_at,d.created_at,d.id LIMIT 1) AND state IN ('pending','retry_wait')`, token, now.Add(90*time.Second).UnixMilli(), ms, ms, ms)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return nil, err
	}
	d, err := scanNotificationDelivery(e.queryRow(`SELECT `+notificationDeliveryCols+` FROM notification_deliveries WHERE lease_token=?`, token))
	if err != nil {
		return nil, err
	}
	return &d, tx.Commit()
}

func (s *Store) BeginNotificationAttempt(d NotificationDelivery, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	res, err := e.exec(`UPDATE notification_deliveries SET sending=1,attempts=attempts+1 WHERE id=? AND lease_token=? AND state='leased' AND lease_until>? AND sending=0`, d.ID, d.LeaseToken, now.UnixMilli())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errNotificationConflict
	}
	if _, err := e.exec(`INSERT INTO notification_attempts(id,delivery_id,lease_token,started_at) VALUES(?,?,?,?)`, randToken(), d.ID, d.LeaseToken, now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishNotification(d NotificationDelivery, state, reason string, status int, now time.Time) (bool, error) {
	switch state {
	case "accepted", "failed", "retry_wait", "expired", "canceled", "uncertain":
	default:
		return false, fmt.Errorf("invalid delivery state")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	res, err := e.exec(`UPDATE notification_deliveries SET state=?,last_error=?,status_code=?,updated_at=?,next_at=?,sending=0,lease_token='',lease_until=0 WHERE id=? AND state='leased' AND lease_token=? AND lease_until>?`, state, reason, status, now.UnixMilli(), d.NextAt, d.ID, d.LeaseToken, now.UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	if _, err := e.exec(`UPDATE notification_attempts SET finished_at=?,outcome=?,error_code=?,status_code=? WHERE delivery_id=? AND lease_token=? AND finished_at=0`, now.UnixMilli(), state, reason, status, d.ID, d.LeaseToken); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) RecoverNotificationLeases(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if _, err := e.exec(`UPDATE notification_attempts SET finished_at=?,outcome='uncertain',error_code='worker_lost' WHERE finished_at=0 AND delivery_id IN (SELECT id FROM notification_deliveries WHERE state='leased' AND lease_until<=?)`, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE notification_deliveries SET state=CASE WHEN sending=1 THEN 'uncertain' ELSE 'pending' END,last_error=CASE WHEN sending=1 THEN 'worker_lost' ELSE '' END,lease_token='',lease_until=0,sending=0,updated_at=? WHERE state='leased' AND lease_until<=?`, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE notification_deliveries SET state='expired',updated_at=? WHERE state IN ('pending','retry_wait') AND expires_at<=?`, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
