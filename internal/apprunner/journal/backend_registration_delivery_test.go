//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestBackendRegistrationClaimIsDurableAndCannotRepeat(t *testing.T) {
	s, ref, sig, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	raw, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(960), strings.Repeat("c", 64))
	if _, err := s.claimBackendRegistration(context.Background(), raw, signature); err == nil {
		t.Fatal("unadmitted claim accepted")
	}
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	marker, err := s.claimBackendRegistration(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	if !validRegistrationClaim(marker) || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("claim did not precede native work")
	}
	if _, err := s.claimBackendRegistration(context.Background(), raw, signature); err == nil {
		t.Fatal("claim repeated")
	}
	tx, f, err := s.beginBackendRegistration(context.Background(), raw, signature, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if !registrationClaimMatches(marker, f) || candidateHash([]byte(f.receipt)) != registrationBinding(marker) {
		tx.Rollback()
		t.Fatal("claim lost exact source")
	}
	tx.Rollback()
	if _, err := s.settleBackendRegistration(context.Background(), raw, signature, marker, driver.BackendRegistrationObservation{}); err == nil {
		t.Fatal("zero observation fabricated receipt")
	}
	if _, err := s.ReadBackendRegistrationReceipt(context.Background(), raw, signature); err == nil {
		t.Fatal("unknown claim became applied receipt")
	}
	if _, err := s.DispatchBackendRegistration(context.Background(), raw, signature, nil, []byte("secret"), nil); err == nil {
		t.Fatal("nil native runtime accepted")
	}
}
func TestBackendRegistrationClaimRefusesChangedHistory(t *testing.T) {
	for _, mode := range []string{"source", "resource", "data", "ack", "pending", "floor", "claim"} {
		t.Run(mode, func(t *testing.T) {
			s, ref, sig, _ := appliedCandidateReceiptFixture(t)
			defer s.Close()
			raw, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(960), strings.Repeat("c", 64))
			if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, signature); err != nil {
				t.Fatal(err)
			}
			_, err := s.claimBackendRegistration(context.Background(), raw, signature)
			if err != nil {
				t.Fatal(err)
			}
			var query string
			switch mode {
			case "source":
				query = `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`
			case "resource":
				query = `UPDATE runner_resources SET revision=revision+1 WHERE kind='container'`
			case "data":
				query = `UPDATE runner_resources SET observed_state='unknown' WHERE kind='volume'`
			case "ack":
				query = `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='activate'`
			case "pending":
				query = `UPDATE runner_commands SET state='unknown' WHERE stage='prepare_data'`
			case "floor":
				query = `UPDATE runner_resources SET control_fence=control_fence+1 WHERE kind='container'`
			case "claim":
				query = `UPDATE runner_commands SET receipt_json='{}' WHERE stage='activate'`
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			tx, f, err := s.beginBackendRegistration(context.Background(), raw, signature, "unknown")
			if err == nil {
				tx.Rollback()
				if mode != "claim" || registrationClaimMatches(backendRegistrationClaim{}, f) {
					t.Fatal("changed history accepted", mode)
				}
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND state='applied'`) != 0 {
				t.Fatal("failed check applied work")
			}
		})
	}
}
func TestBackendRegistrationReceiptParsing(t *testing.T) {
	s, ref, sig, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	raw, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(960), strings.Repeat("c", 64))
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	m, err := s.claimBackendRegistration(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	// This synthetic metadata tests parsing only. It cannot settle a live effect.
	r := BackendRegistrationReceipt{Format: registrationReceiptFormat, Claim: m, BindingSHA256: registrationBinding(m), Device: "1", Inode: "2", ObservedMS: m.ClaimedMS, SettledMS: m.ClaimedMS}
	encoded, _ := json.Marshal(r)
	if _, err := ParseBackendRegistrationReceipt(encoded); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, append(encoded, []byte(" {}")...), []byte(strings.Replace(string(encoded), `"inode":"2"`, `"inode":"0"`, 1)), []byte(strings.Replace(string(encoded), `"binding_sha256":"`, `"binding_sha256":"f`, 1))} {
		if _, err := ParseBackendRegistrationReceipt(bad); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
}

func TestBackendRegistrationReceiptMatchesExactIntent(t *testing.T) {
	s, ref, sig, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	raw, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(960), strings.Repeat("c", 64))
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	m, err := s.claimBackendRegistration(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	var c control.BackendRegistrationCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	r := BackendRegistrationReceipt{Format: registrationReceiptFormat, Claim: m, BindingSHA256: registrationBinding(m), Device: "1", Inode: "2", ObservedMS: m.ClaimedMS, SettledMS: m.ClaimedMS}
	if !r.MatchesCommand(c) {
		t.Fatal("exact historical metadata did not match")
	}
	for _, mode := range []string{"effect", "document", "payload", "binding", "inode"} {
		t.Run(mode, func(t *testing.T) {
			changed := r
			switch mode {
			case "effect":
				changed.Claim.EffectID = fixtureID(961)
			case "document":
				changed.Claim.DocumentSHA256 = strings.Repeat("d", 64)
			case "payload":
				changed.Claim.PayloadSHA256 = strings.Repeat("e", 64)
			case "binding":
				changed.BindingSHA256 = strings.Repeat("f", 64)
			case "inode":
				changed.Inode = "0"
			}
			if changed.MatchesCommand(c) {
				t.Fatal("changed receipt matched", mode)
			}
		})
	}
}
