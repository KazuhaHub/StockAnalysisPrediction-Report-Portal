package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const ManagedRunnerProtocol = "1.0.0-dev.1"
const ManagedBackendProtocol = "portal.backend/1"
const MaxManagedRuntimeAge = 3 * time.Second

var ErrManagedRuntimeObservation = errors.New("authenticated managed runtime profile unavailable")

// ManagedRuntimeProfile reports configuration/protocol facts, not app health or
// permission to execute/activate. Native integer wire values use decimal strings.
type ManagedRuntimeProfile struct {
	Protocol               string `json:"protocol"`
	Platform               string `json:"platform"`
	BackendProtocol        string `json:"backend_protocol"`
	SandboxProfile         string `json:"sandbox_profile"`
	RootFilesystemUUID     string `json:"root_filesystem_uuid"`
	RootInode              string `json:"root_inode"`
	WritableSandboxProfile string `json:"writable_sandbox_profile,omitempty"`
	ExecutionReady         bool   `json:"execution_ready"`
	// Capacity is the effective budgets the runner admits against and the number of reservations still
	// estimated at the descriptor maxima. All decimal strings; the zero value (nothing reported) is accepted so
	// that a runner without a declared envelope remains readable.
	Capacity ManagedRuntimeCapacity `json:"capacity"`
}

// ManagedRuntimeCapacity reports the declared capacity envelope (doc 171).
type ManagedRuntimeCapacity struct {
	MemoryMiB       string `json:"memory_mib,omitempty"`
	CPUMillis       string `json:"cpu_millis,omitempty"`
	DiskBytes       string `json:"disk_bytes,omitempty"`
	ImageStoreBytes string `json:"image_store_bytes,omitempty"`
	RunningBackends string `json:"running_backends,omitempty"`
	EstimatedRows   string `json:"estimated_rows,omitempty"`
}

func validManagedCapacity(c ManagedRuntimeCapacity) bool {
	if c == (ManagedRuntimeCapacity{}) {
		return true
	}
	decimal := func(v string, positive bool) bool {
		n, err := strconv.ParseInt(v, 10, 64)
		return err == nil && n >= 0 && (!positive || n > 0) && strconv.FormatInt(n, 10) == v
	}
	return decimal(c.MemoryMiB, true) && decimal(c.CPUMillis, true) && decimal(c.DiskBytes, true) && decimal(c.ImageStoreBytes, true) &&
		decimal(c.RunningBackends, true) && decimal(c.EstimatedRows, false)
}

type managedRuntimeWire struct {
	Format     string                `json:"format"`
	Controller enrollmentWire        `json:"controller"`
	Profile    ManagedRuntimeProfile `json:"profile"`
	ObservedMS string                `json:"observed_ms"`
}

func validManagedProfile(p ManagedRuntimeProfile) bool {
	inode, err := strconv.ParseUint(p.RootInode, 10, 64)
	return err == nil && inode > 0 && strconv.FormatUint(inode, 10) == p.RootInode && observationUUID.MatchString(p.RootFilesystemUUID) && p.RootFilesystemUUID != "00000000-0000-0000-0000-000000000000" && p.Protocol == ManagedRunnerProtocol && (p.Platform == "linux/amd64" || p.Platform == "linux/arm64") && p.BackendProtocol == ManagedBackendProtocol && p.SandboxProfile == control.CandidateSandboxProfile && !p.ExecutionReady && validManagedCapacity(p.Capacity) && (p.WritableSandboxProfile == "" || p.WritableSandboxProfile == control.WritableCandidateSandboxProfile)
}
func decodeManagedRuntime(raw []byte) (managedRuntimeWire, control.Authority, error) {
	var w managedRuntimeWire
	fail := func() (managedRuntimeWire, control.Authority, error) {
		return managedRuntimeWire{}, control.Authority{}, ErrManagedRuntimeObservation
	}
	if len(raw) == 0 || len(raw) > 4096 || json.Unmarshal(raw, &w) != nil || w.Format != "portal.runner.managed-profile/1-dev.1" || !validManagedProfile(w.Profile) {
		return fail()
	}
	encoded, err := json.Marshal(w)
	if err != nil || !bytes.Equal(raw, encoded) {
		return fail()
	}
	number, err := strconv.ParseInt(w.ObservedMS, 10, 64)
	if err != nil || number < 0 || strconv.FormatInt(number, 10) != w.ObservedMS {
		return fail()
	}
	controllerRaw, _ := json.Marshal(w.Controller)
	e, err := decodeEnrollment(controllerRaw)
	if err != nil {
		return fail()
	}
	return w, control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}, nil
}
func sameManagedAuthority(a, b control.Authority) bool {
	return a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey)
}

// ManagedRuntimeObservation is minted only by the authenticated local probe.
// JSON, callers and zero values cannot supply configuration provenance.
type ManagedRuntimeObservation struct {
	profile    ManagedRuntimeProfile
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
}

func (ManagedRuntimeObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrManagedRuntimeObservation
}
func (o *ManagedRuntimeObservation) UnmarshalJSON([]byte) error {
	*o = ManagedRuntimeObservation{}
	return ErrManagedRuntimeObservation
}
func (o ManagedRuntimeObservation) Profile() ManagedRuntimeProfile { return o.profile }
func (o ManagedRuntimeObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o ManagedRuntimeObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o ManagedRuntimeObservation) ReceivedAt() time.Time              { return o.receivedAt }
