package app

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Administration of the application platform's retention policy (doc 172). The two adjustable values live in
// meta; everything else is fixed policy that the page shows read-only. The routes sit behind the same
// permission as the storage cleanup console.

func (s *Server) apiPlatformRetentionGet(w http.ResponseWriter, r *http.Request, user string) {
	c := s.platformRetentionConfigLoad()
	var last any
	var at, detail string
	if err := s.st.queryRow(`SELECT at,COALESCE(detail,'') FROM audit_log WHERE action=? ORDER BY id DESC LIMIT 1`, platformRetentionAuditActionName).Scan(&at, &detail); err == nil {
		var counts map[string]any
		if json.Unmarshal([]byte(detail), &counts) == nil {
			last = map[string]any{"at": at, "counts": counts}
		}
	}
	writeJSON(w, map[string]any{
		"operation_days": c.OperationDays,
		"recovery_sets":  c.RecoverySets,
		"bounds": map[string]int{
			"operation_days_min": platformRetentionOperationDaysMin,
			"operation_days_max": platformRetentionOperationDaysMax,
			"recovery_sets_min":  platformRetentionRecoverySetsMin,
			"recovery_sets_max":  platformRetentionRecoverySetsMax,
		},
		"fixed": map[string]any{
			"review_plan_minutes":        platformReviewPlanValidityMS / 60000,
			"review_plan_grace_hours":    platformReviewPlanSweepGraceMS / (60 * 60 * 1000),
			"task_days":                  platformTaskRetentionDays,
			"task_rows_per_installation": platformTaskRowsPerInstallation,
			"runner_log_mib":             platformRetentionRunnerLogMiB,
			"uninstalled_data":           "until_deleted",
		},
		"last_sweep": last,
		// Recovery sets past the retention are deleted through the runner's delete-checkpoint command by
		// prunePlatformRecoverySets (doc 172 section 6). The storage page shows the notice only for "blocked".
		"recovery_set_pruning": "enabled",
	})
}

func (s *Server) apiPlatformRetentionSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		OperationDays *int `json:"operation_days"`
		RecoverySets  *int `json:"recovery_sets"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Refused, not clamped, and checked before anything is stored: a value an administrator typed and did not
	// get is a retention they believe is one number and is another.
	if in.OperationDays != nil && (*in.OperationDays < platformRetentionOperationDaysMin || *in.OperationDays > platformRetentionOperationDaysMax) {
		jsonErrorCode(w, http.StatusBadRequest, "retention_out_of_range", "operation_days out of range")
		return
	}
	if in.RecoverySets != nil && (*in.RecoverySets < platformRetentionRecoverySetsMin || *in.RecoverySets > platformRetentionRecoverySetsMax) {
		jsonErrorCode(w, http.StatusBadRequest, "retention_out_of_range", "recovery_sets out of range")
		return
	}
	if in.OperationDays != nil {
		s.st.SetSetting(platformRetentionOperationDaysKey, strconv.Itoa(*in.OperationDays))
	}
	if in.RecoverySets != nil {
		s.st.SetSetting(platformRetentionRecoverySetsKey, strconv.Itoa(*in.RecoverySets))
	}
	writeJSON(w, map[string]any{"ok": true})
}
