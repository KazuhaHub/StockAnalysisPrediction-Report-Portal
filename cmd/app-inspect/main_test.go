package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func inspectorFixture(t *testing.T) (string, string) {
	t.Helper()
	var raw bytes.Buffer
	archive := zip.NewWriter(&raw)
	file, err := archive.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "<!doctype html><p>PRIVATE_ASSET_CONTENT</p>"); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.ReadFile("../../internal/appplatform/testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw.Bytes())
	descriptor = bytes.ReplaceAll(descriptor, []byte(strings.Repeat("a", 64)), []byte(hex.EncodeToString(sum[:])))
	dir := t.TempDir()
	descriptorPath, frontendPath := filepath.Join(dir, "descriptor.json"), filepath.Join(dir, "ui.zip")
	if err := os.WriteFile(descriptorPath, descriptor, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frontendPath, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return descriptorPath, frontendPath
}

func TestInspectorPrintsOnlyPublicMetadata(t *testing.T) {
	descriptor, frontend := inspectorFixture(t)
	var out, diagnostics bytes.Buffer
	code := run([]string{"-descriptor", descriptor, "-frontend", frontend}, &out, &diagnostics)
	if code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("%d: %s", code, &diagnostics)
	}
	var result struct {
		AppID            string `json:"app_id"`
		Verification     string `json:"verification"`
		DescriptorSHA256 string `json:"descriptor_sha256"`
		Frontend         struct {
			Files []json.RawMessage `json:"files"`
		} `json:"frontend"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AppID != "notes-sample" || result.Verification != "frontend_integrity_only" || len(result.DescriptorSHA256) != 64 || len(result.Frontend.Files) != 1 {
		t.Fatalf("wrong output: %s", &out)
	}
	if strings.Contains(out.String(), "PRIVATE_ASSET_CONTENT") || strings.Contains(out.String(), descriptor) {
		t.Fatal("contents or local paths leaked")
	}
}

func offlineInspectorFixture(t *testing.T) string {
	t.Helper()
	descriptorPath, frontendPath := inspectorFixture(t)
	descriptor, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := os.ReadFile(frontendPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest appplatform.Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil {
		t.Fatal(err)
	}
	descriptorSum := sha256.Sum256(descriptor)
	signature, err := json.Marshal(map[string]string{
		"format":            "portal.app.signature/1-dev.1",
		"algorithm":         "Ed25519",
		"publisher":         "example-publisher",
		"key_id":            "fixture-key",
		"descriptor_sha256": hex.EncodeToString(descriptorSum[:]),
		"signature":         strings.Repeat("A", 86),
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for name, body := range map[string][]byte{
		"descriptor.json": descriptor,
		"descriptor.sig":  signature,
		"blobs/" + manifest.Frontend.SHA256 + "/ui.zip": frontend,
	} {
		header := &zip.FileHeader{Name: name}
		header.SetMode(0644)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sample.portalapp")
	if err := os.WriteFile(path, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfflineInspectorPrintsOnlyEnvelopeMetadata(t *testing.T) {
	path := offlineInspectorFixture(t)
	var out, diagnostics bytes.Buffer
	code := run([]string{"-envelope", path}, &out, &diagnostics)
	if code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("%d: %s", code, &diagnostics)
	}
	var result struct {
		Verification string `json:"verification"`
		AppID        string `json:"app_id"`
		Execution    string `json:"execution"`
		Frontend     struct {
			Files []json.RawMessage `json:"files"`
		} `json:"frontend"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Verification != "offline_envelope_frontend_integrity_only" ||
		result.AppID != "notes-sample" || result.Execution != "static" ||
		len(result.Frontend.Files) != 1 {
		t.Fatalf("wrong offline metadata: %s", &out)
	}
	if strings.Contains(out.String(), "PRIVATE_ASSET_CONTENT") ||
		strings.Contains(out.String(), path) {
		t.Fatal("offline output leaked contents or local paths")
	}
}

func TestOfflineInspectorRejectsMixedAndInvalidInput(t *testing.T) {
	path := offlineInspectorFixture(t)
	descriptor, frontend := inspectorFixture(t)
	for _, args := range [][]string{
		{"-envelope", path, "-descriptor", descriptor},
		{"-envelope", path, "-frontend", frontend},
		{"-envelope", filepath.Join(filepath.Dir(path), "missing.portalapp")},
		{"-envelope", descriptor},
		{"-envelope", path, "extra"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(args, &out, &diagnostics); code == 0 ||
			out.Len() != 0 || diagnostics.Len() == 0 {
			t.Fatalf("invalid offline input accepted: code=%d output=%s",
				code, &out)
		}
	}
}

func TestInspectorRefusesInputWithoutPartialSuccessOutput(t *testing.T) {
	descriptor, frontend := inspectorFixture(t)
	for _, args := range [][]string{
		{}, {"-descriptor", descriptor}, {"-descriptor", descriptor, "-frontend", frontend, "extra"},
		{"-unknown"}, {"-descriptor", filepath.Dir(descriptor), "-frontend", frontend},
		{"-descriptor", descriptor + ".missing", "-frontend", frontend},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(args, &out, &diagnostics); code == 0 || out.Len() != 0 || diagnostics.Len() == 0 {
			t.Fatalf("invalid args accepted: code=%d output=%s", code, &out)
		}
	}
	if err := os.WriteFile(frontend, []byte("broken artifact PRIVATE_ASSET_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-descriptor", descriptor, "-frontend", frontend}, &out, &diagnostics); code == 0 || out.Len() != 0 {
		t.Fatalf("corruption accepted: %d %s", code, &out)
	}
	if strings.Contains(diagnostics.String(), "PRIVATE_ASSET_CONTENT") || strings.Contains(diagnostics.String(), frontend) {
		t.Fatal("raw inputs leaked in error")
	}
}

func TestInspectorHelpAndOutputFailure(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-h"}, &out, &diagnostics); code != 0 || diagnostics.Len() == 0 {
		t.Fatalf("help failed: %d", code)
	}
	descriptor, frontend := inspectorFixture(t)
	if code := run([]string{"-descriptor", descriptor, "-frontend", frontend}, inspectorFailedWriter{}, &diagnostics); code == 0 {
		t.Fatal("output failure reported success")
	}
}

type inspectorFailedWriter struct{}

func (inspectorFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestInspectorRejectsSymlinkOversizeAndInvalidDescriptorInputs(t *testing.T) {
	descriptor, frontend := inspectorFixture(t)
	dir := t.TempDir()
	oversized := filepath.Join(dir, "oversized.json")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((128 << 10) + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"private":"NEVER_PRINT_THIS"}`), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "descriptor-link.json")
	if err := os.Symlink(descriptor, linked); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{oversized, invalid, linked} {
		var output, diagnostics bytes.Buffer
		code := run([]string{"-descriptor", input, "-frontend", frontend}, &output, &diagnostics)
		if code == 0 || output.Len() != 0 {
			t.Fatalf("invalid input accepted: %d %s", code, &output)
		}
		if strings.Contains(diagnostics.String(), input) || strings.Contains(diagnostics.String(), "NEVER_PRINT_THIS") {
			t.Fatal("input data leaked")
		}
	}
}
