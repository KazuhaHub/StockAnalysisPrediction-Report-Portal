package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The digest of the option the runner built from 4bcc3d5b created containers with.
const platformSeccompV1SHA256 = "981fc0c0c4ee290a969bed30c93e8ea443a4a0c42a0bdbadddd4f9f24eb142f5"

func platformWriterReceiptWithSeccomp(t *testing.T, r journal.DataWriterReceipt, sha string) journal.DataWriterReceipt {
	t.Helper()
	r.Native.SeccompSHA256 = sha
	binding, _ := json.Marshal(struct {
		Purpose  string `json:"purpose"`
		Claim    any    `json:"claim"`
		Revision string `json:"control_revision"`
		Native   any    `json:"native"`
	}{"portal.runner.data-writer-binding/1-dev.1", r.Claim, r.ControlRevision, r.Native})
	r.BindingSHA256 = platformDigest(binding)
	return r
}

// The writer receipt of an install made under the previous profile stays readable as history, so a running app
// launches and answers requests; the install in progress still requires the current profile.
func TestPlatformWriterReceiptSeccompModes(t *testing.T) {
	_, lease, writer := platformWritableStartFixture(t, false)
	now := time.Now().UnixMilli()
	for name, tc := range map[string]struct {
		sha           string
		current, hist bool
	}{
		"current": {driver.CurrentSeccompSHA256(), true, true},
		"v1":      {platformSeccompV1SHA256, false, true},
		"unknown": {strings.Repeat("e", 64), false, false},
	} {
		r := platformWriterReceiptWithSeccomp(t, writer.receipt, tc.sha)
		if got := platformWriterReceiptMatchesFence(r, writer, lease.fence, now, driver.SeccompCurrent); got != tc.current {
			t.Fatalf("%s: install-in-progress match = %v", name, got)
		}
		if got := platformWriterReceiptMatchesFence(r, writer, lease.fence, now, driver.SeccompHistory); got != tc.hist {
			t.Fatalf("%s: history match = %v", name, got)
		}
	}
}
