package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// After the command is sent a call keeps its fields and its digest, never the
// value or the signed bytes, and the proofs it signs from that still verify
// against the full command the runner holds.
func testPlatformServiceEgressCallKeepsNoCredentialValue(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	command, raw, _, binding := platformServiceCredentialSign(t, s, state)
	value := bytes.Clone(command.CredentialValue)
	call := newPlatformServiceEgressCall(command, raw, binding)
	if len(call.command.CredentialValue) != 0 {
		t.Fatal("the kept call holds the value")
	}
	// %+v prints unexported fields too; a []byte prints as numbers, so look for both.
	if kept := fmt.Sprintf("%+v", call); strings.Contains(kept, string(value)) || strings.Contains(kept, "Bearer") ||
		strings.Contains(kept, fmt.Sprint(value)) {
		t.Fatal("the kept call serializes the value")
	}
	if !bytes.Equal(command.CredentialValue, value) || !call.consistent {
		t.Fatal("building the kept call changed the command it copied or lost its consistency")
	}
	// Wipe what the coordinator wipes; the checks still go on.
	clear(raw)
	clear(command.CredentialValue)
	a, err := s.loadServiceEgressForSession(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	nonce := bytes.Repeat([]byte{5}, 32)
	proof, sig, err := s.approveServiceEgressCheck(context.Background(), state, call, hex.EncodeToString(nonce))
	if err != nil {
		t.Fatal("check refused after the value was wiped:", err)
	}
	full := call.command
	full.CredentialValue = value
	if err := control.VerifyNetworkHTTPCheck(full, hex.EncodeToString(nonce), proof, sig, a.authority, time.Now().UnixMilli()); err != nil {
		t.Fatal("the proof does not bind the full command:", err)
	}
	// A call whose fields are not the signed bytes is never answered.
	forged := call
	forged.consistent = false
	if _, _, err := s.approveServiceEgressCheck(context.Background(), state, forged, hex.EncodeToString(nonce)); err == nil {
		t.Fatal("inconsistent call was answered")
	}
}

func TestPlatformServiceEgressCallKeepsNoCredentialValueSQLite(t *testing.T) {
	testPlatformServiceEgressCallKeepsNoCredentialValue(t, false)
}
func TestPlatformServiceEgressCallKeepsNoCredentialValuePostgres(t *testing.T) {
	testPlatformServiceEgressCallKeepsNoCredentialValue(t, true)
}
