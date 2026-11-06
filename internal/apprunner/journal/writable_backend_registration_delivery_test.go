//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func writableRegistrationClaimFixture(t *testing.T) (*Store, []byte, []byte, *writableRegistrationAttempt) {
	t.Helper()
	s, cr, cs, r := syntheticWritableRunningFixture(t)
	c := writableRegistrationCommandFixture(t, cr, cs, r)
	c.DocumentSHA256 = candidateHash([]byte("synthetic metadata fixture"))
	raw, sig := signWritableRegistrationFixture(t, c)
	if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.claimWritableRegistration(context.Background(), raw, sig)
	s.releaseDataExecution()
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, attempt
}
func TestWritableRegistrationClaimCannotRepeatOrInventNativeDelivery(t *testing.T) {
	s, raw, sig, attempt := writableRegistrationClaimFixture(t)
	defer s.Close()
	var m writableRegistrationClaim
	if !canonicalDataJSON(attempt.marker, &m) || !validWritableRegistrationClaim(m) || m.DocumentSHA256 != candidateHash([]byte("synthetic metadata fixture")) {
		t.Fatal("unbound claim")
	}
	if _, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err == nil {
		t.Fatal("claimed registration still prepared")
	}
	if _, err := s.claimWritableRegistration(context.Background(), raw, sig); err == nil {
		t.Fatal("unknown registration reclaimed")
	}
	if _, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), raw, sig); err == nil {
		t.Fatal("claim treated as delivered")
	}
	tx, f, err := s.beginWritableRegistration(context.Background(), raw, sig, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	_, expected, err := writableCandidateExpectedSnapshot(f.source, f.running.Claim, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.settleWritableRegistration(context.Background(), f, expected, driver.WritableBackendRegistrationObservation{}, false); err == nil || r.Format != "" {
		t.Fatal("zero native proof settled")
	}
	if _, err := s.executeWritableRegistration(context.Background(), raw, sig, nil, []byte("synthetic metadata fixture"), &driver.CandidateRuntime{}, nil); err == nil {
		t.Fatal("live unknown claim observed as expired")
	}
	if _, err := s.executeWritableRegistration(context.Background(), raw, sig, nil, []byte("synthetic metadata fixture"), &driver.CandidateRuntime{}, &writableRegistrationAttempt{owner: new(Store), marker: attempt.marker, deadline: attempt.deadline}); err == nil {
		t.Fatal("foreign owner used attempt")
	}
	if r, err := s.executeWritableRegistration(context.Background(), raw, sig, nil, []byte("synthetic metadata fixture"), &driver.CandidateRuntime{}, attempt); err == nil || r.Format != "" || !attempt.used.Load() {
		t.Fatal("non-native fixture supplied delivery", err)
	}
	if _, err := s.executeWritableRegistration(context.Background(), raw, sig, nil, []byte("synthetic metadata fixture"), &driver.CandidateRuntime{}, attempt); err == nil {
		t.Fatal("once-only attempt reused")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND effect_number=3 AND state='unknown'`) != 1 || scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='container'`) != 2 {
		t.Fatal("failed proof changed state")
	}
}
func TestWritableRegistrationClaimCommitFailurePrecedesNative(t *testing.T) {
	s, cr, cs, r := syntheticWritableRunningFixture(t)
	defer s.Close()
	c := writableRegistrationCommandFixture(t, cr, cs, r)
	raw, sig := signWritableRegistrationFixture(t, c)
	if _, err := s.AdmitWritableBackendRegistrationCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_reg_claim_failure BEFORE UPDATE ON runner_commands WHEN NEW.effect_id='` + c.EffectID + `' AND NEW.state='unknown' BEGIN SELECT RAISE(ABORT,'fixture reg claim failure');END`); err != nil {
		t.Fatal(err)
	}
	if attempt, err := s.claimWritableRegistration(context.Background(), raw, sig); err == nil || attempt != nil {
		t.Fatal("uncommitted claim returned attempt")
	}
	if _, err := s.ReadWritableBackendRegistrationIntent(context.Background(), raw, sig); err != nil {
		t.Fatal("claim rollback lost prepared intent", err)
	}
}

// Synthetic rows exercise current history/parser validation only; no opaque
// native evidence or physical successful delivery is manufactured here.
func TestWritableRegistrationHistoricalReceiptAndRecoveryWindows(t *testing.T) {
	s, raw, sig, attempt := writableRegistrationClaimFixture(t)
	defer s.Close()
	var m writableRegistrationClaim
	json.Unmarshal([]byte(attempt.marker), &m)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	r := WritableBackendRegistrationReceipt{Format: writableRegistrationReceiptFormat, Claim: m, BindingSHA256: writableRegistrationBinding(m), Device: "1", Inode: "2", ObservedMS: now, SettledMS: now}
	encoded, _ := json.Marshal(r)
	if _, err := ParseWritableBackendRegistrationReceipt(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(encoded), now, m.EffectID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), raw, sig); err != nil || got != r {
		t.Fatal("normal historical receipt", err)
	}
	var c control.WritableBackendRegistrationCommand
	json.Unmarshal(raw, &c)
	c.CandidateRaw, c.CandidateSignature = renewWritableRecoveryFixture(t, c.CandidateRaw)
	renewed, renewedSig := signWritableRegistrationFixture(t, c)
	if got, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), renewed, renewedSig); err != nil || got != r {
		t.Fatal("current transport rewrote historical file binding", err)
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	m.ExpiresMS = strconv.FormatInt(claimed+1, 10)
	if delay := time.Until(time.UnixMilli(claimed + 2)); delay > 0 {
		time.Sleep(delay)
	}
	c.CandidateRaw, c.CandidateSignature = renewWritableRecoveryFixture(t, c.CandidateRaw)
	renewed, renewedSig = signWritableRegistrationFixture(t, c)
	var start control.WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &start)
	var writer control.DataWriterCommand
	json.Unmarshal(start.WriterRaw, &writer)
	var ref control.Command
	json.Unmarshal(writer.DataRaw, &ref)
	now = strconv.FormatInt(time.Now().UnixMilli(), 10)
	recovered := WritableBackendRegistrationReceipt{Format: writableRegistrationRecoveredFormat, Claim: m, BindingSHA256: writableRegistrationBinding(m), Device: "1", Inode: "2", ObservedMS: now, SettledMS: now, RecoveryEnvelopeSHA256: candidateHash(renewed), RecoveryControlRevision: ref.ControlRevision, RecoveryIssuedMS: ref.IssuedMS, RecoveryExpiresMS: ref.ExpiresMS}
	encoded, _ = json.Marshal(recovered)
	if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE effect_id=?`, string(encoded), now, m.EffectID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), renewed, renewedSig); err != nil || got != recovered {
		t.Fatal("recovered historical receipt", err)
	}
	for _, mode := range []string{"normal_format", "binding", "source", "native", "inode", "time", "early_recovery", "wide_recovery", "future_revision", "old_revision"} {
		copy := recovered
		switch mode {
		case "normal_format":
			copy.Format = writableRegistrationReceiptFormat
		case "binding":
			copy.BindingSHA256 = strings.Repeat("f", 64)
		case "source":
			copy.Claim.SourceSHA256 = strings.Repeat("f", 64)
			copy.BindingSHA256 = writableRegistrationBinding(copy.Claim)
		case "native":
			copy.Claim.NativeID = strings.Repeat("f", 64)
			copy.BindingSHA256 = writableRegistrationBinding(copy.Claim)
		case "inode":
			copy.Inode = "0"
		case "time":
			copy.SettledMS = copy.RecoveryExpiresMS
		case "early_recovery":
			copy.RecoveryIssuedMS = copy.Claim.ExpiresMS
		case "wide_recovery":
			issued, _ := strconv.ParseInt(copy.RecoveryIssuedMS, 10, 64)
			copy.RecoveryExpiresMS = strconv.FormatInt(issued+30001, 10)
		case "future_revision":
			copy.RecoveryControlRevision = "4"
		case "old_revision":
			copy.RecoveryControlRevision = "2"
		}
		encoded, _ := json.Marshal(copy)
		if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(encoded), m.EffectID); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), renewed, renewedSig); err == nil || got.Format != "" {
			t.Fatal("altered registration receipt accepted", mode)
		}
	}
}
func TestWritableRegistrationDeliveryRefusesCurrentDrift(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "source", "volume", "resource", "claim", "time"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, _ := writableRegistrationClaimFixture(t)
			defer s.Close()
			queries := map[string]string{
				"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='activate' AND effect_number=3`,
				"source":    `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
				"volume":    `UPDATE runner_resources SET revision=2 WHERE kind='volume'`,
				"resource":  `UPDATE runner_resources SET revision=3 WHERE kind='container'`,
				"claim":     `UPDATE runner_commands SET receipt_json='{}' WHERE stage='activate' AND effect_number=3`,
				"time":      `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='activate' AND effect_number=3`,
			}
			if _, err := s.db.Exec(queries[mode]); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.beginWritableRegistration(context.Background(), raw, sig, "unknown"); err == nil {
				t.Fatal("drift accepted", mode)
			}
		})
	}
}

func TestWritableRegistrationPostflightRejectsChangedCurrentFactsBeforeProof(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "source", "resource", "claim", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, _ := writableRegistrationClaimFixture(t)
			defer s.Close()
			tx, before, err := s.beginWritableRegistration(context.Background(), raw, sig, "unknown")
			if err != nil {
				t.Fatal(err)
			}
			tx.Rollback()
			_, expected, err := writableCandidateExpectedSnapshot(before.source, before.running.Claim, nil)
			if err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{
				"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='activate' AND effect_number=3`,
				"source":    `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
				"resource":  `UPDATE runner_resources SET revision=3 WHERE kind='container'`,
				"claim":     `UPDATE runner_commands SET receipt_json='{}' WHERE stage='activate' AND effect_number=3`,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			} else {
				if _, err := s.db.Exec(queries[mode]); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.settleWritableRegistration(ctx, before, expected, driver.WritableBackendRegistrationObservation{}, false); err == nil || got.Format != "" {
				t.Fatal("changed current facts settled", mode)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND effect_number=3 AND state='applied'`) != 0 {
				t.Fatal("failed postflight marked delivered")
			}
		})
	}
}
