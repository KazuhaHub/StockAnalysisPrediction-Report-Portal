//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// importReadSignature signs the exact bytes of an import command as a read-receipt.
func importReadSignature(t *testing.T, raw []byte) []byte {
	t.Helper()
	signature, err := control.SignImportCheckpointRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

// updateStartReadSignature signs the exact bytes of an update-start command as a read-receipt.
func updateStartReadSignature(t *testing.T, raw []byte) []byte {
	t.Helper()
	signature, err := control.SignUpdateStartRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

// A captured read-receipt cannot be replayed as an execute, and an execute signature is not a read: the
// runner verifies the signature purpose per action.
func TestImportReadAndExecuteSignaturesAreNotInterchangeable(t *testing.T) {
	store, _, raw, executeSignature := importStoreFixture(t)
	readSignature := importReadSignature(t, raw)
	if _, replay, err := store.beginImportCheckpoint(context.Background(), raw, readSignature, importDataPin); err == nil || replay {
		t.Fatalf("a read signature was admitted as an import execute: %v", err)
	}
	if rows := scalarJournalInt(t, store.db, `SELECT COUNT(*) FROM runner_commands`); rows != 0 {
		t.Fatalf("a refused execute wrote a row: %d", rows)
	}
	if _, err := store.ReadImportCheckpointOrAbsence(context.Background(), raw, executeSignature); err == nil {
		t.Fatal("an execute signature was answered as an import read")
	}
	if result, err := store.ReadImportCheckpointOrAbsence(context.Background(), raw, readSignature); err != nil {
		t.Fatalf("a read signature was refused: %#v %v", result, err)
	}
}

func TestRestoreStartReadAndExecuteSignaturesAreNotInterchangeable(t *testing.T) {
	store, _, command := importedStartFixture(t)
	raw, executeSignature := signImportedStart(t, command)
	readSignature := updateStartReadSignature(t, raw)
	if _, err := store.ReserveUpdateStart(context.Background(), raw, readSignature); err == nil {
		t.Fatal("a read signature was admitted as a restore-start reservation")
	}
	if _, err := store.ClaimUpdateStart(context.Background(), raw, readSignature); err == nil {
		t.Fatal("a read signature was admitted as a restore-start claim")
	}
	if _, err := store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, executeSignature); err == nil {
		t.Fatal("an execute signature was answered as a restore-start read")
	}
	if result, err := store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, readSignature); err != nil {
		t.Fatalf("a read signature was refused: %#v %v", result, err)
	}
}
