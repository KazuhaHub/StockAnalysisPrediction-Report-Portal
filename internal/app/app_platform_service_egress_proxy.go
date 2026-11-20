package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The forward proxy on the egress session (doc 184): the capability question before a session carries a
// policy, and the audit of what the runner reports about the installation's tunnels.

const (
	platformProxyAuditRollup  = "app_platform.proxy_rollup"
	platformProxyAuditRefused = "app_platform.proxy_refused"
	platformProxyAuditUsage   = "app_platform.proxy_usage"

	// platformProxyRefusalInterval is the least time between two refusal rows of one installation, host and
	// code. The runner coalesces to the same rate; Portal does not rely on it.
	platformProxyRefusalInterval = time.Minute
	// platformProxyRefusalRows bounds the refusal rows of one installation in any interval, and
	// platformProxyRefusalInstallations the installations the limiter keeps state for.
	platformProxyRefusalRows          = 64
	platformProxyRefusalInstallations = 1024
)

// runnerServesProxy asks the runner whether it serves the proxy with the limits this Portal carries. A runner
// that predates the query, or answers other limits, does not; any other failure to ask is an error, so that a
// transient failure never silently drops a granted proxy from a session.
func (c *platformServiceEgressCoordinator) runnerServesProxy(ctx context.Context, endpoint platformRuntimeEndpoint) (bool, error) {
	profile, err := c.transport.QueryProxy(ctx, endpoint)
	switch {
	case errors.Is(err, transport.ErrProxyUnsupported):
		return false, nil
	case err != nil:
		return false, err
	}
	return profile == transport.NewProxyProfile(), nil
}

// platformProxyRefusalLimiter keeps at most one refusal row per installation, host and code per interval, and
// at most platformProxyRefusalRows rows per installation in any interval, whatever the hosts: the host is text
// the application chose, so neither its variety nor the other installations' rows may be at its mercy. Every
// installation has its own state.
type platformProxyRefusalLimiter struct {
	mu            sync.Mutex
	installations map[string]*platformProxyRefusalState
}

type platformProxyRefusalState struct {
	last map[string]time.Time
	rows []time.Time
}

func (l *platformProxyRefusalLimiter) allow(installation, host, code string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.installations == nil {
		l.installations = map[string]*platformProxyRefusalState{}
	}
	st := l.installations[installation]
	if st == nil {
		if len(l.installations) >= platformProxyRefusalInstallations {
			for id, other := range l.installations {
				if len(other.rows) == 0 || now.Sub(other.rows[len(other.rows)-1]) >= platformProxyRefusalInterval {
					delete(l.installations, id)
				}
			}
			if len(l.installations) >= platformProxyRefusalInstallations {
				return false
			}
		}
		st = &platformProxyRefusalState{last: map[string]time.Time{}}
		l.installations[installation] = st
	}
	for len(st.rows) > 0 && now.Sub(st.rows[0]) >= platformProxyRefusalInterval {
		st.rows = st.rows[1:]
	}
	key := host + "\x00" + code
	if at, ok := st.last[key]; ok && now.Sub(at) < platformProxyRefusalInterval {
		return false
	}
	if len(st.rows) >= platformProxyRefusalRows {
		return false
	}
	if len(st.last) >= 2*platformProxyRefusalRows {
		for k, at := range st.last {
			if now.Sub(at) >= platformProxyRefusalInterval {
				delete(st.last, k)
			}
		}
	}
	st.last[key] = now
	st.rows = append(st.rows, now)
	return true
}

// serveProxyReport writes what the runner reported as audit rows. Only a session that carries the proxy may
// report, and a report that is not exactly canonical is dropped: nothing a report says is authority, it is
// only what the console shows.
func (c *platformServiceEgressCoordinator) serveProxyReport(l *platformServiceEgressLive, payload []byte) {
	if !slices.Contains(l.state.resources, platformServiceEgressProxy) {
		return
	}
	report, err := transport.DecodeEgressProxyReport(payload)
	if err != nil {
		return
	}
	installation := l.state.pin.installation
	now := c.now()
	switch report.Kind {
	case broker.ProxyReportRollup:
		for _, h := range report.Hosts {
			c.auditLog(platformProxyAuditRollup, installation, map[string]any{
				"session_id": l.state.sessionID, "hour": report.Hour, "host": h.Host, "tunnels": h.Tunnels,
				"bytes_in": h.BytesIn, "bytes_out": h.BytesOut, "closes": h.Closes,
			})
		}
	case broker.ProxyReportRefusal:
		if c.refusals.allow(installation, report.Host, report.Code, now) {
			c.auditLog(platformProxyAuditRefused, installation, map[string]any{
				"session_id": l.state.sessionID, "host": report.Host, "code": report.Code, "count": report.Count,
			})
		}
	case broker.ProxyReportUsage:
		c.auditLog(platformProxyAuditUsage, installation, map[string]any{
			"session_id": l.state.sessionID, "day": report.Day, "bytes": report.Bytes, "budget": report.Budget,
		})
	}
}
