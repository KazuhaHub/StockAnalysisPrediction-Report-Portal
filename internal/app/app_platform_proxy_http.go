package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformProxyViewQueryTimeout bounds the question the console view puts to the runner.
const platformProxyViewQueryTimeout = 4 * time.Second

// The administrator routes of the proxy review and the console view (doc 184 sections 5.2 and 7). All three sit
// under the admin management route pattern, so a non-administrator, a cross-origin request or a body that is not
// closed JSON is refused before any plan is written.

type platformProxyPolicyView struct {
	Hosts []string `json:"hosts"`
	// Pins are the reviewed pinned destinations (doc 186), each marked with whether the runner's allowlist
	// currently holds the exact entry.
	Pins       []platformProxyPinView `json:"pins"`
	DailyBytes int64                  `json:"daily_bytes"`
}

type platformProxyPinView struct {
	Host      string `json:"host"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Permitted bool   `json:"permitted"`
}

// platformProxyRunnerView is what the runner says it can do for pins: the only choices of the review dialog.
// Available is false when the runner could not be asked or does not serve the proxy at all.
type platformProxyRunnerView struct {
	Available     bool                   `json:"available"`
	PinsSupported bool                   `json:"pins_supported"`
	Pins          []appplatform.ProxyPin `json:"pins"`
}

type platformProxyToday struct {
	Day      string `json:"day"`
	Bytes    int64  `json:"bytes"`
	Budget   int64  `json:"budget"`
	Reported bool   `json:"reported"`
}

type platformProxyRollupView struct {
	Hour     string           `json:"hour"`
	Host     string           `json:"host"`
	Tunnels  int64            `json:"tunnels"`
	BytesIn  int64            `json:"bytes_in"`
	BytesOut int64            `json:"bytes_out"`
	Closes   map[string]int64 `json:"closes"`
}

type platformProxyView struct {
	PolicyRevision       string                    `json:"policy_revision"`
	Granted              bool                      `json:"granted"`
	Ready                bool                      `json:"ready"`
	DeclaredHosts        []string                  `json:"declared_hosts"`
	Policy               *platformProxyPolicyView  `json:"policy"`
	Runner               platformProxyRunnerView   `json:"runner"`
	MaxDailyBytes        int64                     `json:"max_daily_bytes"`
	PreviousPolicySHA256 string                    `json:"previous_policy_sha256"`
	Today                platformProxyToday        `json:"today"`
	Rollups              []platformProxyRollupView `json:"rollups"`
}

// platformProxyRollupWindow is how far back the console shows rollups.
const platformProxyRollupWindow = 24 * time.Hour

// platformProxyRollupRows bounds the audit rows read for one installation's view: 64 hosts for 24 hours, with
// room for an hour the runner reported twice.
const platformProxyRollupRows = 4000

func (s *Server) apiPlatformProxyView(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	out := platformProxyView{DeclaredHosts: []string{}, Rollups: []platformProxyRollupView{}, MaxDailyBytes: appplatform.ProxyDailyBytesMax,
		Runner: platformProxyRunnerView{Pins: []appplatform.ProxyPin{}}}
	// The runner is asked outside the read, as a review asks it. A runner that cannot be asked is not an error
	// of the view: the dialog then offers no pin and marks every reviewed pin as not permitted.
	var runner transport.ProxyRunnerState
	if endpoint, ok := s.platformRuntimeEndpoint(); ok || s.platformObserveProxy != nil {
		queryCtx, cancel := context.WithTimeout(r.Context(), platformProxyViewQueryTimeout)
		state, err := s.observePlatformProxy(queryCtx, endpoint)
		cancel()
		if err == nil {
			runner = state
			out.Runner = platformProxyRunnerView{Available: true, PinsSupported: state.PinsSupported, Pins: append([]appplatform.ProxyPin{}, state.Pins...)}
		}
	}
	err = s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var desired, phase, release, epoch string
		var revision int64
		var descriptor []byte
		if err := e.queryRow(`SELECT i.desired_state,i.phase,i.policy_revision,i.controller_epoch,a.release_sha256,r.descriptor
			FROM app_platform_installations i
			JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
			JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE i.id=?`, installation).Scan(&desired, &phase, &revision, &epoch, &release, &descriptor); err != nil {
			return errPlatformInstallationMissing
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.NetworkProxy == nil {
			return errPlatformInstallationMissing
		}
		out.DeclaredHosts = slices.Clone(parsed.Manifest.NetworkProxy.Hosts)
		operations, err := platformUnresolvedOperationCount(e, installation)
		if err != nil {
			return err
		}
		effects, err := platformUnresolvedOperationEffectCount(e, installation)
		if err != nil {
			return err
		}
		var background int
		var stored string
		if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run' AND policy_revision=?`, installation, revision).Scan(&background); err != nil {
			background = 0
		}
		if err := e.queryRow(`SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy' AND policy_revision=?`, installation, revision).Scan(&stored); err != nil {
			return errPlatformInstallationMissing
		}
		out.PolicyRevision = strconv.FormatInt(revision, 10)
		out.PreviousPolicySHA256 = platformDigest([]byte(stored))
		out.Ready = desired == "enabled" && phase == "ready" && operations+effects == 0 && background == 1
		budget := appplatform.ProxyDailyBytesMax
		// Granted is what the session loader treats as granted: a row a consumed review backs.
		if policy, err := loadPlatformProxyGrant(e, installation, revision, release, epoch); err == nil {
			out.Granted = true
			out.Policy = &platformProxyPolicyView{Hosts: slices.Clone(policy.Policy.Hosts), Pins: []platformProxyPinView{}, DailyBytes: policy.Policy.DailyBytes}
			for _, pin := range policy.Policy.Pins {
				out.Policy.Pins = append(out.Policy.Pins, platformProxyPinView{Host: pin.Host, Address: pin.Address, Port: pin.Port,
					Permitted: out.Runner.Available && runner.PinsSupported && slices.Contains(runner.Pins, pin)})
			}
			budget = policy.Policy.DailyBytes
		}
		now := time.Now().UTC()
		today := now.Format("2006-01-02")
		out.Today = platformProxyToday{Day: today, Budget: budget}
		usage, err := e.query(`SELECT detail FROM audit_log WHERE action=? AND target_type='app_platform_installation' AND target_id=? AND actor='system' ORDER BY id DESC LIMIT 1`, platformProxyAuditUsage, installation)
		if err != nil {
			return err
		}
		for usage.Next() {
			var detail string
			var row struct {
				Day   string `json:"day"`
				Bytes int64  `json:"bytes"`
			}
			if usage.Scan(&detail) == nil && json.Unmarshal([]byte(detail), &row) == nil && row.Day == today {
				out.Today.Bytes, out.Today.Reported = row.Bytes, true
			}
		}
		if err := usage.Err(); err != nil {
			usage.Close()
			return err
		}
		usage.Close()
		rollups, err := e.query(`SELECT detail FROM audit_log WHERE action=? AND target_type='app_platform_installation' AND target_id=? AND actor='system' ORDER BY id DESC LIMIT ?`, platformProxyAuditRollup, installation, platformProxyRollupRows)
		if err != nil {
			return err
		}
		defer rollups.Close()
		merged := map[string]*platformProxyRollupView{}
		var todayBytes int64
		since := now.Add(-platformProxyRollupWindow).Truncate(time.Hour)
		for rollups.Next() {
			var detail string
			var row platformProxyRollupView
			if rollups.Scan(&detail) != nil || json.Unmarshal([]byte(detail), &row) != nil {
				continue
			}
			hour, err := time.Parse(time.RFC3339, row.Hour)
			if err != nil {
				continue
			}
			if hour.UTC().Format("2006-01-02") == today {
				todayBytes += row.BytesIn + row.BytesOut
			}
			if hour.Before(since) {
				continue
			}
			key := row.Hour + "\x00" + row.Host
			if m := merged[key]; m != nil {
				m.Tunnels += row.Tunnels
				m.BytesIn += row.BytesIn
				m.BytesOut += row.BytesOut
				for reason, n := range row.Closes {
					m.Closes[reason] += n
				}
				continue
			}
			if row.Closes == nil {
				row.Closes = map[string]int64{}
			}
			merged[key] = &row
		}
		if err := rollups.Err(); err != nil {
			return err
		}
		// Without a usage report today, the hours already rolled up are a floor, not a zero.
		if !out.Today.Reported && todayBytes > 0 {
			out.Today.Bytes, out.Today.Reported = todayBytes, true
		}
		for _, m := range merged {
			out.Rollups = append(out.Rollups, *m)
		}
		sort.Slice(out.Rollups, func(i, j int) bool {
			if out.Rollups[i].Hour != out.Rollups[j].Hour {
				return out.Rollups[i].Hour > out.Rollups[j].Hour
			}
			return out.Rollups[i].Host < out.Rollups[j].Host
		})
		return nil
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, out)
}

// platformProxyPinsFromJSON reads the optional pins member: an array of closed {host, address, port} objects. The
// port is bounded before it is narrowed.
func platformProxyPinsFromJSON(raw json.RawMessage) ([]appplatform.ProxyPin, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > appplatform.MaxProxyPins {
		return nil, errPlatformManagementInput
	}
	pins := make([]appplatform.ProxyPin, 0, len(items))
	for _, item := range items {
		fields, err := platformManagementObject(item, "host", "address", "port")
		if err != nil || len(fields) != 3 {
			return nil, errPlatformManagementInput
		}
		host, err := platformManagementString(fields, "host")
		if err != nil {
			return nil, err
		}
		address, err := platformManagementString(fields, "address")
		if err != nil {
			return nil, err
		}
		port, ok := platformManagementInt(fields["port"])
		if !ok || port < 1 || port > 65535 {
			return nil, errPlatformManagementInput
		}
		pins = append(pins, appplatform.ProxyPin{Host: host, Address: address, Port: int(port)})
	}
	return pins, nil
}

func platformProxyRequestFromBody(w http.ResponseWriter, r *http.Request) (platformProxyRequest, error) {
	var req platformProxyRequest
	// pins is optional, so that a client that predates pins keeps working; every other member is required.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformManagementBodyLimit))
	if err != nil {
		return req, errPlatformManagementInput
	}
	fields, err := platformManagementObject(raw, "policyRevision", "previousPolicySha256", "hosts", "dailyBytes", "pins")
	if err != nil {
		return req, err
	}
	if _, hasPins := fields["pins"]; hasPins {
		if req.Pins, err = platformProxyPinsFromJSON(fields["pins"]); err != nil {
			return req, err
		}
	} else if len(fields) != 4 {
		return req, errPlatformManagementInput
	}
	for _, key := range []string{"policyRevision", "previousPolicySha256", "hosts", "dailyBytes"} {
		if _, ok := fields[key]; !ok {
			return req, errPlatformManagementInput
		}
	}
	revision, err := platformManagementString(fields, "policyRevision")
	if err != nil || !platformPositiveCounter(revision) {
		return req, errPlatformManagementInput
	}
	req.PolicyRevision, _ = strconv.ParseInt(revision, 10, 64)
	if req.PreviousPolicySHA256, err = platformManagementString(fields, "previousPolicySha256"); err != nil || !platformValidSHA(req.PreviousPolicySHA256) {
		return req, errPlatformManagementInput
	}
	var ok bool
	if req.Hosts, ok = platformManagementStrings(fields["hosts"]); !ok || len(req.Hosts) > appplatform.MaxProxyHosts {
		return req, errPlatformManagementInput
	}
	if req.DailyBytes, ok = platformManagementInt(fields["dailyBytes"]); !ok {
		return req, errPlatformManagementInput
	}
	return req, nil
}

type platformProxyDiffEntry struct {
	Entry    string `json:"entry"`
	Wildcard bool   `json:"wildcard"`
}

func platformProxyDiffEntries(hosts []string) []platformProxyDiffEntry {
	out := make([]platformProxyDiffEntry, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, platformProxyDiffEntry{Entry: h, Wildcard: strings.HasPrefix(h, "*.")})
	}
	return out
}

func (s *Server) apiPlatformProxyPlan(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	req, err := platformProxyRequestFromBody(w, r)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok && s.platformObserveProxy == nil {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	result, err := s.createPlatformProxyReview(r.Context(), session, endpoint, installation, req)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	plan := result.Plan
	writeJSON(w, map[string]any{
		"plan_id": result.ID, "plan_sha256": result.SHA256, "expires_ms": strconv.FormatInt(result.ExpiresMS, 10),
		"policy_revision": strconv.FormatInt(plan.PolicyRevision, 10),
		"diff": map[string]any{
			"added": platformProxyDiffEntries(plan.HostsAdded), "removed": platformProxyDiffEntries(plan.HostsRemoved),
			"pins":        map[string]any{"added": plan.PinsAdded, "removed": plan.PinsRemoved, "changed": plan.PinsChanged},
			"daily_bytes": map[string]int64{"old": plan.DailyBytesOld, "new": plan.DailyBytesNew},
		},
	})
}

func (s *Server) apiPlatformProxyConsume(w http.ResponseWriter, r *http.Request, user string) {
	planID := r.PathValue("id")
	if !platformValidUUID(planID) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	fields, err := platformManagementRead(w, r, "sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	planSHA, err := platformManagementString(fields, "sha256")
	if err != nil || !platformValidSHA(planSHA) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok && s.platformObserveProxy == nil {
		platformManagementError(w, errAppPlatformRuntimeConflict)
		return
	}
	change, err := s.consumePlatformProxyReview(r.Context(), session, endpoint, planID, planSHA)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "policy_revision": strconv.FormatInt(change.PolicyRevision, 10)})
}
