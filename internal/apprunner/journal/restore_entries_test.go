package journal

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// A restore walks at most the namespace's own inode quota when the journal knows it, and the platform
// maximum only when it does not.
func TestRestoreEntryCapFollowsTheNamespaceInodeQuota(t *testing.T) {
	source := restoreSource{command: control.RestoreCommand{Payload: control.RestorePayload{
		CheckpointID: fixtureID(1), NamespaceID: fixtureID(2), ProjectID: "7", DataFormat: "1", ArchiveBytes: "4096",
		NamespaceInode: "9", Device: "3", WriterFence: "1",
	}}}
	source.inodeQuota = 150000
	request, err := restoreRequestFor(source)
	if err != nil || request.MaxEntries != 150000 {
		t.Fatalf("known quota: %+v %v", request, err)
	}
	source.inodeQuota = 0
	request, err = restoreRequestFor(source)
	if err != nil || request.MaxEntries != appplatform.DataQuotaMaxInodes {
		t.Fatalf("unknown quota: %+v %v", request, err)
	}
}
