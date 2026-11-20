package app

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The administrator routes of the proxy review and the console view (doc 184 sections 5.2 and 7). All three sit
// under the admin management route pattern, so a non-administrator, a cross-origin request or a body that is not
// closed JSON is refused before any plan is written.

type platformProxyPolicyView struct {
	Hosts      []string `json:"hosts"`
	DailyBytes int64    `json:"daily_bytes"`
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
	out := platformProxyView{DeclaredHosts: []string{}, Rollups: []platformProxyRollupView{}, MaxDailyBytes: appplatform.ProxyDailyBytesMax}
	err = s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var desired, phase string
		var revision int64
		var descriptor []byte
		if err := e.queryRow(`SELECT i.desired_state,i.phase,i.policy_revision,r.descriptor
			FROM app_platform_installations i
			JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
			JOIN app_platform_releases r ON r.sha256=a.release_sha256 WHERE i.id=?`, installation).Scan(&desired, &phase, &revision, &descriptor); err != nil {
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
		var background, granted int
		var stored string
		if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run' AND policy_revision=?`, installation, revision).Scan(&background); err != nil {
			background = 0
		}
		if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy' AND policy_revision=?`, installation, revision).Scan(&granted, &stored); err != nil {
			return errPlatformInstallationMissing
		}
		out.PolicyRevision = strconv.FormatInt(revision, 10)
		out.PreviousPolicySHA256 = platformDigest([]byte(stored))
		out.Ready = desired == "enabled" && phase == "ready" && operations+effects == 0 && background == 1
		budget := appplatform.ProxyDailyBytesMax
		if granted == 1 {
			if policy, err := appplatform.ParseProxyPolicy([]byte(stored)); err == nil {
				out.Granted = true
				out.Policy = &platformProxyPolicyView{Hosts: slices.Clone(policy.Policy.Hosts), DailyBytes: policy.Policy.DailyBytes}
				budget = policy.Policy.DailyBytes
			}
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

func platformProxyRequestFromBody(w http.ResponseWriter, r *http.Request) (platformProxyRequest, error) {
	var req platformProxyRequest
	fields, err := platformManagementRead(w, r, "policyRevision", "previousPolicySha256", "hosts", "dailyBytes")
	if err != nil {
		return req, err
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
