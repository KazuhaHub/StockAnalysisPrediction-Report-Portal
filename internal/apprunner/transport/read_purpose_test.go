package transport

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// lifecycleSignature is the signature a command carries for the action: a read-receipt is signed under
// its own purpose, every other action under the command's own.
func lifecycleSignature(t *testing.T, action WritableLifecycleAction, a control.Authority, raw, execute []byte) []byte {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	now := time.Now().UnixMilli()
	switch action {
	case ImportCheckpointRead:
		signature, err := control.SignImportCheckpointRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	case LoadImageRead:
		signature, err := control.SignLoadImageRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	case MigrateDataRead:
		signature, err := control.SignMigrateDataRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	case DiscardMigrationRead:
		signature, err := control.SignDiscardMigrationRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	case RestoreRead:
		signature, err := control.SignRestoreRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	case UpdateStartReceipt:
		signature, err := control.SignUpdateStartRead(raw, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	}
	return execute
}

// The transport verifies the signature purpose per action: a read signature is refused as an execute and
// an execute signature as a read, for the import and the restore-start.
func TestWritableLifecycleReadAndExecuteSignaturesAreNotInterchangeable(t *testing.T) {
	a, _, raw, execute, descriptor, _ := importTransportFixture(t)
	now := time.Now().UnixMilli()
	read := lifecycleSignature(t, ImportCheckpointRead, a, raw, execute)
	if _, err := verifyWritableLifecycle(ImportCheckpointExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("an import read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycleSettlement(ImportCheckpointExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("an import read signature was accepted as an execute settlement")
	}
	if _, err := verifyWritableLifecycle(ImportCheckpointRead, raw, execute, descriptor, a, now); err == nil {
		t.Fatal("an import execute signature was accepted as a read")
	}
	a, _, raw, execute, descriptor = restoreStartTransportFixture(t)
	read = lifecycleSignature(t, UpdateStartReceipt, a, raw, execute)
	if _, err := verifyWritableLifecycle(UpdateStartExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a restore-start read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(UpdateStartReceipt, raw, execute, descriptor, a, now); err == nil {
		t.Fatal("a restore-start execute signature was accepted as a read")
	}
}
