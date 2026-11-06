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

func renewWritableRecoveryFixture(t *testing.T, raw []byte) ([]byte, []byte) {
	t.Helper()
	var c control.WritableCandidateCommand
	json.Unmarshal(raw, &c)
	var w control.DataWriterCommand
	json.Unmarshal(c.WriterRaw, &w)
	var ref control.Command
	json.Unmarshal(w.DataRaw, &ref)
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	w.DataRaw, w.DataSignature = signRunnerCommand(t, ref)
	c.WriterRaw, c.WriterSignature = signWriterFixture(t, w)
	return signWritableIntent(t, c)
}

// Historical metadata fixture, never native authority. Restore an unknown,
// bound row after an expired synthetic claim to test independent recovery guards.
func unknownWritableRecoveryFixture(t *testing.T) (*Store, []byte, []byte, writableCandidateClaimMarker) {
	t.Helper()
	s, raw, _, r := syntheticWritableRunningFixture(t)
	m := r.Claim
	bound, _ := strconv.ParseInt(m.PreflightBoundMS, 10, 64)
	m.ExpiresMS = strconv.FormatInt(bound+1, 10)
	encoded, _ := json.Marshal(m)
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE stage='start'`, string(encoded), bound); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET native_id=?,revision=1,observed_state='unknown',observed_ms=NULL,updated_ms=created_ms WHERE kind='container'`, writableCandidatePendingPrefix+m.ResourceID); err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(time.UnixMilli(bound + 2)); delay > 0 {
		time.Sleep(delay)
	}
	raw, sig := renewWritableRecoveryFixture(t, raw)
	return s, raw, sig, m
}
func TestWritableCandidateRecoveryRequiresFreshExpiredBoundClaim(t *testing.T) {
	s, raw, sig, m := unknownWritableRecoveryFixture(t)
	defer s.Close()
	p, err := s.prepareWritableCandidateRecovery(context.Background(), raw, sig, nil)
	if err != nil || p.expected.Binding.ClaimID != m.ClaimID || p.expected.Launch.MountsSHA256 != m.Preflight.Native.MountsSHA256 {
		t.Fatal("fresh exact recovery", err)
	}
	if _, _, err := s.beginWritableCandidate(context.Background(), raw, sig, "unknown", nil); err == nil {
		t.Fatal("renewal resurrected live execution")
	}
	if r, err := s.settleRecoveredWritableCandidate(context.Background(), p, nil, driver.WritableCandidateRecoveryObservation{}); err == nil || r.Format != "" {
		t.Fatal("zero recovery proof settled", err)
	}
	if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("unknown reclaimed")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown'`) != 1 || scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='container'`) != 1 {
		t.Fatal("refused proof changed state")
	}
}
func TestWritableCandidateRecoveryRefusesSourceAndMarkerDrift(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "fence", "source", "volume", "native", "updated", "unbound", "future_binding", "policy", "image", "early_envelope"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, m := unknownWritableRecoveryFixture(t)
			defer s.Close()
			queries := map[string]string{
				"authority": `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":       `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='start'`,
				"fence":     `UPDATE runner_resources SET writer_fence=writer_fence+1 WHERE kind='container'`,
				"source":    `UPDATE runner_commands SET receipt_json='{}' WHERE kind='activate'`,
				"volume":    `UPDATE runner_resources SET revision=2 WHERE kind='volume'`,
				"native":    `UPDATE runner_resources SET native_id='` + strings.Repeat("d", 64) + `' WHERE kind='container'`,
				"updated":   `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='start'`,
			}
			if q := queries[mode]; q != "" {
				if _, err := s.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			} else {
				switch mode {
				case "unbound":
					m.Preflight = nil
					m.PreflightBoundMS = ""
					if _, err := s.db.Exec(`UPDATE runner_commands SET updated_ms=? WHERE stage='start'`, m.ClaimedMS); err != nil {
						t.Fatal(err)
					}
				case "future_binding":
					m.PreflightBoundMS = m.ExpiresMS
				case "policy":
					m.Preflight.SeccompSHA256 = strings.Repeat("f", 64)
				case "image":
					m.Preflight.Native.ImageID = "sha256:" + strings.Repeat("f", 64)
				case "early_envelope":
					m.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
				}
				encoded, _ := json.Marshal(m)
				if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE stage='start'`, string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.prepareWritableCandidateRecovery(context.Background(), raw, sig, nil); err == nil {
				t.Fatal("drift authorized native recovery", mode)
			}
		})
	}
}
func TestWritableCandidateRecoveredReceiptSeparatesWindowsAndHistory(t *testing.T) {
	s, raw, sig, m := unknownWritableRecoveryFixture(t)
	defer s.Close()
	p, err := s.prepareWritableCandidateRecovery(context.Background(), raw, sig, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := p.facts.verified.Index()
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	r := WritableCandidateRunningReceipt{Format: writableRecoveredRunningFormat, Claim: m, NativeID: strings.Repeat("c", 64), NativeObservedMS: now, SettledMS: now, RecoveryEnvelopeSHA256: candidateHash(raw), RecoveryControlRevision: c.ControlRevision, RecoveryIssuedMS: c.IssuedMS, RecoveryExpiresMS: c.ExpiresMS}
	if !validWritableRunningReceipt(r) {
		t.Fatal("synthetic recovered shape")
	}
	encoded, _ := json.Marshal(r)
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE stage='start'`, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET native_id=?,revision=2,observed_state='running',observed_ms=?,updated_ms=? WHERE kind='container'`, r.NativeID, now, now); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err != nil || !sameWritableReceipt(got, r) {
		t.Fatal("recovered historical read", err)
	}
	for _, mode := range []string{"live_format", "missing_revision", "old_revision", "future_revision", "early_issued", "expired_observed", "expired_settled", "bad_hash", "wide_window"} {
		copy := r
		switch mode {
		case "live_format":
			copy.Format = writableRunningFormat
		case "missing_revision":
			copy.RecoveryControlRevision = ""
		case "old_revision":
			copy.RecoveryControlRevision = "2"
		case "future_revision":
			copy.RecoveryControlRevision = "4"
		case "early_issued":
			copy.RecoveryIssuedMS = m.ExpiresMS
		case "expired_observed":
			copy.NativeObservedMS = m.PreflightBoundMS
		case "expired_settled":
			copy.SettledMS = copy.RecoveryExpiresMS
		case "bad_hash":
			copy.RecoveryEnvelopeSHA256 = "bad"
		case "wide_window":
			issued, _ := strconv.ParseInt(copy.RecoveryIssuedMS, 10, 64)
			copy.RecoveryExpiresMS = strconv.FormatInt(issued+30001, 10)
		}
		encoded, _ := json.Marshal(copy)
		if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE stage='start'`, string(encoded)); err != nil {
			t.Fatal(err)
		}
		if got, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
			t.Fatal("changed recovery evidence accepted", mode)
		}
	}
}
