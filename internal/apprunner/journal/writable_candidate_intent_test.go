//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func writableIntentFixture(t *testing.T, writerRaw, writerSig []byte, receipt DataWriterReceipt) control.WritableCandidateCommand {
	t.Helper()
	encoded, _ := json.Marshal(receipt)
	return control.WritableCandidateCommand{Format: control.WritableCandidateFormat, EffectID: fixtureID(1600), WriterRaw: bytes.Clone(writerRaw), WriterSignature: bytes.Clone(writerSig), WriterReceiptSHA256: candidateHash(encoded), SandboxProfile: control.WritableCandidateSandboxProfile}
}
func signWritableIntent(t *testing.T, c control.WritableCandidateCommand) ([]byte, []byte) {
	t.Helper()
	e := enrollmentFixture(30)
	return signWritableIntentAuthority(t, c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint})
}
func signWritableIntentAuthority(t *testing.T, c control.WritableCandidateCommand, key ed25519.PrivateKey, a control.Authority) ([]byte, []byte) {
	t.Helper()
	var err error
	c.PayloadSHA256, err = control.WritableCandidateLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignWritableCandidate(c, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func TestWritableCandidateIntentRequiresCompletedWriterAndReopens(t *testing.T) {
	s, writerRaw, writerSig, receipt := syntheticAppliedWriterFixture(t)
	var directory string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &directory); err != nil {
		t.Fatal(err)
	}
	c := writableIntentFixture(t, writerRaw, writerSig, receipt)
	raw, sig := signWritableIntent(t, c)
	first, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig)
	if err != nil || first.Replayed || first.State != "prepared" || first.AdmittedFence != 1 {
		t.Fatal("writable intent", first, err)
	}
	view, err := s.ReadWritableCandidateIntent(context.Background(), raw, sig)
	if err != nil || view.EffectID != c.EffectID || view.ResourceID != receipt.Claim.ResourceID || view.WriterReceiptSHA256 != c.WriterReceiptSHA256 || view.InstallationFence != "1" {
		t.Fatal("current writable intent", err)
	}
	if replay, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil || !replay.Replayed {
		t.Fatal("writable replay", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 || scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 3 {
		t.Fatal("intent created container or rewrote writer state")
	}
	if _, err := s.ClaimCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("legacy readonly claim accepted writable intent")
	}
	if _, err := s.ReadDataWriterReceipt(context.Background(), writerRaw, writerSig); err == nil {
		t.Fatal("old pre-start writer reader ignored later start intent")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The journal filename is implementation-owned; its parent is the configured
	// control directory. Reopen metadata, never reconstruct an execution claim.
	reopened, err := Open(filepath.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ReadWritableCandidateIntent(context.Background(), raw, sig); err != nil || got != view {
		t.Fatal("reopened intent changed", err, directory)
	}
}

func TestWritableCandidateIntentRefusesSourceAndHistoryDrift(t *testing.T) {
	for _, mode := range []string{"writer_unknown", "writer_ack", "writer_time", "writer_receipt", "source_ack", "source_time", "volume_revision", "volume_time", "authority", "receipt_binding", "future_container", "other_command"} {
		t.Run(mode, func(t *testing.T) {
			s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
			defer s.Close()
			c := writableIntentFixture(t, wraw, wsig, receipt)
			queries := map[string]string{
				"writer_unknown":   `UPDATE runner_commands SET state='unknown' WHERE kind='activate'`,
				"writer_ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
				"writer_time":      `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE kind='activate'`,
				"writer_receipt":   `UPDATE runner_commands SET receipt_json='{}' WHERE kind='activate'`,
				"source_ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='prepare-data'`,
				"source_time":      `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE kind='prepare-data'`,
				"volume_revision":  `UPDATE runner_resources SET revision=2`,
				"volume_time":      `UPDATE runner_resources SET observed_ms=observed_ms+1`,
				"authority":        `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"future_container": `UPDATE runner_resources SET id='` + receipt.Claim.ResourceID + `',kind='container'`,
				"other_command":    `INSERT INTO runner_commands SELECT '` + fixtureID(1602) + `',operation_id,installation_id,'probe',1,'probe',payload_sha256,target_generation,controller_epoch,admitted_fence,'applied','{}',NULL,created_ms,updated_ms FROM runner_commands WHERE kind='activate'`,
			}
			if mode == "receipt_binding" {
				c.WriterReceiptSHA256 = strings.Repeat("f", 64)
			} else if _, err := s.db.Exec(queries[mode]); err != nil {
				t.Fatal(err)
			}
			raw, sig := signWritableIntent(t, c)
			if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("invalid source admitted", mode)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start'`) != 0 {
				t.Fatal("failed admission persisted start")
			}
		})
	}
}

func TestWritableCandidateIntentCurrentKeyPreservesSource(t *testing.T) {
	s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
	defer s.Close()
	c := writableIntentFixture(t, wraw, wsig, receipt)
	raw, sig := signWritableIntent(t, c)
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	var w control.DataWriterCommand
	json.Unmarshal(wraw, &w)
	var ref control.Command
	json.Unmarshal(w.DataRaw, &ref)
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	ref.ControlRevision = "4"
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	var err error
	w.DataRaw, w.DataSignature, err = control.Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.WriterRaw, c.WriterSignature, err = control.SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWritableIntentAuthority(t, c, key, a)
	if view, err := s.ReadWritableCandidateIntent(context.Background(), raw, sig); err != nil || view.ControlRevision != "4" || view.WriterReceiptSHA256 != c.WriterReceiptSHA256 {
		t.Fatal("current key lost source history", err)
	}
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal("current-key replay", err)
	}
	if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 3 {
		t.Fatal("current-key read rewrote native source")
	}
	ref.InstallationFence = "2"
	w.DataRaw, w.DataSignature, err = control.Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.WriterRaw, c.WriterSignature, err = control.SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWritableIntentAuthority(t, c, key, a)
	if _, err := s.ReadWritableCandidateIntent(context.Background(), raw, sig); err == nil {
		t.Fatal("higher-fence envelope silently took over writer")
	}
}

func TestWritableCandidateIntentDoesNotBecomeExecutionAuthority(t *testing.T) {
	s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
	defer s.Close()
	c := writableIntentFixture(t, wraw, wsig, receipt)
	raw, sig := signWritableIntent(t, c)
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	replacement := c
	replacement.EffectID = fixtureID(1601)
	otherRaw, otherSig := signWritableIntent(t, replacement)
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), otherRaw, otherSig); err == nil {
		t.Fatal("duplicate start identity admitted")
	}
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='unknown' WHERE stage='start'`); err != nil {
		t.Fatal(err)
	}
	if view, err := s.ReadWritableCandidateIntent(context.Background(), raw, sig); err == nil || view != (WritableCandidateIntent{}) {
		t.Fatal("unknown start reported prepared")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start'`) != 1 {
		t.Fatal("duplicate intent persisted")
	}
}
