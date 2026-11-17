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
//	app_platform.service_egress_denied   at most 10 per installation per 10 minutes, then counted
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
	deniedWritten     int
	deniedSuppressed  int
}

// platformServiceEgressAggregator turns the per-call facts into one summary row
// per installation per window and a bounded number of denied rows. It is
// in-memory: a window lost to a crash is a lost summary, never a lost decision.
type platformServiceEgressAggregator struct {
	mu      sync.Mutex
	windows map[string]*platformServiceEgressWindow
}

func newPlatformServiceEgressAggregator() *platformServiceEgressAggregator {
	return &platformServiceEgressAggregator{windows: map[string]*platformServiceEgressWindow{}}
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

// admitDenied reports whether a denied row may still be written in this
// window; past the cap the denial is only counted, and the summary says how many.
func (a *platformServiceEgressAggregator) admitDenied(installation, session string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.window(installation, session, now)
	if w == nil {
		return false
	}
	if w.deniedWritten >= platformServiceEgressDeniedPerWindow {
		w.deniedSuppressed++
		return false
	}
	w.deniedWritten++
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
