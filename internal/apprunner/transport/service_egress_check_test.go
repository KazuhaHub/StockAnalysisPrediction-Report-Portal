package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// backgroundFixture adapts the foreground execution fixture to the background
// entry point: the same four authority checks, the same effect.
type backgroundFixture struct{ *networkExecutionFixture }

func (b backgroundFixture) DoBackground(ctx context.Context, raw, sig, body []byte, a control.Authority) (broker.HTTPResponse, error) {
	return b.Do(ctx, raw, sig, body, a)
}

// portalAnswers plays Portal's side of one accepted call: it answers every
// challenge with a signed proof and records the call and sequence of each.
func portalAnswers(t *testing.T, portal *EgressMux, key ed25519.PrivateKey, a control.Authority, command control.NetworkHTTPRequestCommand, seen chan<- [2]byte) {
	t.Helper()
	go func() {
		call, err := portal.Accept(context.Background())
		if err != nil {
			return
		}
		defer call.Finish()
		for {
			frame, err := call.Recv(context.Background())
			if err != nil {
				return
			}
			if frame.Type == EgressFrameAdmit {
				continue // the call's own start
			}
			if frame.Type != EgressFrameChallenge {
				t.Errorf("unexpected frame %d", frame.Type)
				return
			}
			seq, nonce, err := DecodeEgressChallenge(frame.Payload)
			if err != nil {
				t.Error(err)
				return
			}
			seen <- [2]byte{call.ID()[0], seq}
			proof, signature, err := control.SignNetworkHTTPCheck(command, hex.EncodeToString(nonce[:]), key, a, time.Now().UnixMilli())
			if err != nil {
				t.Error(err)
				return
			}
			payload, err := EncodeEgressSigned(proof, signature)
			if err != nil {
				t.Error(err)
				return
			}
			if err := call.Send(EgressFrameProof, payload); err != nil {
				return
			}
		}
	}()
}

func TestServiceEgressCallReceivesFourChecksOnItsSession(t *testing.T) {
	raw, sig, body, a, key := networkSessionFixture(t)
	verified, err := control.VerifyNetworkHTTPRequest(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := newNetworkProvider(networkEnrollmentFixture{a}, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	execution := &networkExecutionFixture{current: provider.current}
	provider.background = backgroundFixture{execution}
	runner, portal := egressPair(t)
	seen := make(chan [2]byte, 8)
	portalAnswers(t, portal, key, a, verified.Command(), seen)
	call, err := runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	// The Portal side only accepts a call once it has seen a frame on it.
	if err := call.Send(EgressFrameAdmit, mustAdmit(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := provider.executeEgress(ctx, call, raw, sig, body, a)
	if err != nil || response.Status != 201 || string(response.Body) != `{"ok":true}` || execution.effects.Load() != 1 {
		t.Fatalf("%+v %v effects=%d", response, err, execution.effects.Load())
	}
	close(seen)
	var sequences []byte
	for s := range seen {
		if s[0] != call.ID()[0] {
			t.Fatal("a check arrived on another call")
		}
		sequences = append(sequences, s[1])
	}
	if string(sequences) != "\x01\x02\x03\x04" {
		t.Fatalf("checks on the call: %v", sequences)
	}
	if len(provider.sessions) != 0 {
		t.Fatal("checker leaked")
	}
	// A call that was never given a fifth check cannot ask for one.
	if _, _, err := (&egressCheck{call: call, seq: EgressMaxChecks}).check(ctx, make([]byte, 32)); !errors.Is(err, broker.ErrDenied) {
		t.Fatalf("fifth check: %v", err)
	}
}

// pausingBackground makes its first authority check, signals, and waits.
type pausingBackground struct {
	current broker.CurrentNetworkAuthority
	entered chan struct{}
	release chan struct{}
	checks  atomic.Int32
}

func (p *pausingBackground) DoBackground(ctx context.Context, raw, sig, body []byte, a control.Authority) (broker.HTTPResponse, error) {
	v, err := control.VerifyNetworkHTTPRequest(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		return broker.HTTPResponse{}, broker.ErrDenied
	}
	for i := 0; i < 4; i++ {
		if _, err := p.current(ctx, v.Command()); err != nil {
			return broker.HTTPResponse{}, broker.ErrDenied
		}
		p.checks.Add(1)
		if i == 0 {
			p.entered <- struct{}{}
			<-p.release
		}
	}
	return broker.HTTPResponse{Status: 200, ContentType: "text/plain", Body: []byte("ok"), RetryAfterSeconds: -1}, nil
}

func TestServiceEgressCheckerKeyedByPortalRequestID(t *testing.T) {
	raw, sig, body, a, key := networkSessionFixture(t)
	verified, err := control.VerifyNetworkHTTPRequest(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	command := verified.Command()
	provider, err := newNetworkProvider(networkEnrollmentFixture{a}, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	background := &pausingBackground{current: provider.current, entered: make(chan struct{}, 1), release: make(chan struct{})}
	provider.background = background
	runner, portal := egressPair(t)
	seen := make(chan [2]byte, 16)
	portalAnswers(t, portal, key, a, command, seen)
	first, _ := runner.NewCall()
	if err := first.Send(EgressFrameAdmit, mustAdmit(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := provider.executeEgress(ctx, first, raw, sig, body, a); done <- err }()
	select {
	case <-background.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}
	// While in flight, the checker is registered under Portal's request ID, not
	// under the call ID, and counts against no foreground slot.
	provider.mu.Lock()
	_, byRequest := provider.sessions[command.RequestID]
	registered := len(provider.sessions)
	foreground := registered - provider.egressChecks
	provider.mu.Unlock()
	if !byRequest || registered != 1 || foreground != 0 {
		t.Fatalf("registered=%d byRequest=%v foreground=%d", registered, byRequest, foreground)
	}
	// A second call carrying the same request ID is refused and cannot take
	// over, or disturb, the first call's checker.
	second, _ := runner.NewCall()
	if _, err := provider.executeEgress(ctx, second, raw, sig, body, a); !errors.Is(err, broker.ErrDenied) {
		t.Fatalf("duplicate request ID: %v", err)
	}
	provider.mu.Lock()
	still := len(provider.sessions)
	provider.mu.Unlock()
	if still != 1 {
		t.Fatal("duplicate registration disturbed the first")
	}
	// A confirmation for a command that is not the registered one is refused.
	other := command
	other.ActorScope = hex.EncodeToString(make([]byte, 32))
	other.ActorScope = other.ActorScope[:63] + "1"
	if _, _, err := provider.confirm(ctx, other, hex.EncodeToString(make([]byte, 32))); !errors.Is(err, broker.ErrDenied) {
		t.Fatalf("confirm for a different command: %v", err)
	}
	other.RequestID = "30000000-0000-4000-8000-0000000000ff"
	if _, _, err := provider.confirm(ctx, other, hex.EncodeToString(make([]byte, 32))); !errors.Is(err, broker.ErrDenied) {
		t.Fatalf("confirm for an unregistered request: %v", err)
	}
	close(background.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if background.checks.Load() != 4 {
		t.Fatalf("checks %d", background.checks.Load())
	}
	provider.mu.Lock()
	left := len(provider.sessions)
	provider.mu.Unlock()
	if left != 0 || provider.egressChecks != 0 {
		t.Fatal("checker leaked after the call")
	}
	// Once the call is over its request ID is free again, so a later call (a
	// retry the app made) is not wedged by a stale registration.
}
