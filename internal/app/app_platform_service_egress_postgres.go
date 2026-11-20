package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The PostgreSQL liveness call of a background egress session, Portal side (doc 178). The runner
// admits a call by naming a reviewed target and a binding; Portal reads the network.postgres grant
// and the binding again from SQL, opens the credential only once both resolved, signs one probe
// command and answers each authority challenge of the call. Nothing of the call is decided by the
// session or the request.

const platformServiceEgressPostgres = platformCapabilityNetworkPostgres

// platformServicePostgresQuerier is the optional half of the egress transport that asks the runner
// whether it serves the probe. Portal adds network.postgres to a session only when it answered: a
// runner that does not know the resource would refuse the whole session. A transport that has no
// such question never offers the resource.
type platformServicePostgresQuerier interface {
	QueryPostgres(ctx context.Context, e platformRuntimeEndpoint) (transport.PostgresProbeProfile, error)
}

// platformServicePostgresResources is the resource list a session is planned with: the granted
// ones, less network.postgres (and secrets.use, if nothing else needs it) when the runner did not
// answer the probe query. A failure to ask is an error, not an answer.
func (c *platformServiceEgressCoordinator) platformServicePostgresResources(ctx context.Context, endpoint platformRuntimeEndpoint, resources []string) ([]string, error) {
	if !slices.Contains(resources, platformServiceEgressPostgres) {
		return resources, nil
	}
	if q, ok := c.transport.(platformServicePostgresQuerier); ok {
		_, err := q.QueryPostgres(ctx, endpoint)
		if err == nil {
			return resources, nil
		}
		if !errors.Is(err, transport.ErrPostgresProbeUnsupported) {
			return nil, err
		}
	}
	out := slices.DeleteFunc(slices.Clone(resources), func(r string) bool { return r == platformServiceEgressPostgres })
	if !slices.Contains(out, platformServiceEgressNetwork) {
		out = slices.DeleteFunc(out, func(r string) bool { return r == platformServiceEgressSecrets })
	}
	if len(out) == 0 {
		return nil, transport.ErrPostgresProbeUnsupported
	}
	return out, nil
}

// loadPlatformServiceEgressPostgresGrant reads the granted network.postgres row of the current
// policy revision. The policy must parse as a canonical probe policy.
func loadPlatformServiceEgressPostgresGrant(e migExec, installation string, policyRevision int64, result *platformServiceEgressAuthority) error {
	var raw string
	if err := e.queryRow(`SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres' AND granted=1 AND policy_revision=?`, installation, policyRevision).Scan(&raw); err != nil {
		return errPlatformServiceEgressUnavailable
	}
	parsed, err := appplatform.ParsePostgresPolicy([]byte(raw))
	if err != nil {
		return errPlatformServiceEgressUnavailable
	}
	result.postgres, result.postgresRaw = parsed, raw
	return nil
}

// loadPlatformServiceEgressPostgres resolves one probe call inside the authority transaction. The
// target comes first: a target the reviewed policy does not have is denied without the binding
// being looked at, so it never reaches the custody. Then the binding must be a pg_password one,
// reviewed for the service and for exactly this target's digest; a denial happens before anything
// is decrypted, a value that cannot be opened is unavailable.
func (s *Server) loadPlatformServiceEgressPostgres(e migExec, installation string, o platformServiceEgressLoad, result *platformServiceEgressAuthority) error {
	if !slices.Contains(result.resources, platformServiceEgressPostgres) || !slices.Contains(result.resources, platformServiceEgressSecrets) || result.postgresRaw == "" {
		return errPlatformServiceEgressDenied
	}
	target, ok := result.postgres.Policy.Select(o.postgresTarget)
	if !ok {
		return errPlatformServiceEgressDenied
	}
	digest, err := appplatform.PostgresTargetSHA256(target)
	if err != nil {
		return errPlatformServiceEgressDenied
	}
	facts, err := loadPlatformSecretBindingFactsMode(e, installation, false, true)
	if err != nil || facts.postgresPolicyRaw != result.postgresRaw {
		return errPlatformServiceEgressUnavailable
	}
	use, sealed, err := loadPlatformSecretBindingUseFor(e, installation, o.credential, facts, platformPrincipalService, platformBindingDestination{postgresTargetSHA: digest})
	if err != nil {
		return platformServiceEgressCredentialError(err)
	}
	if use.operation != "pg_password" {
		return errPlatformServiceEgressDenied
	}
	result.binding, result.credentialName, result.pgTarget, result.pgTargetSHA = use, o.credential, target, digest
	if !o.decrypt {
		return nil
	}
	plain, err := s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil {
		return errPlatformServiceEgressUnavailable
	}
	credential, err := appplatform.ParsePostgresCredential(plain)
	clear(plain)
	if err != nil {
		return errPlatformServiceEgressUnavailable
	}
	result.pgUsername, result.pgPassword = credential.Username, credential.Password
	return nil
}

// loadServiceEgressForSessionPostgres is the reading of a probe call of an open session: at an
// admit (decrypt) the value is opened last; at a check it is only resolved.
func (s *Server) loadServiceEgressForSessionPostgres(ctx context.Context, state platformServiceEgressSessionState, target, credential string, decrypt bool) (platformServiceEgressAuthority, error) {
	return s.loadServiceEgressForSessionLoad(ctx, state, platformServiceEgressLoad{postgresTarget: target, credential: credential, decrypt: decrypt})
}

func (a platformServiceEgressAuthority) hasPostgres() bool {
	return slices.Contains(a.resources, platformServiceEgressPostgres) && a.postgresRaw != ""
}

// signServiceEgressPostgres signs one admitted probe call. The reviewed policy decides (the target,
// resolved in the load); the command carries a fresh Portal-minted request id, the role this
// authority opened and the policy bytes, and expires with the session at the latest.
func (a platformServiceEgressAuthority) signServiceEgressPostgres(sessionID string, admit transport.EgressPostgresAdmit, nowMS, sessionExpiresMS int64) (control.PostgresProbeCommand, []byte, []byte, error) {
	var zero control.PostgresProbeCommand
	if !a.hasPostgres() || len(a.controlSeed) != ed25519.SeedSize || !platformValidUUID(sessionID) {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	if limit := min(sessionExpiresMS, platformServiceEgressIdentityExpiry(a.identity, sessionExpiresMS)); limit-nowMS < platformServiceEgressMinAdmitLeft.Milliseconds() {
		return zero, nil, nil, errPlatformServiceEgressExpiring
	}
	// A command carries a credential only for exactly the target and binding the admit named, both
	// resolved and opened by this authority.
	if a.pgTarget.ID != admit.Target || a.credentialName != admit.Credential || len(a.pgUsername) == 0 || len(a.pgPassword) == 0 || a.binding.operation != "pg_password" {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	id, err := platformNewUUID()
	if err != nil {
		return zero, nil, nil, errPlatformServiceEgressUnavailable
	}
	r := a.runtime
	c := control.PostgresProbeCommand{
		Format: control.PostgresProbeCommandFormat, DeploymentID: r.deploymentID,
		ControllerEpoch: r.controllerEpoch, ControlRevision: strconv.FormatInt(r.controlRevision, 10),
		RequestID: id, InstallationID: a.installation,
		Generation: a.facts.generation, PolicyRevision: a.facts.policyRevision,
		ReleaseSHA256: a.facts.release, ActorScope: platformServiceEgressActorScope(sessionID),
		PolicyRaw: []byte(a.postgresRaw), PolicySHA256: a.postgres.SHA256, TargetID: a.pgTarget.ID,
		PrivateNetworksSHA256: a.postgres.Policy.PrivateNetworksSHA256, CredentialName: a.credentialName,
		Username: bytes.Clone(a.pgUsername), Password: bytes.Clone(a.pgPassword),
		IssuedMS: strconv.FormatInt(nowMS, 10), ExpiresMS: strconv.FormatInt(nowMS+platformServiceEgressRequestMS, 10),
	}
	key := ed25519.NewKeyFromSeed(a.controlSeed)
	defer clear(key)
	raw, signature, err := control.SignPostgresProbeCommand(c, key, a.authority, nowMS)
	if err != nil {
		c.Clear()
		return zero, nil, nil, errPlatformServiceEgressDenied
	}
	return c, raw, signature, nil
}

// platformServiceEgressPostgresCall is what a probe call keeps of its signed command once it has
// been sent: the fields the checks compare and the digest the proofs bind, never the role.
type platformServiceEgressPostgresCall struct {
	command    control.PostgresProbeCommand // Username and Password are always nil
	sha        string
	binding    platformSecretBindingUse
	targetSHA  string
	consistent bool
}

// newPlatformServiceEgressPostgresCall keeps the fields and the digest of a signed command. It
// copies; the caller wipes its own command and bytes.
func newPlatformServiceEgressPostgresCall(command control.PostgresProbeCommand, raw []byte, binding platformSecretBindingUse, targetSHA string) platformServiceEgressPostgresCall {
	kept := command
	kept.PolicyRaw = bytes.Clone(command.PolicyRaw)
	kept.Username, kept.Password = nil, nil
	again, err := marshalPlatformServiceEgressPostgres(command)
	consistent := err == nil && bytes.Equal(again, raw)
	clear(again)
	return platformServiceEgressPostgresCall{command: kept, sha: platformDigest(raw), binding: binding, targetSHA: targetSHA, consistent: consistent}
}

// approveServiceEgressPostgresCheck answers one authority challenge of one probe call. It runs the
// whole authority load again (nothing is carried over from the admit) inside the one-second budget
// of a check, resolving the target and binding without opening the value, and signs the proof only
// when the call's command is still exactly what this authority would sign now: a revoked grant,
// binding or credential, a changed policy or target, a withdrawn service principal all differ. A
// refusal is the absence of a proof.
func (s *Server) approveServiceEgressPostgresCheck(ctx context.Context, state platformServiceEgressSessionState, call platformServiceEgressPostgresCall, challenge string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, platformServiceEgressCheckBudget)
	defer cancel()
	command := call.command
	if !call.consistent || call.binding.id == "" || call.binding.operation != "pg_password" {
		return nil, nil, errPlatformServiceEgressDenied
	}
	a, err := s.loadServiceEgressForSessionPostgres(ctx, state, command.TargetID, command.CredentialName, false)
	if err != nil {
		return nil, nil, err
	}
	defer a.Close()
	now := time.Now().UnixMilli()
	// The call no longer holds the role: the fields are validated as signed, with stand-ins of the
	// right shape for the two values, which the metadata validation only requires to be present.
	checked := command
	checked.Username, checked.Password = []byte("x"), []byte("x")
	if !a.hasPostgres() ||
		command.InstallationID != a.installation ||
		command.ActorScope != platformServiceEgressActorScope(state.sessionID) ||
		command.PolicySHA256 != a.postgres.SHA256 || command.TargetID != a.pgTarget.ID || call.targetSHA != a.pgTargetSHA ||
		command.PrivateNetworksSHA256 != a.postgres.Policy.PrivateNetworksSHA256 ||
		a.binding != call.binding || command.CredentialName != a.credentialName ||
		command.Generation != a.facts.generation || command.PolicyRevision != a.facts.policyRevision ||
		command.ReleaseSHA256 != a.facts.release ||
		control.ValidatePostgresProbeMetadata(checked, a.authority, now) != nil {
		return nil, nil, errPlatformServiceEgressDenied
	}
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil || expires > state.expiresMS {
		return nil, nil, errPlatformServiceEgressDenied
	}
	key := ed25519.NewKeyFromSeed(a.controlSeed)
	defer clear(key)
	proof, signature, err := control.SignPostgresProbeCheckDigest(call.sha, challenge, expires, key, now)
	if err != nil || ctx.Err() != nil {
		return nil, nil, errPlatformServiceEgressDenied
	}
	return proof, signature, nil
}

// serveCall dispatches the postgres admit of a session; see serveAdmit for the HTTP twin.
func (c *platformServiceEgressCoordinator) servePostgres(ctx context.Context, l *platformServiceEgressLive, call *transport.EgressCall, payload []byte) {
	installation := l.state.pin.installation
	admit, err := transport.DecodeEgressPostgresAdmit(payload)
	if err != nil {
		c.refuse(call, transport.EgressRefusalInvalid)
		return
	}
	// record counts one finished call. A call is counted against its target only once the target
	// and binding resolved to reviewed ones: a name the application made up is counted as
	// unresolved, never by name.
	resolved := ""
	record := func(class byte, status uint16) {
		c.audit.recordPostgres(installation, l.state.sessionID, c.now(), resolved, class, status)
	}
	load, cancel := context.WithTimeout(ctx, platformServiceEgressAdmitLoadTimeout)
	a, err := c.server.loadServiceEgressForSessionPostgres(load, l.state, admit.Target, admit.Credential, true)
	cancel()
	if err != nil {
		code, class := transport.EgressRefusalUnavailable, transport.EgressOutcomeUnavailable
		if errors.Is(err, errPlatformServiceEgressDenied) {
			// An unknown target, or an unknown, revoked, unreviewed, wrong-principal or wrong-target
			// binding: refused before anything was decrypted.
			code, class = transport.EgressRefusalDenied, transport.EgressOutcomeDenied
			c.recordDenied(installation, l, "POSTGRES", "")
		}
		c.refuse(call, code)
		record(class, 0)
		return
	}
	binding, targetSHA := a.binding, a.pgTargetSHA
	resolved = admit.Target
	command, raw, signature, err := a.signServiceEgressPostgres(l.state.sessionID, admit, time.Now().UnixMilli(), l.state.expiresMS)
	a.Close()
	// The signed command is the one place the role lives in Portal outside the transaction; it is
	// wiped when the call ends.
	defer command.Clear()
	defer clear(raw)
	if err != nil {
		code, class := transport.EgressRefusalUnavailable, transport.EgressOutcomeUnavailable
		if errors.Is(err, errPlatformServiceEgressDenied) {
			code, class = transport.EgressRefusalDenied, transport.EgressOutcomeDenied
			c.recordDenied(installation, l, "POSTGRES", "")
		}
		c.refuse(call, code)
		record(class, 0)
		return
	}
	frame, err := transport.EncodeEgressSigned(raw, signature)
	if err != nil {
		return
	}
	sent := call.Send(transport.EgressFrameCommand, frame)
	// From here the checks need the command's fields and digest, not the role: wipe the role, the
	// signed bytes and the frame copy now, not at the end of a call that may wait on the database
	// for its whole deadline.
	kept := newPlatformServiceEgressPostgresCall(command, raw, binding, targetSHA)
	clear(frame)
	clear(raw)
	command.Clear()
	if sent != nil {
		return
	}
	withheld := false
	for {
		f, err := call.Recv(ctx)
		if err != nil {
			return
		}
		switch f.Type {
		case transport.EgressFrameChallenge:
			_, nonce, err := transport.DecodeEgressChallenge(f.Payload)
			if err != nil {
				return
			}
			proof, proofSignature, err := c.server.approveServiceEgressPostgresCheck(ctx, l.state, kept, hex.EncodeToString(nonce[:]))
			if err != nil {
				// A refused check is the absence of a proof: answering twice would close the
				// session, and the runner treats silence as a denial.
				withheld = true
				continue
			}
			payload, err := transport.EncodeEgressSigned(proof, proofSignature)
			if err != nil || call.Send(transport.EgressFrameProof, payload) != nil {
				return
			}
		case transport.EgressFrameOutcome:
			outcome, err := transport.DecodeEgressOutcome(f.Payload)
			if err != nil {
				return
			}
			record(outcome.Class, outcome.Status)
			if outcome.Class == transport.EgressOutcomeDenied && !withheld {
				c.recordDenied(installation, l, "POSTGRES", "")
			}
			return
		default:
			return
		}
	}
}

func marshalPlatformServiceEgressPostgres(c control.PostgresProbeCommand) ([]byte, error) {
	return json.Marshal(c)
}
