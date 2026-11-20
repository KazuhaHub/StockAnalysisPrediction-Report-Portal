package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// maxEgressChecks bounds the background calls whose checkers are registered at
// once. The pool already admits at most half of its active slots to background
// work; this is only a ceiling for the registry itself.
const maxEgressChecks = 2 * broker.MaxNetworkPoolActive

// egressCheck is the networkChecker of one background call: it sends each
// authority challenge as a frame of that call and waits for the proof frame on
// the same call, so a proof can never be taken for another call's challenge.
type egressCheck struct {
	call       *EgressCall
	commandSHA string
	mu         sync.Mutex
	seq        byte
}

func (c *egressCheck) commandSHA256() string { return c.commandSHA }

func (c *egressCheck) check(ctx context.Context, challenge []byte) ([]byte, []byte, error) {
	if len(challenge) != 32 {
		return nil, nil, broker.ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if int(c.seq) >= EgressMaxChecks || ctx.Err() != nil {
		return nil, nil, broker.ErrDenied
	}
	c.seq++
	var nonce [32]byte
	copy(nonce[:], challenge)
	if err := c.call.Send(EgressFrameChallenge, EncodeEgressChallenge(c.seq, nonce)); err != nil {
		return nil, nil, broker.ErrDenied
	}
	// The caller's context carries the per-check budget; no deadline is placed on
	// the shared connection.
	frame, err := c.call.Recv(ctx)
	if err != nil || frame.Type != EgressFrameProof {
		return nil, nil, broker.ErrDenied
	}
	proof, signature, err := DecodeEgressProof(frame.Payload)
	if err != nil {
		return nil, nil, broker.ErrDenied
	}
	return proof, signature, nil
}

// executeEgress runs one signed background command on the shared pool. The
// call's authority checks travel on call. The checker is registered under the
// request ID Portal minted for this command (never one the app or the runner
// chose), for exactly as long as the call runs; a second command with the same
// request ID is refused, so no call can take over another's checks.
//
// The caller has already compared the command with its session and admit.
func (p *networkProvider) executeEgress(ctx context.Context, call *EgressCall, raw, signature, body []byte, a control.Authority) (broker.HTTPResponse, error) {
	var zero broker.HTTPResponse
	if p == nil || p.background == nil || call == nil {
		return zero, broker.ErrUnavailable
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return zero, broker.ErrDenied
	}
	defer verified.Clear()
	digest := sha256.Sum256(raw)
	checker := &egressCheck{call: call, commandSHA: hex.EncodeToString(digest[:])}
	// Only the request id is wanted: do not keep the clone that carries the value.
	view := verified.Command()
	id := view.RequestID
	clear(view.CredentialValue)
	view.ClearSignedHeaders()
	p.mu.Lock()
	if p.sessions[id] != nil || p.streamSessions[id] != nil {
		p.mu.Unlock()
		return zero, broker.ErrDenied
	}
	if p.egressChecks >= maxEgressChecks {
		p.mu.Unlock()
		return zero, broker.ErrBusy
	}
	p.sessions[id] = checker
	p.egressChecks++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.sessions[id] == networkChecker(checker) {
			delete(p.sessions, id)
			p.egressChecks--
		}
		p.mu.Unlock()
	}()
	return p.background.DoBackground(ctx, raw, signature, body, a)
}
