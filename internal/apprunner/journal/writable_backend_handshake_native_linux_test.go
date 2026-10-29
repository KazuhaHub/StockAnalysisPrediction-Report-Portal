//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"golang.org/x/sys/unix"
)

func assertWritableNativeHandshake(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, sig, descriptor, document []byte, sockets string, receipt WritableBackendRegistrationReceipt) {
	t.Helper()
	ctx := context.Background()
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	signer, err := registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	before := writableFixtureJournalSnapshot(t, s)
	path := writableRegistrationFixturePath(t, raw, sockets)
	stamp := writableRegistrationFixtureStamp(t, path, document)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.ExchangeWritableBackendHandshake(ctx, raw, sig, descriptor, challenge, native)
	if err != nil || evidence.NativeID != receipt.Claim.NativeID || evidence.PayloadSHA256 != receipt.Claim.PayloadSHA256 || evidence.ChallengeSHA256 != candidateHash([]byte(challenge)) {
		t.Fatal("actual writable handshake exchange", err)
	}
	boot, err := signer.VerifyAnswer(challenge, evidence.Answer, time.Now())
	if err != nil || boot == "" {
		t.Fatal("actual app did not prove delivered registration possession", err)
	}
	send := writableNativeRequestSender(t, s, native, raw, sig, descriptor, registration, boot)
	other, err := signer.NewChallenge(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.VerifyAnswer(other, evidence.Answer, time.Now()); err == nil {
		t.Fatal("answer reused for another challenge")
	}
	originalKey := append([]byte(nil), signer.Key...)
	defer clear(originalKey)
	signer.Key[0] ^= 1
	if _, err := signer.VerifyAnswer(challenge, evidence.Answer, time.Now()); err == nil {
		t.Fatal("wrong signing key verified backend possession")
	}
	copy(signer.Key, originalKey)
	if before != writableFixtureJournalSnapshot(t, s) || stamp != writableRegistrationFixtureStamp(t, path, document) {
		t.Fatal("handshake changed registration file or durable state")
	}
	// Same bytes/UID/mode/xattr still cannot replace the recorded inode. Restore
	// exactly the original owned test file before any later assertion/cleanup.
	held := path + ".held"
	if err := os.Rename(path, held); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		os.Remove(path)
		if err := os.Rename(held, path); err != nil {
			t.Error("restore owned registration", err)
		}
	}
	func() {
		defer restore()
		if err := os.WriteFile(path, document, 0400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65532, 65532); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
		if err := unix.Setxattr(path, "user.portal.registration", []byte(receipt.BindingSHA256), unix.XATTR_CREATE); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ExchangeWritableBackendHandshake(ctx, raw, sig, descriptor, challenge, native); err == nil || got.Format != "" {
			t.Fatal("same-content replacement inode accepted")
		}
		if got, err := send("POST"); err == nil || got.Format != "" {
			t.Fatal("request accepted same-content replacement inode")
		}
		t.Log("NATIVE_WRITABLE_REQUEST_FILE_REPLACEMENT_REFUSED")
	}()
	again, err := s.ExchangeWritableBackendHandshake(ctx, raw, sig, descriptor, other, native)
	if err != nil {
		t.Fatal("restored original file refused", err)
	}
	againBoot, err := signer.VerifyAnswer(other, again.Answer, time.Now())
	if err != nil || againBoot != boot {
		t.Fatal("same process boot identity changed", err)
	}
	assertWritableNativeRequestResult(t, send, receipt)
	// Rename changes ctime during deliberate fault injection; compare inode and
	// content/mode instead, while durable rows must remain byte-identical.
	restored := writableRegistrationFixtureStamp(t, path, document)
	if restored.ino != stamp.ino || restored.dev != stamp.dev || restored.mode != stamp.mode || before != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("handshake changed original file identity or durable journal")
	}
}
func assertWritableStoppedHandshake(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, sig, descriptor, document []byte, dockerSocket string, receipt WritableBackendRegistrationReceipt) {
	t.Helper()
	before := writableFixtureJournalSnapshot(t, s)
	candidateFixtureDocker(t, dockerSocket, "container", "stop", "--time", "10", receipt.Claim.NativeID)
	if got, err := s.ExchangeWritableBackendHandshake(context.Background(), raw, sig, descriptor, "a.b", native); err == nil || got.Format != "" {
		t.Fatal("stopped backend supplied handshake")
	}
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	send := writableNativeRequestSender(t, s, native, raw, sig, descriptor, registration, "stopped-fixture")
	if got, err := send("POST"); err == nil || got.Format != "" {
		t.Fatal("stopped writable backend supplied business reply")
	}
	t.Log("NATIVE_WRITABLE_STOPPED_REQUEST_REFUSED")
	if strings.TrimSpace(string(candidateFixtureDocker(t, dockerSocket, "container", "inspect", "--format", "{{.State.Running}}", receipt.Claim.NativeID))) != "false" || before != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("handshake restarted process or changed history")
	}
}
