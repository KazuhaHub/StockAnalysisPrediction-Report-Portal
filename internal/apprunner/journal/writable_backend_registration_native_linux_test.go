//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func renewWritableRegistrationEnvelope(t *testing.T, raw []byte, window time.Duration) ([]byte, []byte) {
	t.Helper()
	var c control.WritableBackendRegistrationCommand
	json.Unmarshal(raw, &c)
	var start control.WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &start)
	var writer control.DataWriterCommand
	json.Unmarshal(start.WriterRaw, &writer)
	var ref control.Command
	json.Unmarshal(writer.DataRaw, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+window.Milliseconds(), 10)
	writer.DataRaw, writer.DataSignature = signRunnerCommand(t, ref)
	start.WriterRaw, start.WriterSignature = signWriterFixture(t, writer)
	c.CandidateRaw, c.CandidateSignature = signWritableIntent(t, start)
	return signWritableRegistrationFixture(t, c)
}
func writableRegistrationFixturePath(t *testing.T, raw []byte, sockets string) string {
	t.Helper()
	var c control.WritableBackendRegistrationCommand
	json.Unmarshal(raw, &c)
	var start control.WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &start)
	var writer control.DataWriterCommand
	json.Unmarshal(start.WriterRaw, &writer)
	var ref control.Command
	json.Unmarshal(writer.DataRaw, &ref)
	return filepath.Join(sockets, ref.InstallationID, ref.TargetGeneration, "broker", "registration.json")
}

type registrationNativeFileStamp struct {
	dev, ino       uint64
	uid, gid, mode uint32
	size           int64
	ctime, mtime   syscall.Timespec
}

func writableRegistrationFixtureStamp(t *testing.T, path string, document []byte) registrationNativeFileStamp {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal("protected registration absent", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || st.Uid != 65532 || st.Gid != 65532 || st.Nlink != 1 || st.Size != int64(len(document)) {
		t.Fatal("unsafe native registration file")
	}
	actual, err := os.ReadFile(path)
	defer clear(actual)
	if err != nil || !bytes.Equal(actual, document) {
		t.Fatal("registration content mismatch")
	}
	return registrationNativeFileStamp{dev: uint64(st.Dev), ino: st.Ino, uid: st.Uid, gid: st.Gid, mode: st.Mode, size: st.Size, ctime: st.Ctim, mtime: st.Mtim}
}
func assertWritableRegistrationNativeDelivery(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, descriptor, document []byte, sockets, journalPath, dockerSocket, rootPath string, root *data.Root, pin data.RootIdentity) (*Store, *driver.CandidateRuntime) {
	t.Helper()
	ctx := context.Background()
	raw, sig := renewWritableRegistrationEnvelope(t, raw, 5*time.Second)
	var c control.WritableBackendRegistrationCommand
	json.Unmarshal(raw, &c)
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_writable_registration_commit_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture writable registration receipt failure');END`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); !errors.Is(err, ErrWritableRegistrationUncertain) || !strings.Contains(err.Error(), "fixture writable registration receipt failure") || r.Format != "" {
		t.Fatal("actual writable registration commit failure", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_writable_registration_commit_failure`); err != nil {
		t.Fatal(err)
	}
	path := writableRegistrationFixturePath(t, raw, sockets)
	stamp := writableRegistrationFixtureStamp(t, path, document)
	var encoded string
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=? AND state='unknown'`, c.EffectID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var marker writableRegistrationClaim
	if !canonicalDataJSON(encoded, &marker) {
		t.Fatal("missing original registration claim")
	}
	if _, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); err == nil {
		t.Fatal("live unknown registration recovered")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	native, err = driver.OpenCandidateRuntime(ctx, dockerSocket, rootPath, sockets, root, pin)
	if err != nil {
		t.Fatal(err)
	}
	expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
	if delay := time.Until(time.UnixMilli(expires + 1)); delay > 0 {
		time.Sleep(delay)
	}
	raw, sig = renewWritableRegistrationEnvelope(t, raw, 25*time.Second)
	before := writableFixtureJournalSnapshot(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_writable_registration_recovery_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture writable registration recovery failure');END`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); err == nil || !strings.Contains(err.Error(), "fixture writable registration recovery failure") || r.Format != "" {
		t.Fatal("recovery commit failure bypassed", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_writable_registration_recovery_failure`); err != nil {
		t.Fatal(err)
	}
	if before != writableFixtureJournalSnapshot(t, s) || stamp != writableRegistrationFixtureStamp(t, path, document) {
		t.Fatal("failed recovery changed file or durable state")
	}
	recovered, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native)
	if err != nil || recovered.Format != writableRegistrationRecoveredFormat || recovered.Claim != marker || recovered.Inode != strconv.FormatUint(stamp.ino, 10) || recovered.Device != strconv.FormatUint(stamp.dev, 10) || recovered.RecoveryEnvelopeSHA256 != candidateHash(raw) {
		t.Fatal("actual protected file recovery", err)
	}
	if stamp != writableRegistrationFixtureStamp(t, path, document) {
		t.Fatal("read-only recovery rewrote protected file")
	}
	got, err := s.ReadWritableBackendRegistrationReceipt(ctx, raw, sig)
	if err != nil || got != recovered {
		t.Fatal("recovered registration read", err)
	}
	if replay, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); err != nil || replay != recovered {
		t.Fatal("historical registration replay", err)
	}
	if stamp != writableRegistrationFixtureStamp(t, path, document) {
		t.Fatal("applied replay rewrote protected file")
	}
	assertWritableNativeHandshake(t, s, native, raw, sig, descriptor, document, sockets, recovered)
	return s, native
}
func assertWritableRegistrationMissingFile(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, descriptor, document []byte, sockets string) {
	t.Helper()
	ctx := context.Background()
	raw, sig := renewWritableRegistrationEnvelope(t, raw, time.Second)
	if err := s.acquireDataExecution(ctx); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.claimWritableRegistration(ctx, raw, sig)
	s.releaseDataExecution()
	if err != nil {
		t.Fatal(err)
	}
	var marker writableRegistrationClaim
	json.Unmarshal([]byte(attempt.marker), &marker)
	path := writableRegistrationFixturePath(t, raw, sockets)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("missing fixture already has a registration file")
	}
	expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
	if delay := time.Until(time.UnixMilli(expires + 1)); delay > 0 {
		time.Sleep(delay)
	}
	raw, sig = renewWritableRegistrationEnvelope(t, raw, 25*time.Second)
	before := writableFixtureJournalSnapshot(t, s)
	if r, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); !errors.Is(err, ErrWritableRegistrationUncertain) || r.Format != "" {
		t.Fatal("missing file recovery did not remain unknown", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("observation recovery created missing registration")
	}
	if before != writableFixtureJournalSnapshot(t, s) {
		t.Fatal("missing file recovery changed durable state")
	}
}

func assertWritableRegistrationNormalDelivery(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, sig, descriptor, document []byte, sockets string) {
	t.Helper()
	ctx := context.Background()
	path := writableRegistrationFixturePath(t, raw, sockets)
	bad := append(bytes.Clone(document), ' ')
	defer clear(bad)
	if _, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, bad, native); err == nil {
		t.Fatal("changed private bytes delivered")
	}
	if _, err := s.ReadWritableBackendRegistrationIntent(ctx, raw, sig); err != nil {
		t.Fatal("invalid document consumed prepared intent", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("invalid document published a file")
	}
	delivered, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native)
	if err != nil || delivered.Format != writableRegistrationReceiptFormat {
		t.Fatal("normal writable registration delivery", err)
	}
	stamp := writableRegistrationFixtureStamp(t, path, document)
	if delivered.Device != strconv.FormatUint(stamp.dev, 10) || delivered.Inode != strconv.FormatUint(stamp.ino, 10) {
		t.Fatal("normal receipt did not identify actual file")
	}
	if got, err := s.ReadWritableBackendRegistrationReceipt(ctx, raw, sig); err != nil || got != delivered {
		t.Fatal("normal file historical read", err)
	}
	if replay, err := s.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, native); err != nil || replay != delivered {
		t.Fatal("normal historical replay", err)
	}
	if stamp != writableRegistrationFixtureStamp(t, path, document) {
		t.Fatal("normal replay rewrote file")
	}
	assertWritableNativeHandshake(t, s, native, raw, sig, descriptor, document, sockets, delivered)
	assertWritableStoppedHandshake(t, s, native, raw, sig, descriptor, os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), delivered)
}
