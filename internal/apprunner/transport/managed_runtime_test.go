package transport

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func managedRuntimeVector(t *testing.T) (managedRuntimeWire, control.Authority) {
	t.Helper()
	a, _, _, _ := candidateReceiptAuthority(t)
	raw, err := encodeEnrollment(journal.ControllerEnrollment{DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: a.ControlRevision, PublicKey: a.PublicKey, Fingerprint: a.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	var e enrollmentWire
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return managedRuntimeWire{Format: "portal.runner.managed-profile/1-dev.1", Controller: e, Profile: ManagedRuntimeProfile{Protocol: ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: ManagedBackendProtocol, SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: "00000000-0000-4000-8000-000000000999", RootInode: "18446744073709551615"}, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}, a
}
func TestManagedRuntimeCanonicalMetadataAndOpaqueBoundary(t *testing.T) {
	w, a := managedRuntimeVector(t)
	raw, _ := json.Marshal(w)
	parsed, got, err := decodeManagedRuntime(raw)
	if err != nil || parsed.Profile != w.Profile || !sameManagedAuthority(got, a) {
		t.Fatal("profile codec", err)
	}
	proof := ManagedRuntimeObservation{profile: w.Profile, authority: a, receivedAt: time.Now()}
	copy := proof.Profile()
	copy.Platform = "linux/other"
	key := proof.Controller()
	key.PublicKey[0] ^= 1
	if proof.Profile() != w.Profile || proof.Controller().PublicKey[0] != a.PublicKey[0] {
		t.Fatal("profile/key aliases opaque observation")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("profile proof serialized")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("JSON minted/retained profile provenance")
	}
	for _, mode := range []string{"ready", "platform", "inode", "protocol", "root", "unknown", "trailing", "timestamp"} {
		changed, _ := managedRuntimeVector(t)
		switch mode {
		case "ready":
			changed.Profile.ExecutionReady = true
		case "platform":
			changed.Profile.Platform = "linux/other"
		case "inode":
			changed.Profile.RootInode = "018"
		case "protocol":
			changed.Profile.Protocol = "1.0.0"
		case "root":
			changed.Profile.RootFilesystemUUID = "00000000-0000-0000-0000-000000000000"
		case "timestamp":
			changed.ObservedMS = "-1"
		}
		body, _ := json.Marshal(changed)
		if mode == "unknown" {
			body = append(body[:len(body)-1], []byte(`,"unknown":true}`)...)
		}
		if mode == "trailing" {
			body = append(body, ' ')
		}
		if _, _, err := decodeManagedRuntime(body); err == nil {
			t.Fatal("invalid profile metadata accepted", mode)
		}
	}
}

func TestManagedRuntimeWritableAdapterIsConfigurationOnly(t *testing.T) {
	w, _ := managedRuntimeVector(t)
	w.Profile.WritableSandboxProfile = control.WritableCandidateSandboxProfile
	raw, _ := json.Marshal(w)
	parsed, _, err := decodeManagedRuntime(raw)
	if err != nil || parsed.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile || parsed.Profile.ExecutionReady {
		t.Fatal("writable configuration", err)
	}
	w.Profile.WritableSandboxProfile = "unrestricted"
	raw, _ = json.Marshal(w)
	if _, _, err := decodeManagedRuntime(raw); err == nil {
		t.Fatal("unrecognized writable adapter")
	}
}

func TestManagedRuntimeProfileCarriesTheEffectiveCapacity(t *testing.T) {
	w, _ := managedRuntimeVector(t)
	w.Profile.Capacity = ManagedRuntimeCapacity{MemoryMiB: "4096", CPUMillis: "2000", DiskBytes: "8589934592", ImageStoreBytes: "10737418240", RunningBackends: "8", EstimatedRows: "3"}
	raw, _ := json.Marshal(w)
	parsed, _, err := decodeManagedRuntime(raw)
	if err != nil || parsed.Profile.Capacity != w.Profile.Capacity {
		t.Fatalf("capacity facts did not survive the codec: %+v %v", parsed.Profile.Capacity, err)
	}
	for name, mutate := range map[string]func(*ManagedRuntimeCapacity){
		"not_decimal": func(c *ManagedRuntimeCapacity) { c.MemoryMiB = "4 GiB" },
		"negative":    func(c *ManagedRuntimeCapacity) { c.CPUMillis = "-1" },
		"leading_0":   func(c *ManagedRuntimeCapacity) { c.DiskBytes = "08" },
		"zero_budget": func(c *ManagedRuntimeCapacity) { c.RunningBackends = "0" },
		"partial":     func(c *ManagedRuntimeCapacity) { c.EstimatedRows = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := w
			mutate(&bad.Profile.Capacity)
			raw, _ := json.Marshal(bad)
			if _, _, err := decodeManagedRuntime(raw); err == nil {
				t.Fatal("malformed capacity facts accepted")
			}
		})
	}
}
