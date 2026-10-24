package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Revocation cannot undo a request already on the wire. Keep that attempt
// uncertain, fence its former worker, and cancel only work that never started.
func revokeNotificationWork(e migExec, predicate, reason string, args ...any) error {
	ms := time.Now().UTC().UnixMilli()
	attemptArgs := append([]any{ms, reason}, args...)
	if _, err := e.exec(`UPDATE notification_attempts SET finished_at=?,outcome='uncertain',error_code=? WHERE finished_at=0 AND delivery_id IN (SELECT id FROM notification_deliveries WHERE `+predicate+`)`, attemptArgs...); err != nil {
		return err
	}
	deliveryArgs := append([]any{reason, ms}, args...)
	_, err := e.exec(`UPDATE notification_deliveries SET state=CASE WHEN state='leased' AND sending=1 THEN 'uncertain' ELSE 'canceled' END,last_error=?,updated_at=?,lease_token='',lease_until=0,sending=0 WHERE state IN ('pending','leased','retry_wait') AND `+predicate, deliveryArgs...)
	return err
}

func (s *Server) apiNotificationChannelDelete(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Revision int64 `json:"revision"`
	}
	if readJSON(r, &in) != nil || in.Revision <= 0 {
		jsonErrorCode(w, 400, "invalid_param", "channel revision is required")
		return
	}
	id := r.PathValue("id")
	err := s.st.withNotificationConfig(func(e migExec) error {
		var revision int64
		var ref string
		if err := e.queryRow(`SELECT revision,legacy_ref FROM notification_channels WHERE id=?`, id).Scan(&revision, &ref); err != nil {
			return err
		}
		if revision != in.Revision {
			return errNotificationConflict
		}
		if strings.HasPrefix(id, "legacy:") && !strings.HasPrefix(ref, "webhook:") {
			return errNotificationConflict
		}
		if err := revokeNotificationWork(e, "channel_id=?", "channel_deleted", id); err != nil {
			return err
		}
		if _, err := e.exec(`DELETE FROM notification_route_grants WHERE route_id IN (SELECT id FROM notification_routes WHERE channel_id=?)`, id); err != nil {
			return err
		}
		if _, err := e.exec(`DELETE FROM notification_routes WHERE channel_id=?`, id); err != nil {
			return err
		}
		if _, err := e.exec(`DELETE FROM notification_channels WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := e.exec(`DELETE FROM notification_config_mappings WHERE channel_id=?`, id); err != nil {
			return err
		}
		if strings.HasPrefix(ref, "webhook:") {
			wid, err := strconv.ParseInt(strings.TrimPrefix(ref, "webhook:"), 10, 64)
			if err != nil {
				return err
			}
			if _, err := e.exec(`DELETE FROM webhooks WHERE id=?`, wid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationChange, "notification_channel", id, map[string]any{"deleted": true})
	writeJSON(w, okJSON)
}

func (s *Server) apiNotificationRouteDelete(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Revision int64 `json:"revision"`
	}
	if readJSON(r, &in) != nil || in.Revision <= 0 {
		jsonErrorCode(w, 400, "invalid_param", "rule revision is required")
		return
	}
	id := r.PathValue("id")
	err := s.st.withNotificationConfig(func(e migExec) error {
		res, err := e.exec(`DELETE FROM notification_routes WHERE id=? AND revision=?`, id, in.Revision)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errNotificationConflict
		}
		if _, err := e.exec(`DELETE FROM notification_route_grants WHERE route_id=?`, id); err != nil {
			return err
		}
		return revokeNotificationWork(e, "route_id=?", "route_deleted", id)
	})
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationChange, "notification_rule", id, map[string]any{"deleted": true})
	writeJSON(w, okJSON)
}

// The legacy delete facade shares revocation behavior with channel management.
// It still succeeds for an absent receiver, matching its original contract.
func (s *Store) deleteWebhookAndNotifications(id int64) error {
	return s.withNotificationConfig(func(e migExec) error {
		channel := "legacy:webhook:" + itoa64(id)
		if err := revokeNotificationWork(e, "channel_id=?", "channel_deleted", channel); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM notification_route_grants WHERE route_id IN (SELECT id FROM notification_routes WHERE channel_id=?)`,
			`DELETE FROM notification_routes WHERE channel_id=?`,
			`DELETE FROM notification_config_mappings WHERE channel_id=?`,
			`DELETE FROM notification_channels WHERE id=?`,
		} {
			if _, err := e.exec(q, channel); err != nil {
				return err
			}
		}
		_, err := e.exec(`DELETE FROM webhooks WHERE id=?`, id)
		return err
	})
}
