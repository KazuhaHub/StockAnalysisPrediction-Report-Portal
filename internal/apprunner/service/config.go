package service

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigFormat    = "portal.app-runner.config/1-dev.1"
	BootstrapFormat = "portal.app-runner.bootstrap/1-dev.1"
	maxConfigBytes  = 16 << 10
)

var ErrConfig = errors.New("invalid app-runner configuration")

type Controller struct {
	DeploymentID    string `json:"deployment_id"`
	ControllerEpoch string `json:"controller_epoch"`
	ControlRevision int64  `json:"control_revision"`
	Fingerprint     string `json:"fingerprint"`
}

type Config struct {
	Format         string     `json:"format"`
	DockerSocket   string     `json:"docker_socket"`
	JournalDir     string     `json:"journal_dir"`
	DataRoot       string     `json:"data_root"`
	IPCRoot        string     `json:"ipc_root"`
	ControlRoot    string     `json:"control_root"`
	ControlSocket  string     `json:"control_socket"`
	ControllerUID  uint32     `json:"controller_uid"`
	ControllerGID  uint32     `json:"controller_gid"`
	NetworkActive  int        `json:"network_active"`
	NetworkEntries int        `json:"network_entries"`
	MinimumCPUs    int        `json:"minimum_reported_cpus"`
	MinimumMemory  int64      `json:"minimum_reported_memory_bytes"`
	Controller     Controller `json:"controller"`
}

type BootstrapConfig struct {
	Format              string     `json:"format"`
	JournalDir          string     `json:"journal_dir"`
	Controller          Controller `json:"controller"`
	ControllerPublicKey string     `json:"controller_public_key"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	raw, err := readPrivateConfig(path)
	if err != nil || decodeStrict(raw, &cfg) != nil || !validController(cfg.Controller) || cfg.Format != ConfigFormat ||
		!validAbsolute(cfg.DockerSocket) || !validAbsolute(cfg.JournalDir) || !validAbsolute(cfg.DataRoot) ||
		!validAbsolute(cfg.IPCRoot) || !validAbsolute(cfg.ControlRoot) || !validAbsolute(cfg.ControlSocket) ||
		filepath.Dir(cfg.ControlSocket) != cfg.ControlRoot || !validIdentity(cfg.ControllerUID, cfg.ControllerGID) ||
		cfg.NetworkActive < 1 || cfg.NetworkActive > 16 || cfg.NetworkEntries < 16 || cfg.NetworkEntries > 4096 ||
		cfg.MinimumCPUs < 1 || cfg.MinimumCPUs > 1024 || cfg.MinimumMemory < 64<<20 || cfg.MinimumMemory > 1<<50 ||
		overlap(cfg.JournalDir, cfg.DataRoot) || overlap(cfg.JournalDir, cfg.IPCRoot) || overlap(cfg.JournalDir, cfg.ControlRoot) ||
		overlap(cfg.DataRoot, cfg.IPCRoot) || overlap(cfg.DataRoot, cfg.ControlRoot) || overlap(cfg.IPCRoot, cfg.ControlRoot) {
		return Config{}, ErrConfig
	}
	return cfg, nil
}

func LoadBootstrapConfig(path string) (BootstrapConfig, error) {
	var cfg BootstrapConfig
	raw, err := readPrivateConfig(path)
	if err != nil || decodeStrict(raw, &cfg) != nil || cfg.Format != BootstrapFormat || !validAbsolute(cfg.JournalDir) || !validController(cfg.Controller) {
		return BootstrapConfig{}, ErrConfig
	}
	if _, err := controllerPublicKey(cfg); err != nil {
		return BootstrapConfig{}, ErrConfig
	}
	return cfg, nil
}

func controllerPublicKey(cfg BootstrapConfig) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(cfg.ControllerPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(key) != cfg.ControllerPublicKey {
		return nil, ErrConfig
	}
	sum := sha256.Sum256(key)
	if cfg.Controller.Fingerprint != hex.EncodeToString(sum[:]) {
		return nil, ErrConfig
	}
	return key, nil
}

func readPrivateConfig(path string) ([]byte, error) {
	if !validAbsolute(path) {
		return nil, ErrConfig
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parent != filepath.Dir(path) {
		return nil, ErrConfig
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() < 2 || before.Size() > maxConfigBytes || !ownedByEffectiveUser(before) {
		return nil, ErrConfig
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrConfig
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return nil, ErrConfig
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(raw) > maxConfigBytes || len(raw) == 0 {
		return nil, ErrConfig
	}
	return raw, nil
}

func decodeStrict(raw []byte, out any) error {
	scanner := json.NewDecoder(bytes.NewReader(raw))
	token, err := scanner.Token()
	if err != nil || token != json.Delim('{') {
		return ErrConfig
	}
	if scanStrictObjectBody(scanner, 1) != nil {
		return ErrConfig
	}
	if _, err := scanner.Token(); err != io.EOF {
		return ErrConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return ErrConfig
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrConfig
	}
	return nil
}

func scanStrictValue(scanner *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrConfig
	}
	token, err := scanner.Token()
	if err != nil {
		return ErrConfig
	}
	switch token {
	case json.Delim('{'):
		return scanStrictObjectBody(scanner, depth+1)
	case json.Delim('['):
		for scanner.More() {
			if scanStrictValue(scanner, depth+1) != nil {
				return ErrConfig
			}
		}
		close, err := scanner.Token()
		if err != nil || close != json.Delim(']') {
			return ErrConfig
		}
	}
	return nil
}

func scanStrictObjectBody(scanner *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for scanner.More() {
		token, err := scanner.Token()
		key, ok := token.(string)
		if err != nil || !ok || key == "" || seen[key] || seen[strings.ToLower(key)] || key != strings.ToLower(key) {
			return ErrConfig
		}
		seen[key] = true
		if scanStrictValue(scanner, depth+1) != nil {
			return ErrConfig
		}
	}
	close, err := scanner.Token()
	if err != nil || close != json.Delim('}') {
		return ErrConfig
	}
	return nil
}

func validController(c Controller) bool {
	return validUUID(c.DeploymentID) && validUUID(c.ControllerEpoch) && c.ControlRevision > 0 && validDigest(c.Fingerprint)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validAbsolute(path string) bool {
	return path != "" && len(path) <= 4096 && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func validIdentity(uid, gid uint32) bool {
	return uid != 0 && gid != 0 && uid != ^uint32(0) && gid != ^uint32(0)
}

func overlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	return left == right || strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}
