//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func testNativePortalWritableBoot(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, i platformWritableRegistrationIntent) {
	t.Helper()
	ctx := context.Background()
	before := dumpOf(t, s.st)
	boot, err := s.probePlatformWritableBackendBoot(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("actual writable host boot verification", err)
	}
	if boot.bootID == "" || boot.nativeID != i.start.running.NativeID || boot.documentSHA256 != i.command.DocumentSHA256 || boot.startSHA256 != i.command.RunningReceiptSHA256 || boot.lease.rowRevision != lease.rowRevision || !samePlatformManagedRuntime(boot.runtime, i.start.writer.runtime) {
		t.Fatal("host boot changed source identity")
	}
	again, err := s.probePlatformWritableBackendBoot(ctx, lease, endpoint)
	if err != nil || again.bootID != boot.bootID || again.nativeID != boot.nativeID || again.deliverySHA256 != boot.deliverySHA256 {
		t.Fatal("same running backend changed boot identity", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("boot verification granted or mutated state")
	}
	t.Log("PORTAL_WRITABLE_BACKEND_BOOT_VERIFIED")
	signer, err := i.registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := platformHandshakeWritableBackend(ctx, endpoint, i, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if !validPlatformWritableHandshakeObservation(proof, i, endpoint, challenge) {
		t.Fatal("actual writable handshake correlation")
	}
	if _, err := s.finishPlatformWritableBackendBoot(ctx, lease, i, endpoint, challenge+"x", proof); err == nil {
		t.Fatal("foreign challenge became boot")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, i.start.writer.runtime.id)
	disabled := dumpOf(t, s.st)
	if _, err := s.finishPlatformWritableBackendBoot(ctx, lease, i, endpoint, challenge, proof); err == nil {
		t.Fatal("native handshake crossed current runtime disablement")
	}
	if !bytes.Equal(stripHeader(disabled), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("revoked source boot changed storage")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1 WHERE id=?`, i.start.writer.runtime.id)
	t.Log("PORTAL_WRITABLE_BACKEND_BOOT_CURRENT_SOURCE_REFUSED")
}
