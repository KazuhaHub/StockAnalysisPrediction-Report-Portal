package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type notificationDestinationSecret struct {
	URL           string `json:"url"`
	SigningSecret string `json:"signing_secret,omitempty"`
}

type notificationChannelView struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	Adapter          string   `json:"adapter"`
	LegacyRef        string   `json:"legacy_ref"`
	Enabled          bool     `json:"enabled"`
	Paused           bool     `json:"paused"`
	Revision         int64    `json:"revision"`
	Recipients       []string `json:"recipients"`
	URLHost          string   `json:"url_host"`
	HasURL           bool     `json:"has_url"`
	HasSigningSecret bool     `json:"has_signing_secret"`
	Available        bool     `json:"available"`
	CreatedAt        string   `json:"created_at"`
}

func notificationTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func (s *Server) notificationChannelView(c NotificationChannel) notificationChannelView {
	v := notificationChannelView{ID: c.ID, Label: c.Label, Adapter: c.Adapter, LegacyRef: c.LegacyRef, Enabled: c.Enabled, Paused: c.Paused, Revision: c.Revision, Recipients: []string{}, CreatedAt: notificationTime(c.CreatedAt)}
	cfg, _, err := s.effectiveNotificationChannel(c)
	v.Available = err == nil
	if err == nil || strings.HasPrefix(c.LegacyRef, "webhook:") {
		if cfg.Recipients != nil {
			v.Recipients = cfg.Recipients
		}
		v.HasURL = cfg.URL != ""
		v.HasSigningSecret = cfg.signingSecret != ""
		if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
			v.URLHost = u.Scheme + "://" + u.Host
		}
	}
	return v
}

func notificationAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotificationReview):
		jsonErrorCode(w, http.StatusConflict, "notification_review_required", "review possible duplicate delivery before replaying")
	case errors.Is(err, errNotificationConflict):
		jsonErrorCode(w, http.StatusConflict, "notification_conflict", "notification state changed; reload and try again")
	case errors.Is(err, errNotificationCapacity):
		jsonErrorCode(w, http.StatusTooManyRequests, "notification_capacity", "notification capacity unavailable")
	case errors.Is(err, sql.ErrNoRows):
		jsonErrorCode(w, http.StatusNotFound, "not_found", "notification record not found")
	default:
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "notification operation failed")
	}
}

// Legacy references are adopted without copying destination credentials. Listing
// performs metadata reconciliation only; it cannot trigger delivery.
func (s *Server) adoptNotificationChannels() error {
	for _, spec := range []struct{ ref, adapter, label string }{
		{"smtp", "email", "Job completion email"}, {"forecast-email", "email", "Forecast email"}, {"forecast-slack", "slack", "Forecast Slack"},
	} {
		if _, err := s.ensureLegacyNotificationChannel(spec.ref, spec.adapter, spec.label); err != nil {
			return err
		}
	}
	for _, w := range s.st.ListWebhooks() {
		if _, err := s.ensureLegacyNotificationChannel("webhook:"+itoa64(w.ID), "webhook", "Webhook #"+itoa64(w.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) apiNotifications(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.adoptNotificationChannels(); err != nil {
		notificationAPIError(w, err)
		return
	}
	channels, err := s.st.NotificationChannels()
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	views := make([]notificationChannelView, 0, len(channels))
	for _, c := range channels {
		views = append(views, s.notificationChannelView(c))
	}
	routes, err := s.st.NotificationRoutes()
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	principals := []map[string]string{}
	for _, u := range s.st.Users() {
		principals = append(principals, map[string]string{"principal": userPrincipal(u.Username), "label": u.Name(), "kind": "user"})
	}
	for _, g := range s.st.ListUserGroups() {
		principals = append(principals, map[string]string{"principal": groupPrincipal(g.ID), "label": g.Name, "kind": "group"})
	}
	token, counts, err := s.st.notificationQueueReview()
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	writeJSON(w, map[string]any{"channels": views, "routes": routes, "events": s.notificationEvents(), "principals": principals, "dispatch_paused": s.st.GetSetting("notification_dispatch_paused", "0") == "1", "review_token": token, "counts": counts, "retention": s.st.notificationRetention()})
}

func (s *Server) apiNotificationChannelSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Label              string    `json:"label"`
		Adapter            string    `json:"adapter"`
		Revision           int64     `json:"revision"`
		Recipients         *[]string `json:"recipients"`
		URL                string    `json:"url"`
		SigningSecret      string    `json:"signing_secret"`
		ClearDestination   bool      `json:"clear_destination"`
		ClearSigningSecret bool      `json:"clear_signing_secret"`
		Enabled            *bool     `json:"enabled"`
		Paused             *bool     `json:"paused"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid JSON")
		return
	}
	id := r.PathValue("id")
	c := NotificationChannel{ID: randToken(), Enabled: true, Config: `{}`, Revision: 1, CreatedAt: time.Now().UTC().UnixMilli()}
	creating := id == ""
	if !creating {
		var err error
		c, err = s.st.NotificationChannel(id)
		if err != nil {
			notificationAPIError(w, err)
			return
		}
		if in.Revision != c.Revision {
			notificationAPIError(w, errNotificationConflict)
			return
		}
		if in.Adapter != "" && in.Adapter != c.Adapter {
			jsonErrorCode(w, 400, "invalid_param", "channel adapter cannot change")
			return
		}
	} else {
		c.Adapter = in.Adapter
	}
	c.Label = strings.TrimSpace(in.Label)
	if c.Label == "" || len(c.Label) > 120 || (c.Adapter != "email" && c.Adapter != "slack" && c.Adapter != "webhook" && c.Adapter != "inbox") {
		jsonErrorCode(w, 400, "invalid_param", "invalid channel label or adapter")
		return
	}
	previousEnabled := c.Enabled
	previousPaused := c.Paused
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Paused != nil {
		c.Paused = *in.Paused
	}
	cfg := notificationChannelConfig{}
	if !creating {
		var err error
		cfg, _, err = s.effectiveNotificationChannel(c)
		if err != nil {
			repairable := false
			if strings.HasPrefix(c.LegacyRef, "webhook:") {
				id, parseErr := strconv.ParseInt(strings.TrimPrefix(c.LegacyRef, "webhook:"), 10, 64)
				if parseErr == nil {
					_, repairable = s.st.GetWebhook(id)
				}
			}
			if !repairable {
				jsonErrorCode(w, 400, "invalid_param", "channel destination is unavailable")
				return
			}
		}
	}
	oldURL, oldSigning, oldRecipients := cfg.URL, cfg.signingSecret, strings.Join(cfg.Recipients, "\x00")
	if in.Recipients != nil {
		if c.Adapter != "email" || (c.ID == "legacy:smtp") || len(*in.Recipients) > 100 {
			jsonErrorCode(w, 400, "invalid_param", "invalid email recipients")
			return
		}
		cfg.Recipients = []string{}
		seen := map[string]bool{}
		for _, recipient := range *in.Recipients {
			recipient = strings.TrimSpace(recipient)
			a, err := mail.ParseAddress(recipient)
			if err != nil || a.Address != recipient {
				jsonErrorCode(w, 400, "invalid_param", "invalid email recipient")
				return
			}
			if !seen[recipient] {
				cfg.Recipients = append(cfg.Recipients, recipient)
				seen[recipient] = true
			}
		}
	}
	if in.URL != "" {
		cfg.URL = strings.TrimSpace(in.URL)
	}
	if in.ClearDestination {
		cfg.URL = ""
	}
	if in.SigningSecret != "" {
		cfg.signingSecret = in.SigningSecret
	}
	if in.ClearSigningSecret {
		cfg.signingSecret = ""
	}
	if len(cfg.URL) > 4096 || len(cfg.signingSecret) > 1024 {
		jsonErrorCode(w, 400, "invalid_param", "destination exceeds size limit")
		return
	}
	if c.Adapter == "slack" || c.Adapter == "webhook" {
		if cfg.URL == "" && c.Enabled {
			jsonErrorCode(w, 400, "invalid_param", "an enabled channel requires a destination")
			return
		}
		if cfg.URL != "" && (creating || in.URL != "" || in.ClearDestination) {
			var err error
			if c.Adapter == "slack" {
				_, err = s.slackURLAllowed(cfg.URL)
			} else {
				err = validateNotificationWebhookURL(cfg.URL)
			}
			if err != nil {
				jsonErrorCode(w, 400, "invalid_param", "invalid destination URL")
				return
			}
		}
	} else if in.URL != "" || in.SigningSecret != "" || in.ClearDestination || in.ClearSigningSecret {
		jsonErrorCode(w, 400, "invalid_param", "destination fields do not apply to this adapter")
		return
	}
	if c.Adapter != "webhook" && (in.SigningSecret != "" || in.ClearSigningSecret) {
		jsonErrorCode(w, 400, "invalid_param", "signing applies only to raw webhooks")
		return
	}
	if creating && c.Adapter == "email" {
		c.LegacyRef = "smtp"
	}
	legacySettings := map[string]string{}
	if c.LegacyRef == "forecast-email" && in.Recipients != nil {
		legacySettings[setForecastAlertEmailTo] = strings.Join(cfg.Recipients, ",")
	}
	if c.LegacyRef == "forecast-slack" && (in.URL != "" || in.ClearDestination) {
		legacySettings[setForecastAlertSlackURL] = cfg.URL
	}
	if c.LegacyRef == "" && (c.Adapter == "slack" || c.Adapter == "webhook") && (creating || in.URL != "" || in.SigningSecret != "" || in.ClearDestination || in.ClearSigningSecret) {
		body, _ := json.Marshal(notificationDestinationSecret{URL: cfg.URL, SigningSecret: cfg.signingSecret})
		sealed, err := s.sealSecret("notification:"+c.ID, "destination", string(body))
		if err != nil {
			notificationAPIError(w, err)
			return
		}
		c.Secret = sealed
	}
	version := cfg.DeliveryRevision
	if version == 0 {
		version = c.Revision
	}
	transportChanged := previousEnabled != c.Enabled || oldURL != cfg.URL || oldSigning != cfg.signingSecret || oldRecipients != strings.Join(cfg.Recipients, "\x00")
	if !creating && transportChanged {
		version++
	}
	storedRecipients := cfg.Recipients
	if strings.HasPrefix(c.ID, "legacy:") {
		storedRecipients = nil
	}
	config, _ := json.Marshal(notificationChannelConfig{Recipients: storedRecipients, DeliveryRevision: version})
	c.Config = string(config)
	err := s.st.withNotificationConfig(func(e migExec) error {
		if creating {
			var n int
			if err := e.queryRow(`SELECT COUNT(*) FROM notification_channels`).Scan(&n); err != nil {
				return err
			}
			if n >= 100 {
				return errNotificationCapacity
			}
			_, err := e.exec(`INSERT INTO notification_channels(id,label,adapter,legacy_ref,config,secret,enabled,paused,revision,created_at) VALUES(?,?,?,?,?,?,?,?,1,?)`, c.ID, c.Label, c.Adapter, c.LegacyRef, c.Config, c.Secret, boolInt(c.Enabled), boolInt(c.Paused), c.CreatedAt)
			return err
		}
		res, err := e.exec(`UPDATE notification_channels SET label=?,config=?,secret=?,enabled=?,paused=?,revision=revision+1 WHERE id=? AND revision=?`, c.Label, c.Config, c.Secret, boolInt(c.Enabled), boolInt(c.Paused), c.ID, in.Revision)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errNotificationConflict
		}
		if len(legacySettings) > 0 {
			if _, err := saveNotificationSettingsInTx(e, legacySettings, false); err != nil {
				return err
			}
		}
		if (c.LegacyRef == "forecast-email" || c.LegacyRef == "forecast-slack") && (transportChanged || previousPaused != c.Paused) {
			if _, err := e.exec(`DELETE FROM meta WHERE k=?`, setForecastAlertTestOKAt); err != nil {
				return err
			}
		}
		if strings.HasPrefix(c.LegacyRef, "webhook:") {
			wid, err := strconv.ParseInt(strings.TrimPrefix(c.LegacyRef, "webhook:"), 10, 64)
			if err != nil {
				return err
			}
			res, err := e.exec(`UPDATE webhooks SET url=?,secret=?,active=? WHERE id=?`, cfg.URL, cfg.signingSecret, boolInt(c.Enabled), wid)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return errNotificationConflict
			}
		}
		return nil
	})
	if err != nil {
		notificationAPIError(w, err)
		return
	}
	if !creating {
		c.Revision++
	}
	s.recordChange(r, user, AuditNotificationChange, "notification_channel", c.ID, map[string]any{"adapter": c.Adapter, "revision": c.Revision})
	writeJSON(w, map[string]any{"ok": true, "channel": s.notificationChannelView(c)})
}

func validateNotificationWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("invalid webhook destination")
	}
	return nil
}

func (s *Server) apiNotificationRouteSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Label         string   `json:"label"`
		ChannelID     string   `json:"channel_id"`
		Events        []string `json:"events"`
		RecipientMode string   `json:"recipient_mode"`
		Enabled       bool     `json:"enabled"`
		Mandatory     bool     `json:"mandatory"`
		Grants        []string `json:"grants"`
		Revision      int64    `json:"revision"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonErrorCode(w, 400, "invalid_param", "invalid JSON")
		return
	}
	if strings.TrimSpace(in.Label) == "" || len(in.Label) > 120 || len(in.Events) == 0 || len(in.Events) > len(s.notificationEvents()) || len(in.Grants) > 10000 {
		jsonErrorCode(w, 400, "invalid_param", "invalid notification rule")
		return
	}
	c, err := s.st.NotificationChannel(in.ChannelID)
	if err != nil || strings.HasPrefix(in.ChannelID, "legacy:") {
		jsonErrorCode(w, 400, "invalid_param", "select a managed channel")
		return
	}
	known := map[string]bool{}
	for _, kind := range s.notificationEvents() {
		known[kind] = true
	}
	seen := map[string]bool{}
	for _, kind := range in.Events {
		if !known[kind] || seen[kind] {
			jsonErrorCode(w, 400, "invalid_param", "invalid event selector")
			return
		}
		seen[kind] = true
	}
	personal := in.RecipientMode == "grant" || in.RecipientMode == "creator"
	if (!personal && in.RecipientMode != "destination") || (personal && c.Adapter != "email" && c.Adapter != "inbox") || (!personal && c.Adapter == "inbox") || (in.Mandatory && !personal) || (in.RecipientMode == "creator" && (len(in.Events) != 1 || in.Events[0] != EventBatchFinished)) {
		jsonErrorCode(w, 400, "invalid_param", "invalid recipient strategy")
		return
	}
	if in.RecipientMode != "grant" && len(in.Grants) > 0 {
		jsonErrorCode(w, 400, "invalid_param", "grants apply only to explicit audiences")
		return
	}
	grants := []string{}
	for _, p := range in.Grants {
		if strings.HasPrefix(p, "u:") {
			p = userPrincipal(strings.TrimPrefix(p, "u:"))
			if s.st.GetUser(strings.TrimPrefix(p, "u:")) == nil {
				jsonErrorCode(w, 400, "invalid_param", "unknown account principal")
				return
			}
		} else if strings.HasPrefix(p, "g:") {
			id, err := strconv.ParseInt(strings.TrimPrefix(p, "g:"), 10, 64)
			var n int
			if err != nil || id <= 0 || s.st.queryRow(`SELECT COUNT(*) FROM user_groups WHERE id=?`, id).Scan(&n) != nil || n != 1 {
				jsonErrorCode(w, 400, "invalid_param", "unknown OU principal")
				return
			}
			p = groupPrincipal(id)
		} else {
			jsonErrorCode(w, 400, "invalid_param", "invalid audience principal")
			return
		}
		grants = append(grants, p)
	}
	id := r.PathValue("id")
	if id == "" {
		if in.Revision != 0 {
			notificationAPIError(w, errNotificationConflict)
			return
		}
		id = randToken()
	} else if in.Revision <= 0 {
		notificationAPIError(w, errNotificationConflict)
		return
	}
	rule := NotificationRoute{ID: id, Label: strings.TrimSpace(in.Label), ChannelID: c.ID, Events: strings.Join(in.Events, ","), RecipientMode: in.RecipientMode, Enabled: in.Enabled, Mandatory: in.Mandatory, Grants: grants, Revision: in.Revision + 1}
	if err := s.st.SaveNotificationRoute(rule, in.Revision); err != nil {
		notificationAPIError(w, err)
		return
	}
	s.recordChange(r, user, AuditNotificationChange, "notification_rule", id, map[string]any{"revision": rule.Revision, "events": in.Events, "channel_id": c.ID})
	writeJSON(w, map[string]any{"ok": true, "route": rule})
}
