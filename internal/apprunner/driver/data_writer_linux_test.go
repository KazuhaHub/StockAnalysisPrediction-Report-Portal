//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataWriterReferenceInventoryRefusesScopeAndUnknownMounts(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "namespace")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{target, root, filepath.Join(target, "child"), alias, filepath.Join(root, "missing")} {
		t.Run(filepath.Base(source), func(t *testing.T) {
			raw, _ := json.Marshal([]map[string]any{{"Id": strings.Repeat("a", 64), "Mounts": []map[string]any{{"Type": "bind", "Source": source}}, "State": "exited"}})
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/v"+APIVersion+"/containers/json" || r.URL.Query().Get("all") != "1" {
					t.Fatal("inventory omitted stopped containers")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			}))
			err := requireNoContainerMountReferences(context.Background(), client, target)
			if err == nil {
				t.Fatal("referenced/unresolved data admitted")
			}
			if source != filepath.Join(root, "missing") && !errors.Is(err, ErrContainerConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestDataWriterOpaqueEvidenceCannotBeDecoded(t *testing.T) {
	var observation DataWriterObservation
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("zero native writer proof accepted")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &observation); err == nil {
		t.Fatal("JSON constructed writer authority")
	}
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("failed decoding retained writer authority")
	}
}

func TestDataWriterRecoveryOpaqueEvidenceCannotBeDecoded(t *testing.T) {
	var observation DataWriterRecoveryObservation
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("zero recovery proof accepted")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &observation); err == nil {
		t.Fatal("decoded recovery authority")
	}
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("failed decoding retained recovery authority")
	}
}
