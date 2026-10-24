package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	portalmail "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/mail"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/notification"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

type notificationChannelConfig struct {
	Recipients       []string `json:"recipients,omitempty"`
	URL              string   `json:"url,omitempty"`
	DeliveryRevision int64    `json:"delivery_revision,omitempty"`
	smtp             *portalmail.Config
	signingSecret    string
	deliveryID       string
}

// effectiveNotificationChannel resolves legacy settings at the moment of use.
// A fingerprint detects changes made through old APIs without duplicating their
// credentials into a second independently editable configuration.
func (s *Server) effectiveNotificationChannel(c NotificationChannel) (notificationChannelConfig, int64, error) {
	var cfg notificationChannelConfig
	if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
		return cfg, 0, err
	}
	fingerprint := ""
	var snapshot notificationSettingsSnapshot
	if c.LegacyRef == "smtp" || c.LegacyRef == "forecast-email" || c.LegacyRef == "forecast-slack" {
		var err error
		snapshot, err = s.st.notificationSettings()
		if err != nil {
			return cfg, 0, err
		}
	}
	switch {
	case c.LegacyRef == "smtp":
		mc := snapshot.mailConfig()
		cfg.smtp = &mc
		fingerprint = snapshot.channelFingerprint(c.LegacyRef)
	case c.LegacyRef == "forecast-email":
		cfg.Recipients = alertRecipients(snapshot.values[setForecastAlertEmailTo])
		mc := snapshot.mailConfig()
		cfg.smtp = &mc
		fingerprint = snapshot.channelFingerprint(c.LegacyRef)
	case c.LegacyRef == "forecast-slack":
		cfg.URL = snapshot.values[setForecastAlertSlackURL]
		fingerprint = snapshot.channelFingerprint(c.LegacyRef)
	case strings.HasPrefix(c.LegacyRef, "webhook:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(c.LegacyRef, "webhook:"), 10, 64)
		if err != nil {
			return cfg, 0, err
		}
		w, ok := s.st.GetWebhook(id)
		if !ok {
			return cfg, 0, fmt.Errorf("destination disabled")
		}
		cfg.URL = w.URL
		cfg.signingSecret = w.Secret
		if !w.Active {
			return cfg, 0, fmt.Errorf("destination disabled")
		}
		fingerprint = fmt.Sprintf("%s/%s/%v", w.URL, w.Secret, w.Events)
	default:
		if c.Secret != "" {
			value, err := s.openSecret("notification:"+c.ID, "destination", c.Secret)
			if err != nil {
				return cfg, 0, err
			}
			var destination notificationDestinationSecret
			if err := json.Unmarshal([]byte(value), &destination); err != nil {
				return cfg, 0, err
			}
			cfg.URL = destination.URL
			cfg.signingSecret = destination.SigningSecret
		}
		fingerprint = cfg.URL + "\x00" + cfg.signingSecret
	}
	if c.LegacyRef == "" && cfg.DeliveryRevision == 0 {
		return cfg, c.Revision, nil
	}
	base := cfg.DeliveryRevision
	if base == 0 {
		base = c.Revision
	}
	config, _ := json.Marshal(notificationChannelConfig{Recipients: cfg.Recipients, DeliveryRevision: base})
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s", base, config, fingerprint)))
	return cfg, int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff), nil
}

func (s *Server) notificationMayRead(user string, e NotificationEvent) bool {
	u := s.st.GetUser(user)
	if u == nil || !u.Active || s.accountExpired(u) {
		return false
	}
	switch e.ResourceType {
	case "report":
		id, err := strconv.ParseInt(e.ResourceID, 10, 64)
		if err != nil {
			return false
		}
		r, err := s.st.GetNew(id, s.viewerScope(user))
		return err == nil && r != nil
	case "job":
		jobID, owner, hasOwner := strings.Cut(e.ResourceID, "@")
		id, err := strconv.ParseInt(jobID, 10, 64)
		if err != nil {
			return false
		}
		expected := int64(0)
		if hasOwner {
			expected, err = strconv.ParseInt(owner, 10, 64)
			if err != nil || expected < 0 {
				return false
			}
		}
		current, err := notificationAccountGeneration(migExec{s: s.st, ex: s.st.db}, user)
		if err != nil || current != expected {
			return false
		}
		j, ok := s.st.GetBatchJob(id)
		return ok && j.CreatedBy == user
	case "operations":
		return u.IsAdmin()
	case "extension":
		return e.Type == "extension."+e.ResourceID+".notice" && (strings.HasPrefix(e.ResourceID, "app.") || strings.HasPrefix(e.ResourceID, "service."))
	default:
		return false
	}
}

func (s *Server) processNotificationOnce(ctx context.Context, now time.Time) error {
	if s.st.GetSetting("notification_dispatch_paused", "0") == "1" {
		return nil
	}
	d, err := s.st.ClaimNotification(now)
	if err != nil || d == nil {
		return err
	}
	var c NotificationChannel
	finish := func(state, reason string, status int) error {
		ok, err := s.st.FinishNotification(*d, state, reason, status, time.Now().UTC())
		if err == nil && ok {
			s.recordNotificationCompatibility(c, state, reason)
		}
		return err
	}
	c, err = s.st.NotificationChannel(d.ChannelID)
	if err != nil {
		return finish("canceled", "channel_missing", 0)
	}
	if s.st.GetSetting("notification_dispatch_paused", "0") == "1" {
		return finish("retry_wait", "dispatch_paused", 0)
	}
	if !c.Enabled {
		return finish("canceled", "channel_disabled", 0)
	}
	if c.Paused {
		d.NextAt = now.Add(30 * time.Second).UnixMilli()
		return finish("retry_wait", "channel_paused", 0)
	}
	cfg, revision, err := s.effectiveNotificationChannel(c)
	if err != nil {
		return finish("canceled", "channel_unavailable", 0)
	}
	if d.ChannelRevision != 0 && d.ChannelRevision != revision {
		return finish("canceled", "config_changed", 0)
	}
	event, err := s.st.NotificationEvent(d.EventID)
	if err != nil {
		return finish("canceled", "event_missing", 0)
	}
	mandatory := false
	if d.RouteID != "" {
		var enabled bool
		var current int64
		var mode string
		if err := s.st.queryRow(`SELECT enabled,revision,mandatory,recipient_mode FROM notification_routes WHERE id=?`, d.RouteID).Scan(&enabled, &current, &mandatory, &mode); err != nil || !enabled || current != d.RouteRevision {
			return finish("canceled", "route_changed", 0)
		}
		if d.Username != "" && mode == "grant" && !s.notificationRouteAllows(d.RouteID, d.Username) {
			return finish("canceled", "audience_revoked", 0)
		}
	}
	if d.Username != "" {
		if !s.notificationMayRead(d.Username, event) {
			return finish("canceled", "access_revoked", 0)
		}
		var enabled int
		err := s.st.queryRow(`SELECT enabled FROM notification_preferences WHERE username=? AND event_type=?`, d.Username, event.Type).Scan(&enabled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			d.NextAt = time.Now().Add(30 * time.Second).UnixMilli()
			return finish("retry_wait", "preference_unavailable", 0)
		}
		if err == nil && enabled == 0 && !mandatory {
			return finish("canceled", "recipient_muted", 0)
		}
		u := s.st.GetUser(d.Username)
		if u == nil || u.Email == "" {
			return finish("canceled", "recipient_unavailable", 0)
		}
		cfg.Recipients = []string{u.Email}
	}
	if ctx.Err() != nil {
		return finish("retry_wait", "worker_stopping", 0)
	}
	if err := s.st.BeginNotificationAttempt(*d, time.Now().UTC()); err != nil {
		return err
	}
	cfg.deliveryID = d.ID
	accepted, uncertain, retryable, status, after := s.attemptNotification(ctx, c, cfg, event)
	if accepted {
		return finish("accepted", "", status)
	}
	if uncertain {
		return finish("uncertain", "acceptance_unknown", status)
	}
	maxAttempts := 5
	if strings.HasPrefix(c.LegacyRef, "webhook:") || c.LegacyRef == "forecast-slack" {
		maxAttempts = 3
	}
	if retryable && d.Attempts+1 < maxAttempts {
		delay := min(15*time.Minute, 5*time.Second*time.Duration(1<<min(d.Attempts, 8)))
		// Per-delivery deterministic jitter avoids synchronized retry storms and
		// remains reproducible without sleeping a worker or holding a DB lock.
		sum := sha256.Sum256([]byte(d.ID))
		delay += time.Duration(sum[0]) * delay / 1024
		delay = max(delay, after)
		d.NextAt = time.Now().Add(delay).UnixMilli()
		return finish("retry_wait", "destination_retry", status)
	}
	return finish("failed", "destination_rejected", status)
}

func (s *Server) recordNotificationCompatibility(c NotificationChannel, state, reason string) {
	channel := ""
	switch c.LegacyRef {
	case "forecast-email":
		channel = "email"
	case "forecast-slack":
		channel = "slack"
	default:
		return
	}
	s.st.updateForecastAlertState(func(rec *forecastAlertRecord) {
		rec.Channels[channel] = forecastChannelResult{At: time.Now().UTC().Format(time.RFC3339), OK: state == "accepted", Error: reason}
	})
}

// Routing is an audience filter, never an authorization grant. Delivery and inbox
// reads independently intersect these candidates with current resource access.
func (s *Server) notificationRouteAllows(routeID, username string) bool {
	principals := []string{userPrincipal(username)}
	for _, id := range s.st.groupChain(username) {
		principals = append(principals, groupPrincipal(id))
	}
	for _, principal := range principals {
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM notification_route_grants WHERE route_id=? AND principal=?`, routeID, principal).Scan(&n); err != nil {
			return false
		} else if n > 0 {
			return true
		}
	}
	return false
}

func (s *Server) attemptNotification(ctx context.Context, c NotificationChannel, cfg notificationChannelConfig, event NotificationEvent) (accepted, uncertain, retryable bool, status int, after time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	subject, text := notificationMessage(event)
	if c.Adapter == "email" {
		if len(cfg.Recipients) == 0 {
			return false, false, false, 0, 0
		}
		for _, r := range cfg.Recipients {
			a, err := mail.ParseAddress(r)
			if err != nil || a.Address != r {
				return false, false, false, 0, 0
			}
		}
		body := "<p>" + strings.ReplaceAll(html.EscapeString(text), "\n", "<br>") + "</p>"
		if s.mailFn != nil {
			err := s.mailFn(cfg.Recipients, s.brandName()+" — "+subject, body)
			return err == nil, false, false, 0, 0
		}
		if !s.emailEnabled() {
			return false, false, false, 0, 0
		}
		mc := cfg.smtp
		if mc == nil {
			snapshot := s.mailConfig()
			mc = &snapshot
		}
		result, err := mc.Attempt(ctx, cfg.Recipients, s.brandName()+" — "+subject, body)
		var smtpErr *textproto.Error
		transient := errors.As(err, &smtpErr) && smtpErr.Code >= 400 && smtpErr.Code < 500
		return result.Accepted, result.Uncertain, transient, 0, 0
	}
	var body []byte
	if c.Adapter == "slack" {
		if _, err := s.slackURLAllowed(cfg.URL); err != nil {
			return false, false, false, 0, 0
		}
		// Escape text so report-controlled strings cannot inject channel mentions.
		body, _ = json.Marshal(map[string]string{"text": strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)})
	} else if c.Adapter == "webhook" {
		body = []byte(event.Payload)
	} else {
		return false, false, false, 0, 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return false, false, false, 0, 0
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Adapter == "webhook" {
		req.Header.Set(webhook.EventHeader, event.Type)
		req.Header.Set("X-Report-Portal-Event-ID", event.ID)
		if cfg.deliveryID != "" {
			req.Header.Set("X-Report-Portal-Delivery-ID", cfg.deliveryID)
		}
		if cfg.signingSecret != "" {
			req.Header.Set(webhook.SignatureHeader, "sha256="+webhook.Sign(cfg.signingSecret, body))
		}
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if strings.HasPrefix(c.LegacyRef, "webhook:") {
		client.CheckRedirect = nil
	}
	result := notification.AttemptHTTP(client, req)
	if strings.HasPrefix(c.LegacyRef, "webhook:") {
		id, _ := strconv.ParseInt(strings.TrimPrefix(c.LegacyRef, "webhook:"), 10, 64)
		reason := ""
		if result.Err != nil {
			reason = "transport_error"
		}
		s.st.UpdateWebhookStatus(id, result.Status, reason)
	}
	return result.Accepted, result.Uncertain, result.Retryable, result.Status, result.RetryAfter
}

func notificationMessage(e NotificationEvent) (string, string) {
	var data map[string]any
	decoder := json.NewDecoder(strings.NewReader(e.Payload))
	decoder.UseNumber()
	_ = decoder.Decode(&data)
	if e.ResourceType == "extension" {
		return "[" + e.ResourceID + "] " + fmt.Sprint(data["title"]), fmt.Sprint(data["severity"]) + "\n" + fmt.Sprint(data["message"])
	}
	switch e.Type {
	case "notification.test":
		kind, _ := data["sample_type"].(string)
		if notificationSelects(strings.Join(availableEvents(), ","), kind) || (strings.HasPrefix(kind, "extension.") && strings.HasSuffix(kind, ".notice")) {
			body, _ := json.Marshal(data["sample"])
			sample := NotificationEvent{Type: kind, Payload: string(body)}
			if strings.HasPrefix(kind, "extension.") && strings.HasSuffix(kind, ".notice") {
				sample.ResourceType = "extension"
				sample.ResourceID = strings.TrimSuffix(strings.TrimPrefix(kind, "extension."), ".notice")
			}
			_, text := notificationMessage(sample)
			return "Notification channel test", text
		}
		return "Notification channel test", "This is a test notification."
	case EventBatchFinished:
		return "Job completed", fmt.Sprintf("Job #%v completed (%v).\nSucceeded: %v; failed: %v; total: %v.", data["job_id"], data["status"], data["succeeded"], data["failed"], data["total"])
	case EventReportIngested:
		return "Report available", fmt.Sprintf("%v %v\n%v\nReport: %v", data["symbol"], data["name"], data["title"], data["id"])
	default:
		reason, _ := data["reason"].(string)
		if e.Type == EventPredictionsStoreFailed {
			reason = "store_failed"
		}
		return e.Type, forecastAlertText(e.Type, reason, data)
	}
}
