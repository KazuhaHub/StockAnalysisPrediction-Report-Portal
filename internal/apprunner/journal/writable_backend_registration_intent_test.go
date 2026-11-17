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

func signWritableRegistrationFixture(t *testing.T, c control.WritableBackendRegistrationCommand) ([]byte, []byte) {
	t.Helper()
	e := enrollmentFixture(30)
	return signWritableRegistrationAuthority(t, c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint})
}
func signWritableRegistrationAuthority(t *testing.T, c control.WritableBackendRegistrationCommand, key ed25519.PrivateKey, a control.Authority) ([]byte, []byte) {
	t.Helper()
	var err error
	c.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignWritableBackendRegistration(c, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}
func writableRegistrationCommandFixture(t *testing.T, raw, sig []byte, r WritableCandidateRunningReceipt) control.WritableBackendRegistrationCommand {
	t.Helper()
	encoded, _ := json.Marshal(r)
	return control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: fixtureID(1900), CandidateRaw: bytes.Clone(raw), CandidateSignature: bytes.Clone(sig), RunningReceiptSHA256: candidateHash(encoded), DocumentSHA256: strings.Repeat("d", 64)}
}
func TestWritableRegistrationIntentExactSourceReplayReopenAndRenewal(t *testing.T) {
	s, candidateRaw, candidateSig, r := syntheticWritableRunningFixture(t)
	c := writableRegistrationCommandFixture(t, candidateRaw, candidateSig, r)
	raw, sig := signWritableRegistrationFixture(t, c)
	first, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig)
	if err != nil || first.Replayed || first.State != "prepared" || first.AdmittedFence != 1 {
		t.Fatal("registration intent", err)
	}
	view, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig)
	if err != nil || view.NativeID != r.NativeID || view.RunningReceiptSHA256 != c.RunningReceiptSHA256 || view.DocumentSHA256 != c.DocumentSHA256 || view.EffectID != c.EffectID {
		t.Fatal("exact intent view", err)
	}
	if replay, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig); err != nil || !replay.Replayed || replay.State != "prepared" {
		t.Fatal("intent replay", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 4 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 2 || scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 3 || scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='container'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND effect_number=3 AND receipt_json='{}' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("intent changed physical/source records")
	}
	if _, err := s.ReadWritableCandidateRunningReceipt(context.Background(), candidateRaw, candidateSig); err == nil {
		t.Fatal("old stage ignored later registration history")
	}
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("crossed readonly registration")
	}
	var path string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err != nil || got != view {
		t.Fatal("reopened view changed", err)
	}
	c.CandidateRaw, c.CandidateSignature = renewWritableRecoveryFixture(t, candidateRaw)
	raw, sig = signWritableRegistrationFixture(t, c)
	if got, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err != nil || got != view {
		t.Fatal("current renewal lost historical source", err)
	}
	oldRaw, oldSig := raw, sig
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	var start control.WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &start)
	var writer control.DataWriterCommand
	json.Unmarshal(start.WriterRaw, &writer)
	var source control.Command
	json.Unmarshal(writer.DataRaw, &source)
	now := time.Now().UnixMilli()
	source.ControlRevision, source.IssuedMS, source.ExpiresMS = "4", strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	writer.DataRaw, writer.DataSignature, err = control.Sign(source, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	start.WriterRaw, start.WriterSignature, err = control.SignDataWriter(writer, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.CandidateRaw, c.CandidateSignature, err = control.SignWritableCandidate(start, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig = signWritableRegistrationAuthority(t, c, key, a)
	if got, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err != nil || got != view {
		t.Fatal("current key renewal lost original registration source", err)
	}
	if _, err := s.ReadWritableBackendRegistrationIntent(context.Background(), oldRaw, oldSig); err == nil {
		t.Fatal("old key retained registration authority")
	}
}
func TestWritableRegistrationIntentRefusesStateAndHistoryDrift(t *testing.T) {
	for _, mode := range []string{"authority", "source_ack", "writer_ack", "start_ack", "native", "volume", "container_revision", "own_payload", "own_slot", "own_ack", "own_state", "own_time", "own_created", "extra_history", "wrong_receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, cr, cs, r := syntheticWritableRunningFixture(t)
			defer s.Close()
			c := writableRegistrationCommandFixture(t, cr, cs, r)
			raw, sig := signWritableRegistrationFixture(t, c)
			if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{
				"authority":          `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"source_ack":         `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='prepare-data'`,
				"writer_ack":         `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='prepare_data' AND kind='activate'`,
				"start_ack":          `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='start'`,
				"native":             `UPDATE runner_resources SET native_id='` + strings.Repeat("f", 64) + `' WHERE kind='container'`,
				"volume":             `UPDATE runner_resources SET revision=2 WHERE kind='volume'`,
				"container_revision": `UPDATE runner_resources SET revision=3 WHERE kind='container'`,
				"own_payload":        `UPDATE runner_commands SET payload_sha256='` + strings.Repeat("f", 64) + `' WHERE effect_id='` + c.EffectID + `'`,
				"own_slot":           `UPDATE runner_commands SET effect_number=4 WHERE effect_id='` + c.EffectID + `'`,
				"own_ack":            `UPDATE runner_commands SET state='applied',ack_attempted_ms=updated_ms WHERE effect_id='` + c.EffectID + `'`,
				"own_state":          `UPDATE runner_commands SET state='unknown' WHERE effect_id='` + c.EffectID + `'`,
				"own_time":           `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE effect_id='` + c.EffectID + `'`,
				"own_created":        `UPDATE runner_commands SET created_ms=0,updated_ms=0 WHERE effect_id='` + c.EffectID + `'`,
				"extra_history":      `INSERT INTO runner_commands SELECT '` + fixtureID(1901) + `',operation_id,installation_id,'activate',4,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,'applied','{}',NULL,created_ms,updated_ms FROM runner_commands WHERE effect_id='` + c.EffectID + `'`,
			}
			if mode == "wrong_receipt" {
				c.RunningReceiptSHA256 = strings.Repeat("f", 64)
				raw, sig = signWritableRegistrationFixture(t, c)
			} else {
				if _, err := s.db.Exec(queries[mode]); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err == nil || got.EffectID != "" {
				t.Fatal("drift accepted", mode)
			}
			if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig); err == nil {
				t.Fatal("drift replay accepted", mode)
			}
		})
	}
}

func TestWritableRegistrationCannotAdmitUnknownStart(t *testing.T) {
	s, raw, sig, _ := unknownWritableRecoveryFixture(t)
	defer s.Close()
	c := control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: fixtureID(1900), CandidateRaw: raw, CandidateSignature: sig, RunningReceiptSHA256: strings.Repeat("a", 64), DocumentSHA256: strings.Repeat("b", 64)}
	registration, signature := signWritableRegistrationFixture(t, c)
	if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), registration, signature); err == nil {
		t.Fatal("unknown process admitted registration")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`) != 3 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND effect_number=3`) != 0 {
		t.Fatal("failed source validation persisted new intent")
	}
}
