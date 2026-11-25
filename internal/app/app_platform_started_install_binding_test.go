package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// rewriteStartReceipt edits the start effect's running receipt through the typed struct and stores it back.
func (f refusedInstall) rewriteStartReceipt(t *testing.T, edit func(*journal.WritableCandidateRunningReceipt)) {
	t.Helper()
	raw := scalar[string](t, f.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE effect_id=?`, f.startEffect)
	running, err := journal.ParseWritableCandidateRunningReceipt([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	edit(&running)
	encoded, err := json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE effect_id=?`, string(encoded), f.startEffect)
}

// The stop stage loads prepare_data and the start at the install's own fence. A start admitted at a newer fence than
// the preparation would be admitted by the review and then refused by every stop, stranding the installation in
// removed/pending, so the review must refuse it up front.
func TestStartedFailedInstallRefusesAStartAtAnotherFenceThanThePreparation(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	fence := scalar[int64](t, f.s.st, `SELECT fence FROM app_platform_operations WHERE id=?`, f.install)
	next := fence + 1
	f.rewriteStartReceipt(t, func(r *journal.WritableCandidateRunningReceipt) { r.Claim.AdmittedFence = strconv.FormatInt(next, 10) })
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET admitted_fence=?,observed_fence=? WHERE effect_id=?`, next, next, f.startEffect)
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET fence=? WHERE id=?`, next, f.install)
	if _, err := f.review(appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a start at fence %d after a preparation at fence %d was admitted: %v", next, fence, err)
	}
}

// Every binding the stop stage requires of the running receipt is required by the review too.
func TestStartedFailedInstallReviewRequiresWhatTheStopRequires(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*journal.WritableCandidateRunningReceipt)
	}{
		{"data_receipt", func(r *journal.WritableCandidateRunningReceipt) { r.Claim.DataReceiptSHA256 = strings.Repeat("a", 64) }},
		{"namespace_inode", func(r *journal.WritableCandidateRunningReceipt) {
			r.Claim.Preflight.Native.NamespaceInode += "1"
		}},
		{"project", func(r *journal.WritableCandidateRunningReceipt) { r.Claim.Preflight.Native.ProjectID += "1" }},
		{"platform", func(r *journal.WritableCandidateRunningReceipt) { r.Claim.Preflight.Native.Platform += "x" }},
		{"control_revision_ahead", func(r *journal.WritableCandidateRunningReceipt) { r.Claim.ControlRevision = "999999" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartedFailedInstall(t, false)
			f.rewriteStartReceipt(t, tc.edit)
			if _, err := f.review(appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
				t.Fatalf("the review admitted a start the stop would refuse: %v", err)
			}
		})
	}
}

// The close of a started-failed install is audited as such, not as a never-started close.
func TestStartedFailedInstallCloseIsAuditedAsStarted(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	removal, _ := f.walkStartedRemoval(t, 7460)
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.failed_install_closed' AND target_id=? AND detail LIKE ?`, f.install, "%"+removal+"%") != 1 ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.never_started_install_closed' AND target_id=?`, f.install) != 0 {
		t.Fatal("the close of a started install was not audited truthfully")
	}
}
