package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// PostgresProbeCommandFormat is the signed command of one PostgreSQL liveness check (doc 178). It
// carries the same authority tuple as NetworkHTTPRequestCommand, the reviewed policy, the one
// target it is for and the monitor role's username and password, which Portal placed. The runner
// never opens a credential store: the password exists only in these bytes, for one check.
const PostgresProbeCommandFormat = "portal.runner.postgres-probe-request/1-dev.1"
const postgresProbePurpose = "portal.app/runner-postgres-probe/v1"

// PostgresProbeCheckFormat is the short authority proof the runner asks for before it resolves,
// before it dials and before it releases an outcome. It has its own format and signing purpose, so
// a proof for an HTTP request can never answer a probe's challenge, nor the other way round.
const PostgresProbeCheckFormat = "portal.runner.postgres-probe-check/1-dev.1"
const postgresProbeCheckPurpose = "portal.app/runner-postgres-probe-check/v1"

// PostgresProbeCommand is the signed command. It is not current SQL authority; the caller must
// recheck that online.
type PostgresProbeCommand struct {
	Format          string `json:"format"`
	DeploymentID    string `json:"deployment_id"`
	ControllerEpoch string `json:"controller_epoch"`
	ControlRevision string `json:"control_revision"`
	RequestID       string `json:"request_id"`
	InstallationID  string `json:"installation_id"`
	Generation      string `json:"generation"`
	PolicyRevision  string `json:"policy_revision"`
	ReleaseSHA256   string `json:"release_sha256"`
	ActorScope      string `json:"actor_scope"`
	PolicyRaw       []byte `json:"policy_raw"`
	PolicySHA256    string `json:"policy_sha256"`
	TargetID        string `json:"target_id"`
	// PrivateNetworksSHA256 is the runner allowlist digest the policy was reviewed against. It
	// equals the policy's own digest field and is present exactly when the policy has a private
	// target.
	PrivateNetworksSHA256 string `json:"private_networks_sha256,omitempty"`
	CredentialName        string `json:"credential_name"`
	Username              []byte `json:"username"`
	Password              []byte `json:"password"`
	IssuedMS              string `json:"issued_ms"`
	ExpiresMS             string `json:"expires_ms"`
}

// Clear wipes the credential values the command holds.
func (c *PostgresProbeCommand) Clear() {
	clear(c.Username)
	clear(c.Password)
	c.Username, c.Password = nil, nil
}

type VerifiedPostgresProbeCommand struct{ command PostgresProbeCommand }

// Command returns a copy; the caller wipes the credential values of the copy.
func (v VerifiedPostgresProbeCommand) Command() PostgresProbeCommand {
	c := v.command
	c.PolicyRaw = bytes.Clone(c.PolicyRaw)
	c.Username = bytes.Clone(c.Username)
	c.Password = bytes.Clone(c.Password)
	return c
}

// Clear releases the credential copy a verified command holds.
func (v *VerifiedPostgresProbeCommand) Clear() {
	if v != nil {
		v.command.Clear()
	}
}

// VerifyPostgresProbeCommand accepts only the exact canonical encoding of a valid command, signed
// by the enrolled controller key, inside its lifetime at nowMS.
func VerifyPostgresProbeCommand(raw, signature []byte, a Authority, nowMS int64) (VerifiedPostgresProbeCommand, error) {
	var zero VerifiedPostgresProbeCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) || len(signature) != ed25519.SignatureSize || len(a.PublicKey) != ed25519.PublicKeySize || a.ControlRevision < 1 || nowMS < 0 {
		return zero, ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return zero, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return zero, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidCommand
	}
	var c PostgresProbeCommand
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	fail := func() (VerifiedPostgresProbeCommand, error) {
		c.Clear()
		return zero, ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, raw) {
		clear(canonical)
		return fail()
	}
	clear(canonical)
	if validatePostgresProbe(c, a, nowMS) != nil || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(postgresProbePurpose, raw), signature) {
		return fail()
	}
	return VerifiedPostgresProbeCommand{command: c}, nil
}

// SignPostgresProbeCommand signs c and proves the result verifies, so a caller never emits a
// command a runner would refuse for a reason knowable here.
func SignPostgresProbeCommand(c PostgresProbeCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	sig := ed25519.Sign(key, frame(postgresProbePurpose, raw))
	verified, err := VerifyPostgresProbeCommand(raw, sig, a, nowMS)
	if err != nil {
		clear(raw)
		return nil, nil, err
	}
	verified.Clear()
	return raw, sig, nil
}

// ValidatePostgresProbeMetadata checks the bounded logical fields of a command against current
// enrollment. It verifies neither a signature nor SQL authority; Portal's checks use it on the
// fields it kept of a command whose role it has wiped, with stand-ins of the right shape.
func ValidatePostgresProbeMetadata(c PostgresProbeCommand, a Authority, nowMS int64) error {
	return validatePostgresProbe(c, a, nowMS)
}

func validatePostgresProbe(c PostgresProbeCommand, a Authority, nowMS int64) error {
	if c.Format != PostgresProbeCommandFormat || nowMS < 0 || a.ControlRevision < 1 || len(a.PublicKey) != ed25519.PublicKeySize {
		return ErrInvalidCommand
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.RequestID} {
		if !candidateUUID(id) {
			return ErrInvalidCommand
		}
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != a.ControlRevision || c.DeploymentID != a.DeploymentID || c.ControllerEpoch != a.ControllerEpoch {
		return ErrInvalidCommand
	}
	for _, number := range []string{c.Generation, c.PolicyRevision} {
		if _, ok := counter(number, true); !ok {
			return ErrInvalidCommand
		}
	}
	for _, digest := range []string{c.ReleaseSHA256, c.ActorScope} {
		if !shaPattern.MatchString(digest) || digest == strings.Repeat("0", 64) {
			return ErrInvalidCommand
		}
	}
	parsed, err := appplatform.ParsePostgresPolicy(c.PolicyRaw)
	if err != nil || parsed.SHA256 != c.PolicySHA256 || !appplatform.ValidPostgresTargetID(c.TargetID) {
		return ErrInvalidCommand
	}
	if _, ok := parsed.Policy.Select(c.TargetID); !ok || c.PrivateNetworksSHA256 != parsed.Policy.PrivateNetworksSHA256 {
		return ErrInvalidCommand
	}
	if !validNetworkCredentialName(c.CredentialName) || !appplatform.ValidPostgresUsername(c.Username) || !appplatform.ValidPostgresPassword(c.Password) {
		return ErrInvalidCommand
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || expires-issued < parsed.Policy.Deadline.Milliseconds() || issued > nowMS || nowMS >= expires {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || len(canonical) > MaxCommandBytes {
		clear(canonical)
		return ErrInvalidCommand
	}
	clear(canonical)
	return nil
}

// PostgresProbeCheck is the proof body. Its fields are those of the HTTP check, under its own
// format and purpose.
type PostgresProbeCheck struct {
	Format        string `json:"format"`
	RequestSHA256 string `json:"request_sha256"`
	Challenge     string `json:"challenge"`
	IssuedMS      string `json:"issued_ms"`
	ExpiresMS     string `json:"expires_ms"`
}

// SignPostgresProbeCheckDigest signs the check of a command the caller knows only by the SHA-256 of
// its signed bytes and its expiry. The caller has already judged the command and wiped it (it
// carried a password), and passes the digest it computed over the exact signed bytes it sent. It
// validates nothing about the command. A proof for any other digest is refused by the runner.
func SignPostgresProbeCheckDigest(requestSHA256, challenge string, requestExpiresMS int64, key ed25519.PrivateKey, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize || !shaPattern.MatchString(requestSHA256) || !shaPattern.MatchString(challenge) || challenge == strings.Repeat("0", 64) || requestExpiresMS <= nowMS {
		return nil, nil, ErrInvalidCommand
	}
	expires := requestExpiresMS
	if expires-nowMS > 1000 {
		expires = nowMS + 1000
	}
	proof := PostgresProbeCheck{Format: PostgresProbeCheckFormat, RequestSHA256: requestSHA256, Challenge: challenge, IssuedMS: strconv.FormatInt(nowMS, 10), ExpiresMS: strconv.FormatInt(expires, 10)}
	raw, err := json.Marshal(proof)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	return raw, ed25519.Sign(key, frame(postgresProbeCheckPurpose, raw)), nil
}

// VerifyPostgresProbeCheck verifies a proof against the command it answers, the challenge the
// runner sent and the enrolled key. The command is re-encoded to compute the digest the proof must
// bind; the proof may not outlive the command or last longer than a second.
func VerifyPostgresProbeCheck(c PostgresProbeCommand, challenge string, raw, signature []byte, a Authority, nowMS int64) error {
	if len(raw) == 0 || len(raw) > MaxNetworkHTTPCheckBytes || !utf8.Valid(raw) || len(signature) != ed25519.SignatureSize {
		return ErrInvalidCommand
	}
	if !shaPattern.MatchString(challenge) || challenge == strings.Repeat("0", 64) || validatePostgresProbe(c, a, nowMS) != nil {
		return ErrInvalidCommand
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return ErrInvalidCommand
	}
	sum := sha256.Sum256(encoded)
	clear(encoded)
	var proof PostgresProbeCheck
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&proof) != nil {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(raw, canonical) || proof.Format != PostgresProbeCheckFormat || proof.RequestSHA256 != hex.EncodeToString(sum[:]) || proof.Challenge != challenge {
		return ErrInvalidCommand
	}
	issued, ok := counter(proof.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(proof.ExpiresMS, false)
	requestExpires, _ := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 1000 || issued > nowMS || nowMS >= expires || expires > requestExpires {
		return ErrInvalidCommand
	}
	if !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(postgresProbeCheckPurpose, raw), signature) {
		return ErrInvalidCommand
	}
	return nil
}
