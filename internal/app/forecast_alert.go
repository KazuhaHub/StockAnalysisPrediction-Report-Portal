package app

// Forecast verification: alert delivery (ADR 0029 §6, plan §6.2 / V18).
//
// Producers capture one durable event for raw Webhooks, Email and Slack. This
// file retains message formatting, host validation and explicit test delivery;
// it never republishes a business event or owns an automatic retry loop.
// Slack destinations remain write-only and restricted to the approved host.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	setForecastAlertEmailTo  = "forecast_alert_email_to"  // comma-separated addresses
	setForecastAlertSlackURL = "forecast_alert_slack_url" // write-only secret, never returned
)

// slackHost is the only host a Slack webhook may point at. A test may add one loopback host through
// Server.slackHostForTest, which is empty in production.
const slackHost = "hooks.slack.com"

var errSlackURL = errors.New("a Slack webhook URL must be https on hooks.slack.com")

// slackURLAllowed validates a Slack webhook URL and returns it parsed.
func (s *Server) slackURLAllowed(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errSlackURL
	}
	if u.User != nil || u.Hostname() == "" {
		return nil, errSlackURL
	}
	host := strings.ToLower(u.Hostname())
	testLoopback := s.slackHostForTest != "" && host == strings.ToLower(s.slackHostForTest)
	switch {
	case u.Scheme == "https" && host == slackHost:
		return u, nil
	case testLoopback && (u.Scheme == "https" || u.Scheme == "http"):
		return u, nil
	default:
		return nil, errSlackURL
	}
}

// forecastAlertText is the plain-text body every channel carries. The detail keys are sorted so two
// runs of the same event read identically.
func forecastAlertText(kind, reason string, detail map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nreason: %s\n", kind, reason)
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, detail[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// alertRecipients parses the comma-separated recipient list. An invalid entry is dropped here; the
// settings handler is what refuses to store one.
func alertRecipients(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		a := strings.TrimSpace(part)
		if a == "" {
			continue
		}
		if parsed, err := mail.ParseAddress(a); err == nil {
			out = append(out, parsed.Address)
		}
	}
	return out
}

// forecastAlert sends one alert to every configured channel, best effort per channel.
//
// The returned map has an entry per configured channel: nil for delivered, the error otherwise. It never
// returns an error itself, because a failing alert must not fail the operation that produced it; the
// outcome is recorded in `forecast_alert_state` and shown on the page.
func (s *Server) forecastAlert(kind, reason string, detail map[string]any) map[string]error {
	snapshot, err := s.st.notificationSettings()
	if err != nil {
		return map[string]error{"email": errors.New("configuration_unavailable"), "slack": errors.New("configuration_unavailable")}
	}
	return s.forecastAlertSnapshot(context.Background(), snapshot, kind, reason, detail)
}

// Explicit tests use the same one-attempt adapters as workers, against a frozen
// configuration. They neither publish business events nor enter normal retry.
func (s *Server) forecastAlertSnapshot(ctx context.Context, snapshot notificationSettingsSnapshot, kind, reason string, detail map[string]any) map[string]error {
	payload := make(map[string]any, len(detail)+1)
	for k, v := range detail {
		payload[k] = v
	}
	payload["reason"] = reason
	body, _ := json.Marshal(payload)
	event := NotificationEvent{Type: kind, Payload: string(body)}
	mc := snapshot.mailConfig()
	out := make(map[string]error, len(snapshot.forecastActive))
	// Only the configured channels: an absent one is not a failed delivery, and recording it as one
	// would show a permanent failure for a channel the operator chose not to use.
	for _, channel := range snapshot.forecastActive {
		func() {
			defer func() {
				if recover() != nil {
					out[channel] = errors.New("adapter_panic")
				}
			}()
			c := NotificationChannel{Adapter: channel, LegacyRef: "forecast-" + channel}
			cfg := notificationChannelConfig{Recipients: alertRecipients(snapshot.values[setForecastAlertEmailTo]), URL: snapshot.values[setForecastAlertSlackURL], smtp: &mc}
			accepted, uncertain, _, _, _ := s.attemptNotification(ctx, c, cfg, event)
			switch {
			case accepted:
				out[channel] = nil
			case uncertain:
				out[channel] = errors.New("acceptance_unknown")
			default:
				out[channel] = errors.New("destination_rejected")
			}
		}()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.st.updateForecastAlertState(func(rec *forecastAlertRecord) {
		for channel, err := range out {
			res := forecastChannelResult{At: now, OK: err == nil}
			if err != nil {
				res.Error = err.Error()
			}
			rec.Channels[channel] = res
		}
	})
	return out
}

// forecastAlertsView is the GET shape: configuration plus the last delivery per channel. The Slack
// URL itself is never included. `configured` and `active` say which channels have a destination and
// which of those are enabled and unpaused, so the page can tell a channel that takes part in tests
// and alerts from one that does not, rather than showing an old delivery result for it.
func (s *Server) forecastAlertsView() map[string]any {
	rec := s.st.forecastAlertState()
	last := map[string]forecastChannelResult{}
	for k, v := range rec.Channels {
		last[k] = v
	}
	configured, active := []string{}, []string{}
	if snapshot, err := s.st.notificationSettings(); err == nil {
		configured = append(configured, snapshot.forecastConfigured...)
		active = append(active, snapshot.forecastActive...)
	}
	return map[string]any{
		"email_to":   s.st.GetSetting(setForecastAlertEmailTo, ""),
		"has_slack":  strings.TrimSpace(s.st.GetSetting(setForecastAlertSlackURL, "")) != "",
		"configured": configured,
		"active":     active,
		"last":       last,
		"test_ok_at": s.st.GetSetting(setForecastAlertTestOKAt, ""),
	}
}

// validAlertEmailList accepts a comma-separated list of bare addresses. An empty string clears the
// list, which is a valid state (and one the readiness item reports).
func validAlertEmailList(raw string) bool {
	for _, part := range strings.Split(raw, ",") {
		a := strings.TrimSpace(part)
		if a == "" {
			continue
		}
		parsed, err := mail.ParseAddress(a)
		if err != nil || parsed.Address != a {
			return false
		}
	}
	return true
}
