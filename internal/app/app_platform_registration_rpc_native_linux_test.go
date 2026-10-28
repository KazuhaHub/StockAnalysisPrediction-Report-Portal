//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
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

func testNativeRegistrationRPC(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, expectedDigest string) {
	t.Helper()
	ctx := context.Background()
	intent, err := s.preparePlatformBackendDelivery(ctx, lease)
	if err != nil {
		t.Fatal("Portal delivery preparation", err)
	}
	defer intent.registration.Close()
	if intent.command.DocumentSHA256 != expectedDigest || intent.applied {
		t.Fatal("Portal preparation changed registration material")
	}
	again, err := s.preparePlatformBackendDelivery(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer again.registration.Close()
	if again.command.EffectID != intent.command.EffectID || again.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("Portal replaced prepared delivery")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 {
		t.Fatal("Portal did not persist the single delivery intent")
	}
	public := os.NewFile(5, "public-registration-coordination")
	ready := os.NewFile(4, "registration-ready")
	if public == nil || ready == nil {
		t.Fatal("public coordination pipe unavailable")
	}
	defer public.Close()
	if err := json.NewEncoder(public).Encode(registrationRPCFixtureEnvelope{Raw: intent.raw, Signature: intent.signature}); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 2 {
		t.Fatal("registration failure fixture not ready")
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, intent.candidate.runtime, intent.candidate.authority, endpoint); err != nil {
		t.Fatal(err)
	}
	proof, err := platformDeliverBackendRegistration(ctx, endpoint, intent)
	if err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("failed root settlement minted RPC proof")
	}
	mustExec(t, s.st, `CREATE TRIGGER portal_delivery_fixture_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.backend_delivery' BEGIN SELECT RAISE(ABORT,'Portal delivery fixture failure'); END`)
	if err := json.NewEncoder(public).Encode(struct{ Published bool }{true}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 3 {
		t.Fatal("registration retry fixture not ready")
	}
	if _, err := s.dispatchPlatformBackendDelivery(ctx, lease, endpoint); err == nil {
		t.Fatal("Portal settlement failure did not roll back")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`, lease.id) != 1 || scalar[int64](t, s.st, `SELECT row_revision FROM app_platform_operations WHERE id=?`, lease.id) != lease.rowRevision {
		t.Fatal("Portal lost original retry intent")
	}
	mustExec(t, s.st, `DROP TRIGGER portal_delivery_fixture_failure`)
	committed, err := s.dispatchPlatformBackendDelivery(ctx, lease, endpoint)
	if err != nil || committed.rowRevision != lease.rowRevision+1 || committed.snapshot.Stage != lease.snapshot.Stage || committed.snapshot.Revision != lease.snapshot.Revision {
		t.Fatal("Portal delivery recovery", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND state='applied' AND observed_fence=admitted_fence AND receipt_json<>'{}'`, lease.id) != 1 {
		t.Fatal("Portal did not retain verified delivery receipt")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "pending" || scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE installation_id=?`, lease.installationID) != "candidate" {
		t.Fatal("delivery activated application")
	}
	if old, err := s.preparePlatformBackendDelivery(ctx, lease); err == nil {
		old.registration.Close()
		t.Fatal("stale Portal lease reused after settlement")
	}
	replay, err := s.dispatchPlatformBackendDelivery(ctx, committed, endpoint)
	if err != nil || replay.rowRevision != committed.rowRevision {
		t.Fatal("Portal delivery historical replay changed lease", err)
	}
	observed, err := s.preparePlatformBackendDelivery(ctx, committed)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.registration.Close()
	proof, err = platformDeliverBackendRegistration(ctx, endpoint, observed)
	if err != nil || !validPlatformBackendDeliveryObservation(proof, observed, endpoint) || !observed.applied || proof.Receipt() != observed.receipt {
		t.Fatal("Portal retained delivery differs from root", err)
	}
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	if _, err := s.commitPlatformBackendDelivery(ctx, committed, observed, endpoint, proof); err == nil {
		t.Fatal("restore pause accepted delivery settlement")
	}
	mustExec(t, s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	before := dumpOf(t, s.st)
	boot, err := s.probePlatformBackendBoot(ctx, committed, endpoint)
	receiptBytes, _ := json.Marshal(observed.receipt)
	if err != nil || len(boot.bootID) != 32 || boot.nativeID != observed.receipt.Claim.NativeID || boot.deliverySHA256 != platformDigest(receiptBytes) || boot.documentSHA256 != observed.command.DocumentSHA256 || boot.lease.rowRevision != committed.rowRevision || !samePlatformManagedRuntime(boot.runtime, observed.candidate.runtime) || boot.receivedAt.IsZero() {
		t.Fatal("actual registered backend boot", err)
	}
	repeated, err := s.probePlatformBackendBoot(ctx, committed, endpoint)
	if err != nil || repeated.bootID != boot.bootID || repeated.nativeID != boot.nativeID {
		t.Fatal("boot identity changed without process restart", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("boot probe changed activation or grants")
	}
	signer, err := observed.registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	exchanged, err := platformHandshakeBackend(ctx, endpoint, observed, challenge)
	if err != nil {
		t.Fatal(err)
	}
	other, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.VerifyAnswer(other, exchanged.Evidence().Answer, time.Now()); err == nil {
		t.Fatal("old actual answer replayed for a different challenge")
	}
	wrong := signer
	wrong.Key = bytes.Clone(signer.Key)
	defer clear(wrong.Key)
	wrong.Key[0] ^= 1
	bad, err := wrong.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if proof, err := platformHandshakeBackend(ctx, endpoint, observed, bad); err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("actual backend accepted wrong-key challenge")
	}
	if _, err := json.Marshal(boot); err == nil {
		t.Fatal("live boot proof serialized")
	}
	t.Log("MANAGED_BACKEND_BOOT_VERIFIED")
	testNativeBackendRequestRelay(t, endpoint, observed, boot)
	testNativeManagedViewerRequest(t, s, committed, endpoint, boot)
	t.Log("MANAGED_BACKEND_REGISTRATION_RPC")
	t.Log("MANAGED_BACKEND_DELIVERY_COMMITTED")
}
