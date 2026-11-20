package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// This file is the runner side of the PostgreSQL liveness call of a background egress session
// (doc 178): the application's POST /v1/postgres/liveness, the admit and command exchange with
// Portal, and the three authority checks that travel on the call. The application never sees a
// credential, a server message or a byte of the exchange; it gets an outcome class and a latency.

// postgres serves POST /v1/postgres/liveness.
func (in *egressInstall) postgres(w http.ResponseWriter, r *http.Request, raw []byte) {
	request, err := broker.DecodeAppPostgresRequest(raw)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	admitted := EgressPostgresAdmit{Target: request.Target, Credential: request.Credential}
	admit, err := EncodeEgressPostgresAdmit(admitted)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	session, done, code := in.begin("network.postgres")
	if code != "" {
		writeAppError(w, code)
		return
	}
	defer done()
	// A check always names a binding, so it needs a session that carries secrets.use. Portal is not
	// asked: a session that never held the resource cannot use a credential.
	if !session.allows("secrets.use") {
		writeAppError(w, broker.AppErrDenied)
		return
	}
	call, err := session.mux.NewCall()
	if err != nil {
		writeAppError(w, newCallCode(err))
		return
	}
	defer call.Finish()
	ctx, cancel := context.WithTimeout(r.Context(), egressCallTimeout)
	defer cancel()
	if err := call.Send(EgressFramePostgresAdmit, admit); err != nil {
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	frame, code := awaitPortal(ctx, call)
	if code != "" {
		writeAppError(w, code)
		return
	}
	switch frame.Type {
	case EgressFrameRefusal:
		refused, err := DecodeEgressRefusal(frame.Payload)
		if err != nil {
			session.mux.Close(EgressCloseRefused)
			writeAppError(w, broker.AppErrUnavailable)
			return
		}
		writeAppError(w, refusalCode(refused))
		return
	case EgressFrameCommand:
	default:
		session.mux.Close(EgressCloseRefused)
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	// The command frame and its decoded copy carry the password Portal placed: they are wiped when
	// the call ends.
	defer clear(frame.Payload)
	commandRaw, commandSig, err := DecodeEgressCommand(frame.Payload)
	if err != nil {
		session.mux.Close(EgressCloseRefused)
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	defer clear(commandRaw)
	result, runErr := in.runPostgres(ctx, session, call, commandRaw, commandSig, admitted)
	if payload, err := EncodeEgressOutcome(egressPostgresOutcomeOf(result, runErr)); err == nil {
		_ = call.Send(EgressFrameOutcome, payload)
	}
	if runErr != nil {
		writeAppError(w, broker.AppPostgresErrorFor(runErr))
		return
	}
	writeAppJSON(w, 200, broker.AppPostgresSuccessBody(result.Outcome, result.Latency.Milliseconds()), "")
}

// runPostgres verifies Portal's command against the session and the admit, then runs the check. A
// command that does not match is denied and never connects.
func (in *egressInstall) runPostgres(ctx context.Context, session *egressSession, call *EgressCall, raw, signature []byte, admit EgressPostgresAdmit) (broker.PostgresResult, error) {
	var zero broker.PostgresResult
	authority, err := in.registry.provider.enrollment(ctx)
	if err != nil {
		return zero, broker.ErrDenied
	}
	verified, err := control.VerifyPostgresProbeCommand(raw, signature, authority, in.registry.now().UnixMilli())
	if err != nil {
		return zero, broker.ErrDenied
	}
	defer verified.Clear()
	command := verified.Command()
	defer command.Clear()
	if session.checkPostgres(command, admit) != nil || !session.claim(command.RequestID) {
		return zero, broker.ErrDenied
	}
	return in.registry.provider.executePostgres(ctx, call, raw, signature, authority, command)
}

// egressPostgresOutcomeOf is what Portal's summary audit learns about a check: its class and, for a
// check that ran, the outcome as a status code. There is no uncertain class: whatever is neither a
// denial nor a spent budget is unavailable.
func egressPostgresOutcomeOf(result broker.PostgresResult, err error) EgressOutcome {
	outcome := EgressOutcome{RetryAfter: -1}
	switch {
	case err == nil:
		outcome.Class = EgressOutcomeOK
		outcome.Status = EgressPostgresStatus(result.Outcome)
	case errors.Is(err, broker.ErrDenied):
		outcome.Class = EgressOutcomeDenied
	case errors.Is(err, broker.ErrBusy):
		outcome.Class = EgressOutcomeBusy
	default:
		outcome.Class = EgressOutcomeUnavailable
	}
	return outcome
}

// checkPostgres compares a verified probe command with the session it arrived on and the admit that
// asked for it. Portal signs the command; the runner never trusts it to be for this installation,
// session, target or binding without checking.
func (s *egressSession) checkPostgres(c control.PostgresProbeCommand, admit EgressPostgresAdmit) error {
	scope := sha256.Sum256([]byte("portal-network-service/v1:" + s.command.SessionID))
	expires, ok := parseCounter(c.ExpiresMS)
	switch {
	case c.DeploymentID != s.command.DeploymentID, c.ControllerEpoch != s.command.ControllerEpoch, c.ControlRevision != s.command.ControlRevision,
		c.InstallationID != s.command.InstallationID, c.Generation != s.command.Generation, c.PolicyRevision != s.command.PolicyRevision,
		c.ReleaseSHA256 != s.command.ReleaseSHA256,
		// A service call is made as the service of this session, never as a viewer.
		c.ActorScope != hex.EncodeToString(scope[:]),
		c.TargetID != admit.Target, c.CredentialName != admit.Credential,
		!ok, time.UnixMilli(expires).After(s.expires):
		return errEgressCommand
	}
	return nil
}

// executePostgres runs one signed probe command. Its three authority checks travel on call: each
// sends a fresh challenge, takes Portal's signed proof and verifies it against the command and the
// enrollment as it is now. The caller has already compared the command with its session and admit.
func (p *networkProvider) executePostgres(ctx context.Context, call *EgressCall, raw, signature []byte, a control.Authority, command control.PostgresProbeCommand) (broker.PostgresResult, error) {
	var zero broker.PostgresResult
	if p == nil || p.postgres == nil || call == nil {
		return zero, broker.ErrUnavailable
	}
	digest := sha256.Sum256(raw)
	checker := &egressCheck{call: call, commandSHA: hex.EncodeToString(digest[:])}
	check := func(ctx context.Context) error {
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return broker.ErrDenied
		}
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		proof, proofSignature, err := checker.check(cctx, nonce[:])
		if err != nil || cctx.Err() != nil {
			return broker.ErrDenied
		}
		// Enrollment may rotate while Portal checks authority: the proof is judged against the
		// enrollment as it is now, and only if it is still the one the command was verified under.
		current, err := p.enrollment(cctx)
		if err != nil || current.DeploymentID != a.DeploymentID || current.ControllerEpoch != a.ControllerEpoch || current.ControlRevision != a.ControlRevision ||
			current.Fingerprint != a.Fingerprint || !bytes.Equal(current.PublicKey, a.PublicKey) || len(current.PublicKey) != ed25519.PublicKeySize {
			return broker.ErrDenied
		}
		if control.VerifyPostgresProbeCheck(command, hex.EncodeToString(nonce[:]), proof, proofSignature, current, time.Now().UnixMilli()) != nil {
			return broker.ErrDenied
		}
		return nil
	}
	return p.postgres.Run(ctx, raw, signature, a, check)
}
