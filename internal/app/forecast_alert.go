package app

// Forecast verification: alert delivery (ADR 0029 §6, plan §6.2 / V18).
//
// The webhook events are the extension surface: signed JSON, one shape for every subscriber. They
// cannot reach Slack, whose incoming webhook wants a {"text"} body, and they cannot reach a mailbox
// at all. This file is the portal's own two channels beside them.
//
// Two rules are deliberate:
//
//   - The webhook is fired by the PRODUCER (the watchdog, the ingest branch), never here. If this
//     function also fired it, one event would be delivered twice.
//   - The Slack URL is a write-only secret with a closed host list. It is stored so the portal can
//     POST to it, and never returned; and a URL that is not https on hooks.slack.com is refused, so a
//     later settings write cannot turn the portal into a request forwarder.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
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

const (
	slackTimeout  = 10 * time.Second
	slackAttempts = 3 // the first try and two retries
)

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

// slackPost delivers one message. Transport errors and 5xx retry twice; a 4xx is final, because a
// revoked or malformed webhook will not start working on the second attempt.
func (s *Server) slackPost(ctx context.Context, raw, text string) error {
	u, err := s.slackURLAllowed(raw)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: slackTimeout}
	var last error
	for attempt := 0; attempt < slackAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// url.Error carries the full URL, which is the secret. The stored/displayed error must
			// never contain it.
			last = errors.New(strings.ReplaceAll(err.Error(), raw, "<slack-webhook>"))
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("slack returned HTTP %d", resp.StatusCode)
		if resp.StatusCode < 500 {
			return last
		}
	}
	return last
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
// The returned map always has an entry per channel: nil for delivered, the error otherwise. It never
// returns an error itself, because a failing alert must not fail the operation that produced it; the
// outcome is recorded in `forecast_alert_state` and shown on the page.
func (s *Server) forecastAlert(kind, reason string, detail map[string]any) map[string]error {
	text := forecastAlertText(kind, reason, detail)
	subject := fmt.Sprintf("%s — %s", s.brandName(), kind)
	out := map[string]error{"email": nil, "slack": nil}

	if to := alertRecipients(s.st.GetSetting(setForecastAlertEmailTo, "")); len(to) == 0 {
		out["email"] = errors.New("no email recipients configured")
	} else if err := s.sendEmail(to, subject, "<p>"+strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")+"</p>"); err != nil {
		out["email"] = err
	}

	raw := strings.TrimSpace(s.st.GetSetting(setForecastAlertSlackURL, ""))
	if raw == "" {
		out["slack"] = errors.New("no Slack webhook configured")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), slackTimeout*time.Duration(slackAttempts)+3*time.Second)
		defer cancel()
		out["slack"] = s.slackPost(ctx, raw, text)
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
	if out["email"] != nil || out["slack"] != nil {
		log.Printf("forecast alert %s/%s: email=%v slack=%v", kind, reason, out["email"], out["slack"])
	}
	return out
}

// forecastAlertsView is the GET shape: configuration plus the last delivery per channel. The Slack
// URL itself is never included.
func (s *Server) forecastAlertsView() map[string]any {
	rec := s.st.forecastAlertState()
	last := map[string]forecastChannelResult{}
	for k, v := range rec.Channels {
		last[k] = v
	}
	return map[string]any{
		"email_to":   s.st.GetSetting(setForecastAlertEmailTo, ""),
		"has_slack":  strings.TrimSpace(s.st.GetSetting(setForecastAlertSlackURL, "")) != "",
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
