package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	portalmail "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/mail"
)

const notificationConfigRevision = "notification_config_revision"

type notificationSettingsSnapshot struct {
	values      map[string]string
	fingerprint string
	// forecastConfigured lists the forecast alert channels ("email", "slack") that have a destination;
	// forecastActive, those of them that are also enabled and not paused. A test alert goes to exactly
	// the active ones, and readiness item 5 needs at least one: forecastChannelsReady.
	forecastConfigured    []string
	forecastActive        []string
	forecastChannelsReady bool
}

var notificationLegacySettingDefaults = map[string]string{
	"smtp_enabled": "0", "smtp_host": "", "smtp_port": "587", "smtp_user": "", "smtp_pass": "", "smtp_from": "", "smtp_security": "starttls",
	setForecastAlertEmailTo: "", setForecastAlertSlackURL: "", notificationConfigRevision: "0",
	"notification_smtp_revision": "0", "notification_forecast_email_revision": "0", "notification_forecast_slack_revision": "0",
}

// All notification configuration writers share a short transaction lock. This
// also makes test-result stamping atomic with its final revision comparison.
// Network I/O must never run inside the callback.
func (s *Store) withNotificationConfig(fn func(migExec) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	if s.driver == "postgres" {
		if _, err := e.exec(`SELECT pg_advisory_xact_lock(780716)`); err != nil {
			return err
		}
	}
	if err := fn(e); err != nil {
		return err
	}
	return tx.Commit()
}

func notificationSettingsInTx(e migExec) (notificationSettingsSnapshot, error) {
	ss := notificationSettingsSnapshot{values: make(map[string]string, len(notificationLegacySettingDefaults))}
	for k, fallback := range notificationLegacySettingDefaults {
		var value sql.NullString
		err := e.queryRow(`SELECT v FROM meta WHERE k=?`, k).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ss, err
		}
		ss.values[k] = fallback
		if err == nil && value.Valid {
			ss.values[k] = value.String
		}
	}
	for _, ref := range []string{"forecast-email", "forecast-slack"} {
		var revision int64
		var enabled, paused int
		var config string
		err := e.queryRow(`SELECT revision,enabled,paused,config FROM notification_channels WHERE id=?`, "legacy:"+ref).Scan(&revision, &enabled, &paused, &config)
		if errors.Is(err, sql.ErrNoRows) {
			revision, enabled, paused = 1, 1, 0
		} else if err != nil {
			return ss, err
		}
		if config != "" {
			var cfg notificationChannelConfig
			if err := json.Unmarshal([]byte(config), &cfg); err != nil {
				return ss, err
			}
			if cfg.DeliveryRevision > 0 {
				revision = cfg.DeliveryRevision
			}
		}
		ss.values["channel:"+ref] = fmt.Sprintf("%d:%d:%d", revision, enabled, paused)
		configured := false
		switch ref {
		case "forecast-email":
			configured = len(alertRecipients(ss.values[setForecastAlertEmailTo])) > 0
		case "forecast-slack":
			configured = strings.TrimSpace(ss.values[setForecastAlertSlackURL]) != ""
		}
		channel := strings.TrimPrefix(ref, "forecast-")
		if configured {
			ss.forecastConfigured = append(ss.forecastConfigured, channel)
		}
		if configured && enabled != 0 && paused == 0 {
			ss.forecastActive = append(ss.forecastActive, channel)
		}
	}
	ss.forecastChannelsReady = len(ss.forecastActive) > 0
	body, _ := json.Marshal(ss.values)
	digest := sha256.Sum256(body)
	ss.fingerprint = hex.EncodeToString(digest[:])
	return ss, nil
}

func (s *Store) notificationSettings() (notificationSettingsSnapshot, error) {
	var ss notificationSettingsSnapshot
	err := s.withNotificationConfig(func(e migExec) error { var err error; ss, err = notificationSettingsInTx(e); return err })
	return ss, err
}

func (ss notificationSettingsSnapshot) mailConfig() portalmail.Config {
	port, _ := strconv.Atoi(ss.values["smtp_port"])
	return portalmail.Config{Host: ss.values["smtp_host"], Port: port, User: ss.values["smtp_user"], Pass: ss.values["smtp_pass"], From: ss.values["smtp_from"], Security: ss.values["smtp_security"]}
}

func (ss notificationSettingsSnapshot) channelFingerprint(ref string) string {
	keys := []string{}
	if ref == "smtp" || ref == "forecast-email" {
		keys = append(keys, "smtp_enabled", "smtp_host", "smtp_port", "smtp_user", "smtp_pass", "smtp_from", "smtp_security", "notification_smtp_revision")
	}
	if ref == "forecast-email" {
		keys = append(keys, setForecastAlertEmailTo, "notification_forecast_email_revision")
	}
	if ref == "forecast-slack" {
		keys = append(keys, setForecastAlertSlackURL, "notification_forecast_slack_revision")
	}
	values := map[string]string{}
	for _, k := range keys {
		values[k] = ss.values[k]
	}
	body, _ := json.Marshal(values)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// force preserves the SMTP form's historical invalidation even on a no-op save.
// Forecast settings otherwise invalidate only when a destination actually changes.
func (s *Store) saveNotificationSettings(values map[string]string, force bool) (bool, error) {
	changed := false
	err := s.withNotificationConfig(func(e migExec) error {
		var err error
		changed, err = saveNotificationSettingsInTx(e, values, force)
		return err
	})
	return changed, err
}

func saveNotificationSettingsInTx(e migExec, values map[string]string, force bool) (bool, error) {
	changed := false
	err := func() error {
		ss, err := notificationSettingsInTx(e)
		if err != nil {
			return err
		}
		for k, value := range values {
			if _, ok := notificationLegacySettingDefaults[k]; !ok || strings.HasPrefix(k, "notification_") {
				return errNotificationConflict
			}
			if ss.values[k] != value {
				changed = true
			}
		}
		if !changed && !force {
			return nil
		}
		revisionKeys := map[string]bool{notificationConfigRevision: true}
		if force {
			revisionKeys["notification_smtp_revision"] = true
		}
		for k, value := range values {
			if ss.values[k] == value {
				continue
			}
			switch k {
			case setForecastAlertEmailTo:
				revisionKeys["notification_forecast_email_revision"] = true
			case setForecastAlertSlackURL:
				revisionKeys["notification_forecast_slack_revision"] = true
			default:
				revisionKeys["notification_smtp_revision"] = true
			}
		}
		for k, value := range values {
			if err := e.setSetting(k, value); err != nil {
				return err
			}
		}
		for key := range revisionKeys {
			revision, err := strconv.ParseUint(ss.values[key], 10, 64)
			if err != nil || revision == ^uint64(0) {
				return errNotificationConflict
			}
			if err := e.setSetting(key, strconv.FormatUint(revision+1, 10)); err != nil {
				return err
			}
		}
		_, err = e.exec(`DELETE FROM meta WHERE k=?`, setForecastAlertTestOKAt)
		return err
	}()
	return changed, err
}

func (s *Store) stampForecastNotificationTest(expected notificationSettingsSnapshot) error {
	return s.withNotificationConfig(func(e migExec) error {
		current, err := notificationSettingsInTx(e)
		if err != nil {
			return err
		}
		if current.fingerprint != expected.fingerprint {
			return errNotificationConflict
		}
		if !current.forecastChannelsReady {
			return errNotificationConflict
		}
		return e.setSetting(setForecastAlertTestOKAt, time.Now().UTC().Format(time.RFC3339))
	})
}
