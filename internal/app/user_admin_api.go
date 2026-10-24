package app

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HTTP handlers for the enterprise user admin: organizational groups (group model B)
// and bulk actions over the user list. Admin-only (wired with requireAdminJSON).

func userGroupsJSON(gs []UserGroup) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		// A nil weight / urgent_unlimited on a non-default group means "inherit the
		// Default group's value"; the UI renders that as an inherit toggle.
		var weight any = g.Weight
		var urgent any = g.UrgentFree
		var allowUrgent any = g.AllowUrgent
		var maxQueued any = g.MaxQueued
		var runWindow any = g.RunWindow
		if g.WeightInherit {
			weight = nil
		}
		if g.UrgentInherit {
			urgent = nil
		}
		if g.AllowUrgentInherit {
			allowUrgent = nil
		}
		if g.MaxQueuedInherit {
			maxQueued = nil
		}
		if g.RunWindowInherit {
			runWindow = nil
		}
		var dailyQuota any = g.DailyRunQuota
		if g.DailyQuotaInherit {
			dailyQuota = nil
		}
		// nil = this OU sets nothing and inherits, which is what the InheritField renders as
		// "inherited" — the same convention daily_run_quota uses above.
		var totpEnroll, passkeyEnroll, require2fa any = g.TOTPEnroll, g.PasskeyEnroll, g.Require2FA
		if g.TOTPEnrollInherit {
			totpEnroll = nil
		}
		if g.PasskeyEnrollInherit {
			passkeyEnroll = nil
		}
		if g.Require2FAInherit {
			require2fa = nil
		}
		out = append(out, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description,
			"is_default": g.IsDefault, "weight": weight, "urgent_unlimited": urgent,
			"allow_urgent": allowUrgent, "max_queued": maxQueued, "run_window": runWindow,
			"priority": g.Priority, "members": g.Members,
			// External-user tenancy (ADR 0022): restricted is this group's OWN flag, while
			// restricted_effective also accounts for a restricted ancestor (sticky down the tree).
			"restricted": g.Restricted, "restricted_effective": g.RestrictedEffective,
			"daily_run_quota":  dailyQuota,
			"run_quota_period": g.QuotaPeriod,
			// Per-OU second-factor enrolment (security_policy.go).
			"totp_enroll": totpEnroll, "passkey_enroll": passkeyEnroll, "require_2fa": require2fa,
			// parent_id so the admin UI can render the tree it is editing (ADR 0022).
			"parent_id": g.ParentID,
		})
	}
	return out
}

// adminGroupsJSON is the OU list the admin console renders, with forecast access resolved by the
// server from one strict read of the tree. Every admin surface that lists OUs (the groups list and
// the users list the OU editor lives on) goes through it, so none can describe access differently.
// The tree is read first, so a failed read is the only way this errors.
func (s *Server) adminGroupsJSON() ([]map[string]any, error) {
	return s.st.adminOUGroups()
}

// adminGroupsWithAccess adds the three forecast fields to each OU the index holds. An OU the index
// does not hold was created after the tree was read: it is listed without them rather than failing
// the whole list, and the editor hides a field the server did not resolve. Nothing is guessed, so
// nothing can be shown that the server did not read.
func adminGroupsWithAccess(list []UserGroup, ix ouIndex) []map[string]any {
	groups := userGroupsJSON(list)
	for _, group := range groups {
		id := group["id"].(int64)
		row, ok := ix.ous[id]
		if !ok {
			continue
		}
		d := resolveForecastScope(forecastSubject{User: "preview", GroupID: id}, ix)
		group["forecast_scope"] = row.scope
		group["forecast_scope_effective"] = d.Scope
		group["forecast_scope_source"] = d.Source
	}
	return groups
}

func (s *Server) apiAdminGroups(w http.ResponseWriter, r *http.Request, user string) {
	groups, err := s.adminGroupsJSON()
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	writeJSON(w, map[string]any{"groups": groups})
}

// groupInput is the create/update body. Weight and UrgentUnlimited are pointers so a
// JSON null means "inherit the Default group" and a value means "override".
type groupInput struct {
	ForecastScope   json.RawMessage `json:"forecast_scope"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Weight          *int            `json:"weight"`
	UrgentUnlimited *bool           `json:"urgent_unlimited"`
	AllowUrgent     *bool           `json:"allow_urgent"`
	MaxQueued       *int            `json:"max_queued"`
	RunWindow       *string         `json:"run_window"`
	Priority        string          `json:"priority"`
	// External-user tenancy (ADR 0022). Restricted is a plain flag (absent = leave as-is);
	// DailyRunQuota is a pointer so null means "inherit the parent OU" and 0 means unlimited.
	Restricted    *bool `json:"restricted"`
	DailyRunQuota *int  `json:"daily_run_quota"`
	// The window the cap is measured over. Only meaningful alongside a quota, so it is written in
	// the same call rather than separately — a period with no number configures nothing.
	RunQuotaPeriod *string `json:"run_quota_period"`
	// ParentID places this OU in the tree. A pointer because absent means "leave it where it is" —
	// every pre-existing caller of this endpoint omits the field — while an explicit 0 detaches it
	// to a root. Without this the tree could only be built by hand in SQL, which made every
	// inherited behaviour (restricted stickiness, quota inheritance, the run allow-list's and the
	// version grants' nearest-ancestor resolution) unreachable through the product.
	ParentID *int64 `json:"parent_id"`
	// The login-protection overrides. Always sent by the form — the endpoint replaces an OU's whole
	// configuration — so nil means "inherit the parent OU", exactly as daily_run_quota reads.
	TOTPEnroll    *bool `json:"totp_enroll"`
	PasskeyEnroll *bool `json:"passkey_enroll"`
	Require2FA    *bool `json:"require_2fa"`
}

// tenancy normalizes the external-tenancy fields for storage: a negative quota is clamped to 0
// (unlimited), and the Default group (the root baseline) can neither be restricted nor inherit.
func (in groupInput) tenancy(isDefault bool) (*bool, *int) {
	restricted, quota := in.Restricted, in.DailyRunQuota
	if quota != nil && *quota < 0 {
		zero := 0
		quota = &zero
	}
	if isDefault {
		if restricted == nil || *restricted {
			no := false
			restricted = &no // the root OU is the internal baseline; never restricted
		}
		if quota == nil {
			zero := 0
			quota = &zero
		}
	}
	return restricted, quota
}

// quotaPeriod is the window the cap is measured over, defaulting to the day — which is both the
// pre-existing behaviour and the narrowest window, so an omitted or unrecognized value can never
// widen an allowance.
func (in groupInput) quotaPeriod() string {
	if in.RunQuotaPeriod == nil {
		return QuotaDay
	}
	return *in.RunQuotaPeriod
}

// overrides normalizes the input's inherit/override fields for storage. The Default
// group cannot inherit (it is the baseline), so a null there is coerced to concrete.
func (in groupInput) overrides(isDefault bool) (*int, *bool) {
	w, u := in.Weight, in.UrgentUnlimited
	if w != nil {
		cw := clampWeight(*w)
		w = &cw
	} else if isDefault {
		zero := 0
		w = &zero
	}
	if u == nil && isDefault {
		f := false
		u = &f
	}
	return w, u
}

// governance normalizes the allow-urgent / max-queued / run-window fields. On the Default
// group a null is coerced to the permissive baseline (urgent allowed, no cap, any hour).
func (in groupInput) governance(isDefault bool) (*bool, *int, *string) {
	allow, mq, rw := in.AllowUrgent, in.MaxQueued, in.RunWindow
	if mq != nil {
		v := *mq
		if v < 0 {
			v = 0
		}
		mq = &v
	}
	if rw != nil {
		v := normalizeRunWindow(*rw)
		rw = &v
	}
	if isDefault {
		if allow == nil {
			def := true
			allow = &def
		}
		if mq == nil {
			zero := 0
			mq = &zero
		}
		if rw == nil {
			empty := ""
			rw = &empty
		}
	}
	return allow, mq, rw
}

// normalizeRunWindow keeps a valid "H1-H2" window, else "" (no restriction).
func normalizeRunWindow(win string) string {
	if _, _, ok := parseRunWindow(win); ok {
		return strings.TrimSpace(win)
	}
	return ""
}

func (s *Server) apiGroupAdd(w http.ResponseWriter, r *http.Request, user string) {
	var in groupInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	result, err := s.writeOUInput(0, in, true)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	s.recordChange(r, user, AuditGroupCreate, "group", itoa64(result.ID), result.Detail)
	writeJSON(w, map[string]any{"ok": true, "id": result.ID})
}

func (s *Server) apiGroupSave(w http.ResponseWriter, r *http.Request, user string) {
	var in groupInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	id := pathID(r, "id")
	result, err := s.writeOUInput(id, in, false)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	if result.Changed {
		s.recordChange(r, user, AuditGroupChange, "group", itoa64(id), result.Detail)
	}
	pending := 0
	if s.st.GroupRequires2FA(id) {
		if n, err := s.st.membersWithoutFactor(id); err == nil {
			pending = n
		}
	}
	writeJSON(w, map[string]any{"ok": true, "pending_enrolment": pending})
}

// apiGroupTargets returns one OU's run allow-list (ADR 0022 R3) alongside every target, so the
// admin matrix can render "which workflows may this OU run, and on which surfaces".
func (s *Server) apiGroupTargets(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	chain, err := snapshot.graph.policyChain(id)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	policies, err := loadOUTargetPoliciesFrom(s.st.db)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	resolved := resolveOUTargetPolicy(chain, policies)
	granted := make([]map[string]any, 0)
	for _, g := range policies[id] {
		granted = append(granted, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	targets := make([]map[string]any, 0)
	for _, t := range s.st.ListTargets() {
		targets = append(targets, map[string]any{"id": t.ID, "name": t.Name,
			"surfaces": TargetSurfaces(t.Surfaces), "output_subtype": targetOutputSubtype(t.Config)})
	}
	effective := make([]map[string]any, 0)
	for _, g := range resolved.Effective {
		effective = append(effective, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	_, configured := policies[id]
	writeJSON(w, map[string]any{"granted": granted, "targets": targets, "configured": configured,
		"effective": effective, "source_ou": resolved.SourceOU})
}

// apiGroupTargetsSave replaces an OU's allow-list atomically. An empty list means "this OU may run
// nothing" — default-deny is the point, so it is a legitimate save, not an error.
func (s *Server) apiGroupTargetsSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Inherit bool `json:"inherit"`
		Granted []struct {
			TargetID int64    `json:"target_id"`
			Surfaces []string `json:"surfaces"`
		} `json:"granted"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	rows := make([]GroupTarget, 0, len(in.Granted))
	for _, g := range in.Granted {
		// An all-surfaces selection is stored as "" so the row keeps tracking the target's own
		// surfaces if an admin later narrows them.
		sub := strings.Join(g.Surfaces, ",")
		if len(TargetSurfaces(sub)) == len(TargetSurfaces("")) {
			sub = ""
		}
		rows = append(rows, GroupTarget{TargetID: g.TargetID, Surfaces: sub})
	}
	gid := pathID(r, "id")
	// Both sides, like every other grant row: the current allow-list answers "what may this OU
	// run now", and only the log answers "when did it gain that, and who granted it".
	before := make([]map[string]any, 0)
	for _, g := range s.st.GroupTargets(gid) {
		before = append(before, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	var saveErr error
	if in.Inherit {
		saveErr = s.st.InheritGroupTargets(gid)
	} else {
		saveErr = s.st.SetGroupTargets(gid, rows)
	}
	if err := saveErr; err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after := make([]map[string]any, 0, len(rows))
	for _, g := range rows {
		after = append(after, map[string]any{"target_id": g.TargetID, "surfaces": TargetSurfaces(g.Surfaces)})
	}
	s.recordChange(r, user, AuditGrantChange, "group", itoa64(gid),
		map[string]any{"kind": "run_allowlist", "before": before, "after": after, "inherit": in.Inherit})
	writeJSON(w, okJSON)
}

// clampWeight keeps a group's urgent-ticket allowance in a sane range.
func clampWeight(w int) int {
	if w < 0 {
		return 0
	}
	if w > 999 {
		return 999
	}
	return w
}

// apiGroupAnnouncements previews what deleting this OU would do to the announcements addressed to
// it. The console asks before offering the delete, so an operator sees the consequence while it is
// still avoidable rather than discovering it afterwards from an announcement nobody received.
func (s *Server) apiGroupAnnouncements(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"affected": announcementImpactJSON(s.announcementImpactOf(groupPrincipal(pathID(r, "id"))))})
}

// apiGroupDelete removes an OU.
//
// Deleting one sweeps its announcement grants (ids are reassigned, so a left-behind row would be
// inherited by the next OU), and an announcement whose ONLY recipient was this OU is then addressed
// to nobody — still enabled, still "live" in the console, reaching no one and reporting nothing.
// Rather than pick an answer to that, the endpoint refuses until the caller gives one:
//
//	?orphans=disable — switch those announcements off, so nothing pretends to be published
//	?orphans=keep    — leave them; the console flags them as unreachable
//
// The refusal is deliberately at the API and not only in the console. Doing it in the UI alone
// would leave the silent path open to every script that deletes an OU, which is the same class of
// gap as the bulk group-move that recorded no audit line.
func (s *Server) apiGroupDelete(w http.ResponseWriter, r *http.Request, user string) {
	id := pathID(r, "id")
	// Read before the delete: afterwards there is no name to record, and an id alone means
	// nothing to whoever reads the row later.
	name := ""
	movedChildren := []int64{}
	for _, g := range s.st.ListUserGroups() {
		if g.ID == id {
			name = g.Name
		}
		if g.ParentID == id {
			movedChildren = append(movedChildren, g.ID)
		}
	}
	// Also read before the delete: DeleteUserGroup sweeps the grants this is derived from.
	impact := s.announcementImpactOf(groupPrincipal(id))
	choice := strings.TrimSpace(r.URL.Query().Get("orphans"))
	if len(impact.orphaned) > 0 && choice != "disable" && choice != "keep" {
		jsonErrorCode(w, http.StatusConflict, "group_has_announcements",
			"有公告只发给这个分组，删除后将无人可见，请先选择如何处理")
		return
	}
	reattributed, targetOU, err := s.st.DeleteUserGroup(id)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	if reattributed == 0 {
		// The plan's contract: both audit keys are present and both are 0 when nothing moved.
		targetOU = 0
	}
	// A root OU's sets join the Default OU's population without changing the Default's subtree ids,
	// so the population key does not move and the cache has to be dropped explicitly.
	s.purgeForecastGroups()
	disabled := 0
	if choice == "disable" {
		for _, aid := range impact.orphaned {
			if s.st.SetAnnouncementEnabled(aid, false) == nil {
				disabled++
			}
		}
	}
	// Deleting an OU takes its grants and viewer rows with it, so this is an access change too.
	s.recordChange(r, user, AuditGroupDelete, "group", itoa64(id), map[string]any{
		"name": name, "announcements_affected": len(impact.affected),
		"announcements_orphaned": len(impact.orphaned), "announcements_disabled": disabled,
		// P38: the sets keep their frozen attribution except for this sanctioned re-attribution, and
		// the children now inherit along the deleted OU's line.
		"forecast_sets_reattributed": reattributed, "forecast_sets_to_ou": targetOU,
		"children_moved": movedChildren,
	})
	writeJSON(w, okJSON)
}

// apiUsersBulk applies one action to many users at once (enable / disable / delete /
// set_role / set_group / clear_group), honouring the same last-admin and no-self-
// lockout guards as the single-user endpoints. Returns how many were affected.
func (s *Server) apiUsersBulk(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Action    string   `json:"action"`
		Usernames []string `json:"usernames"`
		Role      string   `json:"role"`
		GroupID   int64    `json:"group_id"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.Action == "set_group" || in.Action == "clear_group" {
		destination := in.GroupID
		if in.Action == "clear_group" {
			destination = 0
		}
		changes, err := s.st.SetPrimaryGroups(in.Usernames, destination)
		if err != nil {
			ouWriteHTTPError(w, err)
			return
		}
		for name, from := range changes {
			s.recordChange(r, user, AuditUserChange, "user", name, map[string]any{
				"op": in.Action, "from": from, "group_id": destination, "bulk": true})
		}
		writeJSON(w, map[string]any{"ok": true, "n": len(changes)})
		return
	}
	n := 0
	for _, name := range in.Usernames {
		u := s.st.GetUser(name)
		if u == nil {
			continue
		}
		// A destructive action on yourself or the last admin is skipped.
		protected := name == user || (u.IsAdmin() && s.st.CountAdmins() <= 1)
		switch in.Action {
		case "enable":
			s.st.SetUserActive(name, true)
			n++
		case "disable":
			if protected {
				continue
			}
			s.st.SetUserActive(name, false)
			n++
		case "delete":
			if protected {
				continue
			}
			s.st.DeleteUser(name)
			n++
		case "set_role":
			role := validRole(in.Role)
			if role != "admin" && u.IsAdmin() && s.st.CountAdmins() <= 1 {
				continue
			}
			s.st.SetUserRole(name, role)
			n++
		// Moving an account between OUs is a policy change, not bookkeeping: the OU decides what
		// that person may read (ADR 0022/0024) and, since ADR 0025, what they are TOLD. The
		// single-user path has always audited it; this one did not, so the bulk form of the same
		// change left no trail at all — the shape of gap that is only noticed while investigating.
		case "set_group":
			if in.GroupID == 0 {
				continue
			}
			s.st.SetPrimaryGroup(name, in.GroupID)
			s.recordChange(r, user, AuditUserChange, "user", name,
				map[string]any{"op": "set_group", "group_id": in.GroupID, "bulk": true})
			n++
		case "clear_group":
			s.st.SetPrimaryGroup(name, 0)
			s.recordChange(r, user, AuditUserChange, "user", name,
				map[string]any{"op": "clear_group", "bulk": true})
			n++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "n": n})
}
