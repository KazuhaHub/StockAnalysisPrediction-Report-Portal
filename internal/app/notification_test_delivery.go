package app

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

func notificationSample(kind string) NotificationEvent {
	var payload any
	switch kind {
	case EventReportIngested:
		payload = map[string]any{"id": 1, "symbol": "EXAMPLE", "name": "Example", "title": "Example report"}
	case EventBatchFinished:
		payload = map[string]any{"job_id": 1, "status": "finished", "succeeded": 1, "failed": 0, "total": 1}
	default:
		payload = map[string]string{"reason": "example", "detail": "Notification channel test"}
		if strings.HasPrefix(kind, "extension.") {
			payload = map[string]string{"title": "Example source notice", "message": "Notification channel test", "severity": "info"}
		}
	}
	body, _ := json.Marshal(payload)
	event := NotificationEvent{Type: kind, Payload: string(body), ResourceType: "operations"}
	if strings.HasPrefix(kind, "extension.") && strings.HasSuffix(kind, ".notice") {
		event.ResourceType = "extension"
		event.ResourceID = strings.TrimSuffix(strings.TrimPrefix(kind, "extension."), ".notice")
	}
	return event
}

func (s *Server) apiNotificationPreview(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		EventType string `json:"event_type"`
		EventID   string `json:"event_id"`
	}
	if readJSON(r, &in) != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid JSON")
		return
	}
	var event NotificationEvent
	if in.EventID != "" {
		var err error
		event, err = s.st.NotificationEvent(in.EventID)
		if err != nil {
			notificationAPIError(w, err)
			return
		}
	} else {
		if !notificationSelects(strings.Join(s.notificationEvents(), ","), in.EventType) {
			jsonErrorCode(w, 400, "invalid_param", "select a registered event")
			return
		}
		event = notificationSample(in.EventType)
	}
	subject, text := notificationMessage(event)
	if in.EventID == "" {
		writeJSON(w, map[string]any{"subject": subject, "text": text, "targets": []any{}, "inbox_recipients": []string{}, "sample": true})
		return
	}
	ds, recipients, err := s.routeNotification(&event, nil)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	targets := []map[string]string{}
	for _, d := range ds {
		targets = append(targets, map[string]string{"channel_id": d.ChannelID, "route_id": d.RouteID, "username": d.Username})
	}
	writeJSON(w, map[string]any{"subject": subject, "text": text, "targets": targets, "inbox_recipients": recipients, "sample": in.EventID == ""})
}

// notificationTestSlot shares a small independent bound across both current and
// compatibility test endpoints. It never consumes authentication mail capacity.
func (s *Server) notificationTestSlot() (func(), bool) {
	s.notificationTestMu.Lock()
	if s.notificationTestSlots == nil {
		s.notificationTestSlots = make(chan struct{}, 2)
	}
	slots := s.notificationTestSlots
	s.notificationTestMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

// Manual tests are separately bounded and never enter the normal worker queue.
// A test can exercise a paused channel without resuming any restored old work.
func (s *Server) apiNotificationChannelTest(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Revision  int64  `json:"revision"`
		EventType string `json:"event_type"`
		To        string `json:"to"`
	}
	if readJSON(r, &in) != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid JSON")
		return
	}
	if in.EventType == "" {
		in.EventType = EventReportIngested
	}
	if !notificationSelects(strings.Join(s.notificationEvents(), ","), in.EventType) {
		jsonErrorCode(w, 400, "invalid_param", "select a registered event")
		return
	}
	c, err := s.st.NotificationChannel(r.PathValue("id"))
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	if c.Revision != in.Revision {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	if !c.Enabled || c.Adapter == "inbox" {
		jsonErrorCode(w, 409, "notification_unavailable", "select an enabled external channel")
		return
	}
	cfg, effective, err := s.effectiveNotificationChannel(c)
	if err != nil {
		jsonErrorCode(w, 409, "notification_unavailable", "notification destination is unavailable")
		return
	}
	if c.Adapter == "email" {
		if in.To != "" {
			cfg.Recipients = []string{strings.TrimSpace(in.To)}
		}
		if len(cfg.Recipients) == 0 {
			if u := s.st.GetUser(user); u != nil && u.Email != "" {
				cfg.Recipients = []string{u.Email}
			}
		}
		if len(cfg.Recipients) == 0 {
			jsonErrorCode(w, 400, "invalid_param", "a test recipient is required")
			return
		}
		for _, recipient := range cfg.Recipients {
			a, err := mail.ParseAddress(recipient)
			if err != nil || a.Address != recipient {
				jsonErrorCode(w, 400, "invalid_param", "invalid test recipient")
				return
			}
		}
	}
	release, ok := s.notificationTestSlot()
	if !ok {
		notificationAPIError(w, errNotificationCapacity)
		return
	}
	defer release()
	sample := notificationSample(in.EventType)
	body, _ := json.Marshal(map[string]any{"sample_type": in.EventType, "sample": json.RawMessage(sample.Payload)})
	now := time.Now().UTC()
	event := NotificationEvent{ID: randToken(), SourceKey: "test:" + randToken(), Type: "notification.test", ResourceType: "operations", Payload: string(body), CreatedAt: now.UnixMilli()}
	d, err := s.st.beginNotificationTest(event, c, effective, now)
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	cfg.deliveryID = d.ID
	state, reason, status := "uncertain", "adapter_panic", 0
	func() {
		defer func() { _ = recover() }()
		accepted, uncertain, _, code, _ := s.attemptNotification(r.Context(), c, cfg, event)
		status = code
		switch {
		case accepted:
			state, reason = "accepted", ""
		case uncertain:
			state, reason = "uncertain", "acceptance_unknown"
		default:
			state, reason = "failed", "destination_rejected"
		}
	}()
	if ok, err := s.st.FinishNotification(d, state, reason, status, time.Now().UTC()); err != nil || !ok {
		if err == nil {
			err = errNotificationConflict
		}
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationTest, "notification_delivery", d.ID, map[string]any{"channel_id": c.ID, "state": state, "status_code": status})
	current, err := s.st.NotificationChannel(c.ID)
	if err == nil {
		_, revision, resolveErr := s.effectiveNotificationChannel(current)
		if resolveErr != nil || revision != effective {
			err = errNotificationConflict
		}
	}
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"code": "notification_config_changed", "error": "notification configuration changed during test", "id": d.ID})
		return
	}
	code := http.StatusOK
	if state != "accepted" {
		code = http.StatusBadGateway
		writeJSONStatus(w, code, map[string]any{"ok": false, "id": d.ID, "state": state, "error_code": reason, "status_code": status, "code": "notification_test_failed", "error": "test delivery was not accepted"})
		return
	}
	writeJSONStatus(w, code, map[string]any{"ok": state == "accepted", "id": d.ID, "state": state, "error_code": reason, "status_code": status})
}

func (s *Store) beginNotificationTest(event NotificationEvent, c NotificationChannel, effective int64, now time.Time) (NotificationDelivery, error) {
	d := NotificationDelivery{ID: randToken(), EventID: event.ID, ChannelID: c.ID, ChannelRevision: effective, State: "leased", Attempts: 1, LeaseToken: randToken(), LeaseUntil: now.Add(90 * time.Second).UnixMilli(), ExpiresAt: now.Add(24 * time.Hour).UnixMilli(), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	tx, err := s.db.Begin()
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780714)`); err != nil {
			return d, err
		}
	}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780715)`); err != nil {
			return d, err
		}
	}
	var revision int64
	if err := e.queryRow(`SELECT revision FROM notification_channels WHERE id=?`, c.ID).Scan(&revision); err != nil {
		return d, err
	}
	if revision != c.Revision {
		return d, errNotificationConflict
	}
	var active, perChannel, channelActive int
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE state IN ('pending','leased','retry_wait')`).Scan(&active); err != nil {
		return d, err
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel_id=? AND state='leased'`, c.ID).Scan(&perChannel); err != nil {
		return d, err
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel_id=? AND state IN ('pending','leased','retry_wait')`, c.ID).Scan(&channelActive); err != nil {
		return d, err
	}
	if active >= 10000 || perChannel > 0 || channelActive >= 1000 {
		return d, errNotificationCapacity
	}
	if _, err := e.exec(`INSERT INTO notification_events(id,source_key,event_type,payload,resource_type,created_at) VALUES(?,?,?,?,?,?)`, event.ID, event.SourceKey, event.Type, event.Payload, event.ResourceType, event.CreatedAt); err != nil {
		return d, err
	}
	if _, err := e.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,state,attempts,lease_token,lease_until,expires_at,created_at,updated_at,channel_revision,sending) VALUES(?,?,?,'leased',1,?,?,?,?,?,?,1)`, d.ID, d.EventID, d.ChannelID, d.LeaseToken, d.LeaseUntil, d.ExpiresAt, d.CreatedAt, d.UpdatedAt, d.ChannelRevision); err != nil {
		return d, err
	}
	if _, err := e.exec(`INSERT INTO notification_attempts(id,delivery_id,lease_token,started_at) VALUES(?,?,?,?)`, randToken(), d.ID, d.LeaseToken, now.UnixMilli()); err != nil {
		return d, err
	}
	return d, tx.Commit()
}
