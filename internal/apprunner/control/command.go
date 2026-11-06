// Package control validates experimental signed runner messages. Cryptographic
// validity does not prove journal admission, peer identity or physical effects.
package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"unicode/utf8"
)

var ErrInvalidCommand = errors.New("invalid runner command or authority")

const MaxCommandBytes = 65536
const Format = "portal.runner.command/1-dev.1"

type PrepareDataPayload struct {
	DataNamespaceID string `json:"data_namespace_id"`
	ReleaseSHA256   string `json:"release_sha256"`
	DataFormat      string `json:"data_format"`
	QuotaBytes      string `json:"quota_bytes"`
	QuotaInodes     string `json:"quota_inodes"`
	WriterFence     string `json:"writer_fence"`
}
type Command struct {
	Format            string             `json:"format"`
	DeploymentID      string             `json:"deployment_id"`
	ControllerEpoch   string             `json:"controller_epoch"`
	ControlRevision   string             `json:"control_revision"`
	InstallationID    string             `json:"installation_id"`
	OperationID       string             `json:"operation_id"`
	TargetGeneration  string             `json:"target_generation"`
	Stage             string             `json:"stage"`
	EffectNumber      string             `json:"effect_number"`
	EffectID          string             `json:"effect_id"`
	Kind              string             `json:"kind"`
	InstallationFence string             `json:"installation_fence"`
	PayloadSHA256     string             `json:"payload_sha256"`
	IssuedMS          string             `json:"issued_ms"`
	ExpiresMS         string             `json:"expires_ms"`
	Payload           PrepareDataPayload `json:"payload"`
}
type Authority struct {
	DeploymentID, ControllerEpoch, Fingerprint string
	ControlRevision                            int64
	PublicKey                                  []byte
}
type VerifiedCommand struct{ command Command }

func (v VerifiedCommand) Command() Command { return v.command }

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var fieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func frame(purpose string, raw []byte) []byte {
	result := append([]byte(purpose), 0)
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(len(raw)))
	result = append(result, size...)
	result = append(result, raw...)
	return result
}
func counter(value string, positive bool) (int64, bool) {
	if len(value) == 0 || len(value) > 19 || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && (!positive || n > 0)
}
func validLogical(c Command) bool {
	if c.Format != Format || c.Stage != "prepare_data" || c.Kind != "prepare-data" {
		return false
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.OperationID, c.InstallationID, c.Payload.DataNamespaceID} {
		if !uuidPattern.MatchString(id) {
			return false
		}
	}
	if !shaPattern.MatchString(c.Payload.ReleaseSHA256) {
		return false
	}
	for _, number := range []string{c.TargetGeneration, c.EffectNumber, c.Payload.DataFormat, c.Payload.QuotaBytes, c.Payload.QuotaInodes, c.Payload.WriterFence} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	slot, _ := counter(c.EffectNumber, true)
	format, _ := counter(c.Payload.DataFormat, true)
	return slot <= 32 && format <= 1000000
}

// LogicalDigest binds the stable reviewed effect identity and typed payload.
// Transport timing, installation fence and control revision are excluded so a
// renewed envelope can observe the same effect. Durable floors remain mandatory.
func LogicalDigest(c Command) (string, error) {
	if !validLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string             `json:"format"`
		DeploymentID     string             `json:"deployment_id"`
		ControllerEpoch  string             `json:"controller_epoch"`
		OperationID      string             `json:"operation_id"`
		InstallationID   string             `json:"installation_id"`
		TargetGeneration string             `json:"target_generation"`
		Stage            string             `json:"stage"`
		EffectNumber     string             `json:"effect_number"`
		Kind             string             `json:"kind"`
		Payload          PrepareDataPayload `json:"payload"`
	}{"portal.runner.effect/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.OperationID, c.InstallationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame("portal.app/runner-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func strictObject(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !fieldPattern.MatchString(key) || seen[key] || len(seen) >= 32 {
			return false
		}
		seen[key] = true
		token, err = d.Token()
		if err != nil {
			return false
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter != json.Delim('{') || !strictObject(d, depth+1) {
				return false
			}
		}
	}
	token, err := d.Token()
	return err == nil && token == json.Delim('}')
}
func parse(raw []byte) (Command, error) {
	var command Command
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) {
		return command, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return command, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return command, ErrInvalidCommand
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil {
		return Command{}, ErrInvalidCommand
	}
	return command, nil
}

// Verify checks a bounded message against an already trusted controller snapshot
// at the supplied trusted-clock sample. It never admits a replay/fence or dispatches
// an effect; the journal and OS peer boundary must still authorize the request.
func Verify(raw, signature []byte, authority Authority, nowMS int64) (VerifiedCommand, error) {
	fail := func() (VerifiedCommand, error) { return VerifiedCommand{}, ErrInvalidCommand }
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	command, err := parse(raw)
	if err != nil {
		return fail()
	}
	revision, ok := counter(command.ControlRevision, true)
	if !ok || revision != authority.ControlRevision || command.DeploymentID != authority.DeploymentID || command.ControllerEpoch != authority.ControllerEpoch {
		return fail()
	}
	if !uuidPattern.MatchString(command.EffectID) {
		return fail()
	}
	if _, ok := counter(command.InstallationFence, true); !ok {
		return fail()
	}
	issued, ok := counter(command.IssuedMS, false)
	if !ok {
		return fail()
	}
	expires, ok := counter(command.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || issued > nowMS || nowMS >= expires {
		return fail()
	}
	expected, err := LogicalDigest(command)
	if err != nil || command.PayloadSHA256 != expected {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame("portal.app/runner-command/v1", raw), signature) {
		return fail()
	}
	return VerifiedCommand{command: command}, nil
}

// Sign encodes and signs an already constructed command under explicit authority.
// It never grants a lease or admits an effect; callers must supply those durable checks.
func Sign(command Command, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame("portal.app/runner-command/v1", raw))
	if _, err := Verify(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
