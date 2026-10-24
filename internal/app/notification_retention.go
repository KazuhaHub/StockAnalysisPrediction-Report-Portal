package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type notificationRetention struct {
	Enabled     bool `json:"enabled"`
	HistoryDays int  `json:"history_days"`
	InboxDays   int  `json:"inbox_days"`
}

func (s *Store) notificationRetention() notificationRetention {
	history, _ := strconv.Atoi(s.GetSetting("notification_history_days", "30"))
	inbox, _ := strconv.Atoi(s.GetSetting("notification_inbox_days", "90"))
	return notificationRetention{Enabled: s.GetSetting("notification_retention_enabled", "0") == "1", HistoryDays: history, InboxDays: inbox}
}

func notificationPayloadDigest(payload string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

// Cleanup is opt-in. Unknown external acceptance is never automatically discarded.
// Event metadata and a payload digest remain as compact deduplication evidence.
func (s *Store) CleanupNotifications(now time.Time) error {
	policy := s.notificationRetention()
	if !policy.Enabled || s.GetSetting("notification_dispatch_paused", "0") == "1" {
		return nil
	}
	if policy.HistoryDays < 1 || policy.HistoryDays > 3650 || policy.InboxDays < 1 || policy.InboxDays > 3650 {
		return fmt.Errorf("invalid notification retention policy")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		for _, key := range []int{780716, 780714, 780715} {
			if _, err := e.exec(`SELECT pg_advisory_xact_lock(?)`, key); err != nil {
				return err
			}
		}
	}
	// Recheck under exclusion with policy writers; a concurrent disable wins.
	if e.setting("notification_retention_enabled") != "1" || e.setting("notification_dispatch_paused") == "1" {
		return nil
	}
	history, err := strconv.Atoi(e.setting("notification_history_days"))
	if err != nil {
		history = 30
	}
	inbox, err := strconv.Atoi(e.setting("notification_inbox_days"))
	if err != nil {
		inbox = 90
	}
	if history < 1 || history > 3650 || inbox < 1 || inbox > 3650 {
		return fmt.Errorf("invalid notification retention policy")
	}
	historyBefore := now.Add(-time.Duration(history) * 24 * time.Hour).UnixMilli()
	inboxBefore := now.Add(-time.Duration(inbox) * 24 * time.Hour).UnixMilli()
	// Purge a whole event's closed delivery family together, preserving replay links
	// until every delivery in the family has passed the history window.
	rows, err := e.query(`SELECT DISTINCT d.event_id FROM notification_deliveries d WHERE NOT EXISTS (SELECT 1 FROM notification_deliveries live WHERE live.event_id=d.event_id AND (live.state IN ('pending','leased','retry_wait','uncertain') OR live.updated_at>=? OR live.created_at>=?)) ORDER BY d.event_id LIMIT 500`, historyBefore, historyBefore)
	if err != nil {
		return err
	}
	var events []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		events = append(events, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range events {
		if _, err := e.exec(`DELETE FROM notification_attempts WHERE delivery_id IN (SELECT id FROM notification_deliveries WHERE event_id=?)`, id); err != nil {
			return err
		}
		if _, err := e.exec(`DELETE FROM notification_deliveries WHERE event_id=?`, id); err != nil {
			return err
		}
	}
	if _, err := e.exec(`DELETE FROM notification_recipients WHERE id IN (SELECT r.id FROM notification_recipients r JOIN notification_events event ON event.id=r.event_id WHERE event.created_at<? ORDER BY event.created_at,r.id LIMIT 1000)`, inboxBefore); err != nil {
		return err
	}
	// Payload compaction never runs while any inbox or delivery reference remains.
	rows, err = e.query(`SELECT id,payload FROM notification_events event WHERE event.payload<>'' AND event.created_at<? AND NOT EXISTS (SELECT 1 FROM notification_deliveries d WHERE d.event_id=event.id) AND NOT EXISTS (SELECT 1 FROM notification_recipients r WHERE r.event_id=event.id) ORDER BY event.created_at,event.id LIMIT 500`, min(historyBefore, inboxBefore))
	if err != nil {
		return err
	}
	type compacted struct{ id, payload string }
	var payloads []compacted
	for rows.Next() {
		var item compacted
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return err
		}
		payloads = append(payloads, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range payloads {
		if _, err := e.exec(`INSERT INTO notification_source_cycles(source_key,occurrence_id,signature) VALUES(?,?,?) ON CONFLICT(source_key) DO NOTHING`, "event-tombstone:"+item.id, item.id, notificationPayloadDigest(item.payload)); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE notification_events SET payload='' WHERE id=?`, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) apiNotificationRetention(w http.ResponseWriter, r *http.Request, user string) {
	var in notificationRetention
	if readJSON(r, &in) != nil || in.HistoryDays < 1 || in.HistoryDays > 3650 || in.InboxDays < 1 || in.InboxDays > 3650 {
		jsonErrorCode(w, 400, "invalid_param", "notification retention must be between 1 and 3650 days")
		return
	}
	err := s.st.withNotificationConfig(func(e migExec) error {
		for key, value := range map[string]string{"notification_retention_enabled": boolStr(in.Enabled), "notification_history_days": strconv.Itoa(in.HistoryDays), "notification_inbox_days": strconv.Itoa(in.InboxDays)} {
			if err := e.setSetting(key, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationChange, "notification_retention", "operational", map[string]any{"enabled": in.Enabled, "history_days": in.HistoryDays, "inbox_days": in.InboxDays})
	writeJSON(w, map[string]any{"ok": true, "retention": in})
}
