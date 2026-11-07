package transport

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A terminal receipt is immutable: read back on a later tick it still carries the time it was observed, which
// precedes the command signed for the read. Only a status, which describes the moment of the read, must be fresh.
func TestSettledReceiptsAreAcceptedOnReadBack(t *testing.T) {
	old := strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)
	started, received := time.Now().Add(-time.Second), time.Now()
	t.Run("migrate", func(t *testing.T) {
		a, _, raw, sig, descriptor, receipt := migrateTransportFixture(t)
		ref, err := verifyWritableLifecycle(MigrateDataRead, raw, lifecycleSignature(t, MigrateDataRead, a, raw, sig), descriptor, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		receipt.Claim.ClaimedMS, receipt.ObservedMS, receipt.SettledMS = old, old, old
		body, _ := json.Marshal(receipt)
		for _, action := range []WritableLifecycleAction{MigrateDataRead, MigrateDataExecute} {
			if !validWritableLifecycleReply(action, body, ref, started, received) {
				t.Fatal("a migrate receipt observed before the read was signed was refused", action)
			}
		}
	})
	t.Run("discard", func(t *testing.T) {
		a, command, raw, sig, descriptor := discardTransportFixture(t)
		ref, err := verifyWritableLifecycle(DiscardMigrationRead, raw, lifecycleSignature(t, DiscardMigrationRead, a, raw, sig), descriptor, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		receipt := journal.DiscardMigrationReceipt{
			Format: "portal.runner.discard-migration-receipt/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
			InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: "migrate",
			EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, NamespaceID: command.Payload.NamespaceID, ResourceID: command.Payload.ResourceID,
			MigrateEffectID: command.Payload.MigrateEffectID, MigratePayloadSHA256: command.Payload.MigratePayloadSHA256,
			MigrateReceiptSHA256: command.PayloadSHA256, State: "discarded", ObservedMS: old, SettledMS: old,
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(DiscardMigrationRead, body, ref, started, received) {
			t.Fatal("a discard receipt observed before the read was signed was refused")
		}
	})
	t.Run("restore", func(t *testing.T) {
		a, _, raw, sig, descriptor, receipt := restoreTransportFixture(t)
		ref, err := verifyWritableLifecycle(RestoreRead, raw, lifecycleSignature(t, RestoreRead, a, raw, sig), descriptor, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		receipt.ObservedMS, receipt.SettledMS = old, old
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(RestoreRead, body, ref, started, received) {
			t.Fatal("a restore receipt observed before the read was signed was refused")
		}
	})
	t.Run("a stale status is still refused", func(t *testing.T) {
		a, command, raw, sig, descriptor, _ := migrateTransportFixture(t)
		ref, err := verifyWritableLifecycle(MigrateDataRead, raw, lifecycleSignature(t, MigrateDataRead, a, raw, sig), descriptor, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		var status journal.MigrateDataStatus
		_ = json.Unmarshal(migrateStatusBody(t, "migrate-data", "migrate", "2", "orphaned", command.CommandHeader), &status)
		status.ObservedMS = old
		body, _ := json.Marshal(status)
		if validWritableLifecycleReply(MigrateDataRead, body, ref, started, received) {
			t.Fatal("a status observed before the read was signed was accepted")
		}
	})
}
