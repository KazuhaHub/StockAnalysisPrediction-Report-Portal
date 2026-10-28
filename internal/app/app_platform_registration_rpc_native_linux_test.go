//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type registrationRPCFixtureEnvelope struct{ Raw, Signature []byte }

// The root parent receives public intent/synchronization only. Secret bytes must
// travel through the authenticated production transport entry point, never here.
func observeNativeRegistrationRPCFixture(t *testing.T, ctx context.Context, reader io.Reader, ready io.Writer, sockets string, a control.Authority, store *journal.Store) func() {
	t.Helper()
	var message registrationRPCFixtureEnvelope
	decoder := json.NewDecoder(io.LimitReader(reader, control.MaxCommandBytes*2))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		t.Fatal("public registration command missing", err)
	}
	verified, err := control.VerifyBackendRegistration(message.Raw, message.Signature, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	ref := verified.Candidate()
	c := verified.Command()
	probe, err := sql.Open("sqlite", filepath.Join(os.Getenv("APP_DATA_JOURNAL_CONTROL"), "journal.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { probe.Close() })
	if _, err := probe.Exec(`CREATE TRIGGER registration_delivery_fixture_failure BEFORE UPDATE OF state ON runner_commands WHEN NEW.stage='activate' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'registration fixture settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := ready.Write([]byte{2}); err != nil {
		t.Fatal(err)
	}
	var event struct{ Published bool }
	if err := decoder.Decode(&event); err != nil || !event.Published {
		t.Fatal("failed-settlement RPC signal missing", err)
	}
	path := filepath.Join(sockets, ref.InstallationID, ref.TargetGeneration, "broker", "registration.json")
	before, err := os.Lstat(path)
	if err != nil || before.Mode().Perm() != 0400 {
		t.Fatal("RPC did not publish protected file", err)
	}
	var state string
	if err := probe.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&state); err != nil || state != "unknown" {
		t.Fatal("RPC lost durable uncertainty", err)
	}
	if _, err := probe.Exec(`DROP TRIGGER registration_delivery_fixture_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := ready.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	return func() {
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("RPC reconciliation replaced original file", err)
		}
		receipt, err := store.ReadBackendRegistrationReceipt(ctx, message.Raw, message.Signature)
		if err != nil || !receipt.MatchesCommand(c) {
			t.Fatal("RPC did not settle exact delivery receipt", err)
		}
		t.Log("BACKEND_REGISTRATION_INTENT_ADMITTED")
		t.Log("BACKEND_REGISTRATION_DELIVERY_RECONCILED")
		t.Log("BACKEND_REGISTRATION_RPC_RECONCILED")
	}
}

func testNativeRegistrationRPC(t *testing.T, endpoint platformRuntimeEndpoint, intent platformCandidateIntent, descriptor, document []byte) {
	t.Helper()
	id, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	c := control.BackendRegistrationCommand{Format: control.BackendRegistrationFormat, EffectID: id, CandidateRaw: intent.raw, CandidateSignature: intent.signature, DocumentSHA256: platformDigest(document)}
	c.PayloadSHA256, err = control.BackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	// This is a sample controller intent. Portal operation-effect persistence and
	// normal scheduler integration are deliberately not fabricated by this fixture.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	defer clear(key)
	raw, sig, err := control.SignBackendRegistration(c, key, intent.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	public := os.NewFile(5, "public-registration-coordination")
	ready := os.NewFile(4, "registration-ready")
	if public == nil || ready == nil {
		t.Fatal("public coordination pipe unavailable")
	}
	defer public.Close()
	if err := json.NewEncoder(public).Encode(registrationRPCFixtureEnvelope{Raw: raw, Signature: sig}); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 2 {
		t.Fatal("registration failure fixture not ready")
	}
	proof, err := transport.DeliverControllerRegistration(context.Background(), endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, sig, intent.authority, descriptor, document)
	if err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("failed root settlement minted RPC proof")
	}
	if err := json.NewEncoder(public).Encode(struct{ Published bool }{true}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 3 {
		t.Fatal("registration retry fixture not ready")
	}
	proof, err = transport.DeliverControllerRegistration(context.Background(), endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, sig, intent.authority, descriptor, document)
	if err != nil || !proof.Endpoint().RootController || proof.Endpoint().RunnerUID != 0 || proof.Endpoint().ControllerUID != 41001 || proof.Endpoint().ControllerGID != 41001 || proof.Endpoint().Path != endpoint.path || !proof.Receipt().MatchesCommand(c) || proof.CommandSHA256() != platformDigest(raw) {
		t.Fatal("root registration RPC recovery", err)
	}
	receipt := proof.Receipt()
	historical, err := transport.DeliverControllerRegistration(context.Background(), endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, sig, intent.authority, descriptor, document)
	if err != nil || historical.Receipt() != receipt {
		t.Fatal("registration RPC historical replay", err)
	}
	t.Log("MANAGED_BACKEND_REGISTRATION_RPC")
}
