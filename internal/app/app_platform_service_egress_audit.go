package app

import (
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Audit of background egress (doc 174 section 5). Every row has actor `system`
// and target type `app_platform_installation`:
//
//	app_platform.service_egress_opened   session, replaced session, resources, tuple, concurrency
//	app_platform.service_egress_closed   session and the reason
//	app_platform.service_egress_summary  one per installation per 10 minutes, from the runner's outcome frames
//	app_platform.service_egress_denied   at most 10 per installation in any 10 minutes, then counted
//
// Limits, as far as they go without a schema: the denied cap slides (it holds
// across the end of a summary window) and is seeded from the audit log, so it
// holds across a restart. The summary windows are in memory and start at an
// installation's first call; shutdown flushes them, a crash loses at most the
// open window's summary (never a decision), and a restart starts a new window.
//
// A row never carries a path, a query, a body or a header: a denied call names
// its method and origin only.

const (
	platformServiceEgressAuditOpened  = "app_platform.service_egress_opened"
	platformServiceEgressAuditClosed  = "app_platform.service_egress_closed"
	platformServiceEgressAuditSummary = "app_platform.service_egress_summary"
	platformServiceEgressAuditDenied  = "app_platform.service_egress_denied"

	platformServiceEgressSummaryWindow   = 10 * time.Minute
	platformServiceEgressDeniedPerWindow = 10
	// platformServiceEgressAuditWindows bounds the installations a coordinator
	// keeps counters for.
	platformServiceEgressAuditWindows = 1024
	platformServiceEgressMaxOrigins   = 32
)

var platformServiceEgressReasonNames = map[byte]string{
	transport.EgressCloseExpired: "expired", transport.EgressCloseRenewed: "renewed",
	transport.EgressCloseDrift: "drift", transport.EgressCloseRestorePause: "restore_pause",
	transport.EgressCloseEpoch: "epoch", transport.EgressCloseDisabled: "disabled",
	transport.EgressCloseConnectionLost: "connection_lost", transport.EgressCloseRefused: "refused",
	transport.EgressCloseShutdown: "shutdown",
}

func platformServiceEgressReasonByte(name string) byte {
	for b, n := range platformServiceEgressReasonNames {
		if n == name {
			return b
		}
	}
	return transport.EgressCloseDrift
}

// platformServiceEgressOrigin is scheme and host of a target, nothing else.
func platformServiceEgressOrigin(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

var platformServiceEgressClassNames = map[byte]string{
	transport.EgressOutcomeOK: "ok", transport.EgressOutcomeDenied: "denied",
	transport.EgressOutcomeUnavailable: "unavailable", transport.EgressOutcomeUncertain: "uncertain",
	transport.EgressOutcomeBusy: "busy",
}

// platformServiceEgressWindow is what one installation did in the open window.
type platformServiceEgressWindow struct {
	start             time.Time
	session           string
	counts            map[string]int
	notices           int
	bytesOut, bytesIn int64
	origins           map[string]bool
	deniedSuppressed  int
}

// platformServiceEgressAggregator turns the per-call facts into one summary row
// per installation per window and a bounded number of denied rows. It is
// in-memory: a window lost to a crash is a lost summary, never a lost decision.
type platformServiceEgressAggregator struct {
	mu      sync.Mutex
	windows map[string]*platformServiceEgressWindow
	// denied is when each installation's individual denied rows were written in
	// the last summary window (a sliding record, so the cap holds across the end
	// of a summary window); it is seeded once from the audit log, so it holds
	// across a restart too.
	denied map[string][]time.Time
}

func newPlatformServiceEgressAggregator() *platformServiceEgressAggregator {
	return &platformServiceEgressAggregator{windows: map[string]*platformServiceEgressWindow{}, denied: map[string][]time.Time{}}
}

func (a *platformServiceEgressAggregator) seeded(installation string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.denied[installation]
	return ok
}

// seed records the denied rows an earlier coordinator already wrote.
func (a *platformServiceEgressAggregator) seed(installation string, written []time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.denied[installation]; !ok {
		if len(a.denied) >= platformServiceEgressAuditWindows {
			return
		}
		a.denied[installation] = append([]time.Time{}, written...)
	}
}

// recentPlatformServiceEgressDenied reads when the denied rows of the last
// window were written. A failed read seeds nothing (the cap then counts from
// this process only).
func (s *Server) recentPlatformServiceEgressDenied(installation string, now time.Time) []time.Time {
	rows, err := s.st.query(`SELECT at FROM audit_log WHERE action=? AND target_type='app_platform_installation' AND target_id=? AND actor='system' AND at>? ORDER BY id`,
		platformServiceEgressAuditDenied, installation, now.Add(-platformServiceEgressSummaryWindow).UTC().Format(time.RFC3339))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var written []time.Time
	for rows.Next() {
		var at string
		if rows.Scan(&at) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			written = append(written, t)
		}
	}
	return written
}

func (a *platformServiceEgressAggregator) window(installation, session string, now time.Time) *platformServiceEgressWindow {
	w := a.windows[installation]
	if w == nil {
		if len(a.windows) >= platformServiceEgressAuditWindows {
			return nil
		}
		w = &platformServiceEgressWindow{start: now, counts: map[string]int{}, origins: map[string]bool{}}
		a.windows[installation] = w
	}
	w.session = session
	return w
}

// recordCall counts one finished HTTP call. bytesOut is the request body the
// app sent, bytesIn the response bytes the runner released.
func (a *platformServiceEgressAggregator) recordCall(installation, session string, now time.Time, class byte, origin string, bytesOut int, bytesIn uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.window(installation, session, now)
	if w == nil {
		return
	}
	if name, ok := platformServiceEgressClassNames[class]; ok {
		w.counts[name]++
	}
	w.bytesOut += int64(bytesOut)
	w.bytesIn += int64(bytesIn)
	if origin != "" && (w.origins[origin] || len(w.origins) < platformServiceEgressMaxOrigins) {
		w.origins[origin] = true
	}
}

func (a *platformServiceEgressAggregator) recordNotice(installation, session string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w := a.window(installation, session, now); w != nil {
		w.notices++
	}
}

// admitDenied reports whether a denied row may still be written: at most
// platformServiceEgressDeniedPerWindow in any window of the summary length ending
// now. Past the cap the denial is only counted, and the summary says how many.
func (a *platformServiceEgressAggregator) admitDenied(installation, session string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.window(installation, session, now)
	if w == nil {
		return false
	}
	recent := slices.DeleteFunc(a.denied[installation], func(at time.Time) bool {
		return !at.After(now.Add(-platformServiceEgressSummaryWindow))
	})
	if len(recent) >= platformServiceEgressDeniedPerWindow {
		a.denied[installation] = recent
		w.deniedSuppressed++
		return false
	}
	a.denied[installation] = append(recent, now)
	return true
}

type platformServiceEgressSummaryRow struct {
	installation string
	detail       []byte
}

// due removes and returns the summaries of the windows that are over (all of
// them when flush is set). A window with nothing in it writes nothing.
func (a *platformServiceEgressAggregator) due(now time.Time, flush bool) []platformServiceEgressSummaryRow {
	a.mu.Lock()
	defer a.mu.Unlock()
	var rows []platformServiceEgressSummaryRow
	for installation, w := range a.windows {
		if !flush && now.Sub(w.start) < platformServiceEgressSummaryWindow {
			continue
		}
		delete(a.windows, installation)
		if len(w.counts) == 0 && w.notices == 0 && w.deniedSuppressed == 0 {
			continue
		}
		origins := make([]string, 0, len(w.origins))
		for origin := range w.origins {
			origins = append(origins, origin)
		}
		sort.Strings(origins)
		counts := map[string]int{"ok": 0, "denied": 0, "unavailable": 0, "uncertain": 0, "busy": 0}
		for name, n := range w.counts {
			counts[name] = n
		}
		detail, err := json.Marshal(map[string]any{
			"session_id": w.session, "window_start_ms": w.start.UnixMilli(), "window_end_ms": now.UnixMilli(),
			"counts": counts, "notices": w.notices, "bytes_out": w.bytesOut, "bytes_in": w.bytesIn,
			"origins": origins, "denied_suppressed": w.deniedSuppressed,
		})
		if err == nil {
			rows = append(rows, platformServiceEgressSummaryRow{installation: installation, detail: detail})
		}
	}
	slices.SortFunc(rows, func(x, y platformServiceEgressSummaryRow) int {
		switch {
		case x.installation < y.installation:
			return -1
		case x.installation > y.installation:
			return 1
		}
		return 0
	})
	return rows
}

// writePlatformServiceEgressAudit appends one row. A failure to audit is
// returned to be logged; it never decides a call.
func (s *Server) writePlatformServiceEgressAudit(action, installation string, at time.Time, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return s.writePlatformServiceEgressAuditRaw(action, installation, at, raw)
}

func (s *Server) writePlatformServiceEgressAuditRaw(action, installation string, at time.Time, detail []byte) error {
	_, err := s.st.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`,
		at.UTC().Format(time.RFC3339), action, installation, string(detail))
	return err
}
