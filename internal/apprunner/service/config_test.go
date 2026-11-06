package service

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func controllerFixture() Controller {
	return Controller{
		DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002",
		ControlRevision: 1, Fingerprint: strings.Repeat("a", 64),
	}
}

func configFixture(canonical string) Config {
	return Config{
		Format: ConfigFormat, DockerSocket: "/run/docker.sock", JournalDir: filepath.Join(canonical, "journal"),
		DataRoot: filepath.Join(canonical, "data"), CheckpointRoot: filepath.Join(canonical, "checkpoints"),
		IPCRoot: filepath.Join(canonical, "ipc"), ControlRoot: filepath.Join(canonical, "control"),
		ControlSocket: filepath.Join(canonical, "control", "runner.sock"), ControllerUID: 41001, ControllerGID: 41001,
		NetworkActive: 4, NetworkEntries: 1024, MinimumCPUs: 2, MinimumMemory: 2 << 30, Controller: controllerFixture(),
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "runner-service-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writePrivate(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigStrictAndBounded(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil || loaded != cfg {
		t.Fatalf("load valid config: %+v %v", loaded, err)
	}
	for name, raw := range map[string]string{
		"unknown":          `{"format":"` + ConfigFormat + `","secret":"private"}`,
		"duplicate":        `{"format":"` + ConfigFormat + `","format":"` + ConfigFormat + `"}`,
		"nested_duplicate": `{"format":"` + ConfigFormat + `","controller":{"deployment_id":"x","deployment_id":"y"}}`,
		"trailing":         `{}` + `{}`,
		"case":             `{"Format":"` + ConfigFormat + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestConfigRefusesExposureAndOverlappingRoots(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	for name, mutate := range map[string]func(*Config){
		"mode":            func(*Config) {},
		"control_outside": func(c *Config) { c.ControlSocket = filepath.Join(dir, "outside.sock") },
		"overlap":         func(c *Config) { c.DataRoot = c.JournalDir },
		"checkpoint_overlap": func(c *Config) {
			c.CheckpointRoot = c.DataRoot
		},
		"missing_checkpoint": func(c *Config) { c.CheckpointRoot = "" },
		"zero_controller":    func(c *Config) { c.ControllerUID = 0 },
		"network_overflow":   func(c *Config) { c.NetworkEntries = 4097 },
		"relative":           func(c *Config) { c.DataRoot = "relative" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := configFixture(dir)
			mutate(&cfg)
			writePrivate(t, path, cfg)
			if name == "mode" {
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("unsafe config accepted")
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBootstrapConfigPinsPublicKey(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "bootstrap.json")
	public, _, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(public)
	cfg := BootstrapConfig{
		Format: BootstrapFormat, JournalDir: dir, ControllerPublicKey: base64.RawURLEncoding.EncodeToString(public),
		Controller: Controller{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002", ControlRevision: 1, Fingerprint: hex.EncodeToString(sum[:])},
	}
	writePrivate(t, path, cfg)
	loaded, err := LoadBootstrapConfig(path)
	if err != nil || loaded.ControllerPublicKey != cfg.ControllerPublicKey {
		t.Fatalf("load bootstrap config: %+v %v", loaded, err)
	}
	cfg.Controller.Fingerprint = strings.Repeat("b", 64)
	writePrivate(t, path, cfg)
	if _, err := LoadBootstrapConfig(path); err == nil {
		t.Fatal("mismatched public key pin accepted")
	}
}

func TestBootstrapFunctionRequiresCanonicalPinnedPublicKey(t *testing.T) {
	for name, mutate := range map[string]func(*BootstrapConfig){
		"noncanonical": func(c *BootstrapConfig) {
			c.ControllerPublicKey += "="
		},
		"fingerprint": func(c *BootstrapConfig) {
			c.Controller.Fingerprint = strings.Repeat("b", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := canonicalTempDir(t)
			seed := bytes.Repeat([]byte{5}, ed25519.SeedSize)
			public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
			sum := sha256.Sum256(public)
			cfg := BootstrapConfig{
				Format: BootstrapFormat, JournalDir: dir, ControllerPublicKey: base64.RawURLEncoding.EncodeToString(public),
				Controller: Controller{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002", ControlRevision: 1, Fingerprint: hex.EncodeToString(sum[:])},
			}
			mutate(&cfg)
			if _, err := Bootstrap(t.Context(), cfg); err == nil {
				t.Fatal("invalid bootstrap document accepted")
			}
		})
	}
}

func TestBootstrapIsExplicitAndIdempotent(t *testing.T) {
	dir := canonicalTempDir(t)
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(public)
	cfg := BootstrapConfig{
		Format: BootstrapFormat, JournalDir: dir, ControllerPublicKey: base64.RawURLEncoding.EncodeToString(public),
		Controller: Controller{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002", ControlRevision: 1, Fingerprint: hex.EncodeToString(sum[:])},
	}
	first, err := Bootstrap(t.Context(), cfg)
	if err != nil || !first.Created || first.Deployment.Fingerprint != cfg.Controller.Fingerprint {
		t.Fatalf("explicit bootstrap: %+v %v", first, err)
	}
	second, err := Bootstrap(t.Context(), cfg)
	if err != nil || second.Created || second.Deployment.DeploymentID != first.Deployment.DeploymentID {
		t.Fatalf("idempotent bootstrap: %+v %v", second, err)
	}
	cfg.Controller.ControlRevision = 2
	if _, err := Bootstrap(t.Context(), cfg); err == nil {
		t.Fatal("bootstrap replaced enrolled revision")
	}
}
