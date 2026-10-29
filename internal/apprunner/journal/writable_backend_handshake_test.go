//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Synthetic applied-file metadata tests readers only, never native exchange.
func writableHandshakeFixture(t *testing.T) (*Store, []byte, []byte, writableBackendExchangePreflight) {
	t.Helper()
	s, raw, sig, attempt := writableRegistrationClaimFixture(t)
	var marker writableRegistrationClaim
	json.Unmarshal([]byte(attempt.marker), &marker)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	receipt := WritableBackendRegistrationReceipt{Format: writableRegistrationReceiptFormat, Claim: marker, BindingSHA256: writableRegistrationBinding(marker), Device: "1", Inode: "2", ObservedMS: now, SettledMS: now}
	encoded, _ := json.Marshal(receipt)
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(encoded), now, marker.EffectID); err != nil {
		t.Fatal(err)
	}
	before, err := s.prepareWritableBackendExchange(context.Background(), raw, sig, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, before
}
func TestWritableBackendHandshakeMetadataAndOpaqueBoundary(t *testing.T) {
	e := WritableBackendHandshakeEvidence{Format: writableBackendHandshakeFormat, PayloadSHA256: strings.Repeat("a", 64), DeliveryReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), ChallengeSHA256: strings.Repeat("d", 64), Answer: "a.b", ObservedMS: "1"}
	raw, _ := json.Marshal(e)
	if got, err := ParseWritableBackendHandshakeEvidence(raw); err != nil || got != e {
		t.Fatal("canonical evidence", err)
	}
	for _, bad := range [][]byte{nil, append(raw, ' '), []byte(strings.Replace(string(raw), writableBackendHandshakeFormat, "portal.runner.backend-handshake/1-dev.1", 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1"`, `"observed_ms":"-1"`, 1)), []byte(strings.Replace(string(raw), `"answer":"a.b"`, `"answer":"invalid"`, 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`), []byte(strings.TrimSuffix(string(raw), "}") + `,"answer":"a.b"}`)} {
		if got, err := ParseWritableBackendHandshakeEvidence(bad); err == nil || got.Format != "" {
			t.Fatal("ambiguous evidence accepted")
		}
	}
	if _, err := ParseBackendHandshakeEvidence(raw); err == nil {
		t.Fatal("writable evidence crossed readonly protocol")
	}
	if _, err := new(Store).ExchangeWritableBackendHandshake(context.Background(), nil, nil, nil, "a.b", nil); err == nil {
		t.Fatal("missing native provider supplied evidence")
	}
	s, _, _, before := writableHandshakeFixture(t)
	defer s.Close()
	if r, err := s.finishWritableBackendHandshake(context.Background(), before, "a.b", driver.WritableBackendHandshakeObservation{}); err == nil || r.Format != "" {
		t.Fatal("zero native exchange accepted")
	}
	if got, err := s.ReadWritableBackendRegistrationReceipt(context.Background(), before.facts.raw, before.facts.signature); err != nil || got != before.receipt {
		t.Fatal("failed handshake changed history", err)
	}
}
func TestWritableBackendHandshakeRefusesCurrentDrift(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "source", "native", "file_receipt", "time", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, before := writableHandshakeFixture(t)
			defer s.Close()
			queries := map[string]string{
				"authority":    `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":          `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='activate' AND effect_number=3`,
				"source":       `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
				"native":       `UPDATE runner_resources SET native_id='` + strings.Repeat("f", 64) + `' WHERE kind='container'`,
				"file_receipt": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='activate' AND effect_number=3`,
				"time":         `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='activate' AND effect_number=3`,
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
			if _, err := s.prepareWritableBackendExchange(ctx, before.facts.raw, before.facts.signature, nil); err == nil {
				t.Fatal("changed source preflight accepted", mode)
			}
			if got, err := s.finishWritableBackendHandshake(ctx, before, "a.b", driver.WritableBackendHandshakeObservation{}); err == nil || got.Format != "" {
				t.Fatal("changed current handshake source accepted", mode)
			}
		})
	}
}
func TestWritableBackendHandshakeRefusesUnknownDelivery(t *testing.T) {
	s, raw, sig, _ := writableRegistrationClaimFixture(t)
	defer s.Close()
	if _, err := s.prepareWritableBackendExchange(context.Background(), raw, sig, nil); err == nil {
		t.Fatal("unknown private-file delivery treated as completed")
	}
}
