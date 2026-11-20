//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

type fakeInbox struct {
	found   data.ImportInboxArchive
	err     error
	removed []string
}

func (f *fakeInbox) InspectImportInbox(ctx context.Context, _ data.RootIdentity, digest string, maxEntries int64) (data.ImportInboxArchive, error) {
	return f.found, f.err
}

func (f *fakeInbox) RemoveImportArchive(ctx context.Context, _ data.RootIdentity, digest string) error {
	f.removed = append(f.removed, digest)
	return f.err
}

func dataQuery(t *testing.T, f checkpointFixture, payload control.DataQueryPayload) (control.DataQueryCommand, []byte, []byte) {
	t.Helper()
	authority, key := dataCommandAuthority()
	now := time.Now().UTC().UnixMilli()
	payload.ReleaseSHA256 = f.command.Payload.ReleaseSHA256
	command := control.DataQueryCommand{CommandHeader: control.CommandHeader{
		Format: control.DataQueryFormat, DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		ControlRevision: "3", InstallationID: f.command.InstallationID, OperationID: fixtureID(7300),
		TargetGeneration: f.command.TargetGeneration, Stage: control.DataQueryStage, EffectNumber: "1",
		EffectID: fixtureID(7301), Kind: control.DataQueryKind, InstallationFence: "1",
	}, Payload: payload}
	digest, err := control.DataQueryLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignDataQuery(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, signature
}

func TestDataQueryInspectsTheInboxAndReportsAMissingArchiveWithoutAnError(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	digest := strings.Repeat("a", 64)
	inbox := &fakeInbox{found: data.ImportInboxArchive{Found: true, Bytes: 4096, SchemaMarker: 3, Manifest: data.ImportManifest{Entries: 12, Bytes: 9000}}}
	command, raw, sig := dataQuery(t, f, control.DataQueryPayload{Operation: control.DataQueryInspectImport, ArchiveSHA256: digest, QuotaBytes: "0", QuotaInodes: "100000"})
	report, err := f.store.AnswerDataQuery(ctx, raw, sig, inbox, data.RootIdentity{})
	if err != nil || !report.ArchiveFound || report.ArchiveEntries != "12" || report.ContentBytes != "9000" || report.SchemaMarker != "3" ||
		!DataQueryReportMatchesCommand(report, command) {
		t.Fatalf("inspect: %+v %v", report, err)
	}
	for _, missing := range []error{data.ErrImportArchiveMissing, data.ErrImportArchiveAmbiguous, data.ErrImportArchiveInvalid} {
		report, err := f.store.AnswerDataQuery(ctx, raw, sig, &fakeInbox{err: missing}, data.RootIdentity{})
		if err != nil || report.ArchiveFound {
			t.Fatalf("%v: %+v %v", missing, report, err)
		}
	}
	if _, err := f.store.AnswerDataQuery(ctx, raw, sig, &fakeInbox{err: errors.New("disk")}, data.RootIdentity{}); err == nil {
		t.Fatal("an unexpected inbox failure was reported as a missing archive")
	}
}

func TestDataQueryReportsCapacityWithoutReservingAnything(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	if err := f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 2 << 30, ImageStoreBytes: 1 << 40, RunningBackends: 64}); err != nil {
		t.Fatal(err)
	}
	before := scalarJournalInt(t, f.store.db, `SELECT COALESCE(SUM(disk_bytes),0) FROM runner_reservations WHERE state='held'`)
	namespace := f.command.Payload.NamespaceID
	_, raw, sig := dataQuery(t, f, control.DataQueryPayload{Operation: control.DataQueryQuotaCapacity, NamespaceID: namespace, QuotaBytes: strconv.FormatInt(6<<30, 10), QuotaInodes: "100000"})
	report, err := f.store.AnswerDataQuery(ctx, raw, sig, nil, data.RootIdentity{})
	if err != nil || report.CapacityFits || report.CapacityDim != CapacityDataQuota || report.CapacityRequired == "0" {
		t.Fatalf("a quota beyond capacity: %+v %v", report, err)
	}
	_, raw, sig = dataQuery(t, f, control.DataQueryPayload{Operation: control.DataQueryQuotaCapacity, NamespaceID: namespace, QuotaBytes: "536870912", QuotaInodes: "65536"})
	report, err = f.store.AnswerDataQuery(ctx, raw, sig, nil, data.RootIdentity{})
	if err != nil || !report.CapacityFits {
		t.Fatalf("a quota inside capacity: %+v %v", report, err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COALESCE(SUM(disk_bytes),0) FROM runner_reservations WHERE state='held'`) != before {
		t.Fatal("a capacity question changed the reservations")
	}
}

func TestDataQueryRemovesAnArchiveOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	inbox := &fakeInbox{}
	digest := strings.Repeat("b", 64)
	_, raw, sig := dataQuery(t, f, control.DataQueryPayload{Operation: control.DataQueryRemoveImport, ArchiveSHA256: digest, QuotaBytes: "0", QuotaInodes: "0"})
	if _, err := f.store.AnswerDataQuery(ctx, raw, sig, inbox, data.RootIdentity{}); err != nil || len(inbox.removed) != 1 || inbox.removed[0] != digest {
		t.Fatalf("remove: %v %v", inbox.removed, err)
	}
	_, raw, sig = dataQuery(t, f, control.DataQueryPayload{Operation: control.DataQueryInspectImport, ArchiveSHA256: digest, QuotaBytes: "0", QuotaInodes: "1000"})
	if _, err := f.store.AnswerDataQuery(ctx, raw, sig, inbox, data.RootIdentity{}); err != nil || len(inbox.removed) != 1 {
		t.Fatal("an inspection removed an archive")
	}
}
