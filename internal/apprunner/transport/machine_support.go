package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The machine support query tells Portal whether a runner forwards machine assertions (doc 183
// section 4.7). Like the network targets query it is a separate query, never a field of a runtime
// profile: a profile is compared byte for byte with the plan Portal stored, so a new field there would
// break every installation (doc 175 amendment 1). A runner that predates the query closes the
// connection at the unknown magic; Portal then does not offer the capability against it.
const (
	// MachineSupportQueryMagic is followed by a 32-byte nonce: Portal -> runner.
	MachineSupportQueryMagic = "PRMA0001"
	// MachineSupportReplyMagic is followed by the nonce, a big-endian u32 length and the closed JSON
	// of MachineSupport: runner -> Portal.
	MachineSupportReplyMagic = "PRMB0001"
	MachineSupportFormat     = "portal.runner.machine-assertions/1-dev.1"
	maxMachineSupportBytes   = 512
)

// ErrMachineSupportUnsupported means the runner does not answer the query: it predates it, or the
// reply was not the closed format.
var ErrMachineSupportUnsupported = errors.New("runner does not support machine assertions")

// MachineSupport is what a runner reports: the visible assertion prefixes it forwards beside the
// viewer's.
type MachineSupport struct {
	Format            string   `json:"format"`
	AssertionPrefixes []string `json:"assertion_prefixes"`
}

// NewMachineSupport is the advertisement of this runner build.
func NewMachineSupport() MachineSupport {
	return MachineSupport{Format: MachineSupportFormat, AssertionPrefixes: []string{appplatform.MachineAssertionPrefix}}
}

// Supports reports whether the advertisement names the machine assertion prefix.
func (m MachineSupport) Supports() bool {
	return m.Format == MachineSupportFormat && slices.Contains(m.AssertionPrefixes, appplatform.MachineAssertionPrefix)
}

// EncodeMachineSupport returns the canonical reply body of a valid advertisement.
func EncodeMachineSupport(m MachineSupport) ([]byte, error) {
	if !m.Supports() {
		return nil, ErrMachineSupportUnsupported
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > maxMachineSupportBytes {
		return nil, ErrMachineSupportUnsupported
	}
	return raw, nil
}

// ParseMachineSupport accepts only the exact canonical encoding of an advertisement that names the
// machine assertion prefix.
func ParseMachineSupport(raw []byte) (MachineSupport, error) {
	var m MachineSupport
	if len(raw) == 0 || len(raw) > maxMachineSupportBytes || !utf8.Valid(raw) {
		return MachineSupport{}, ErrMachineSupportUnsupported
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return MachineSupport{}, ErrMachineSupportUnsupported
	}
	canonical, err := EncodeMachineSupport(m)
	if err != nil || !bytes.Equal(canonical, raw) {
		return MachineSupport{}, ErrMachineSupportUnsupported
	}
	return m, nil
}
