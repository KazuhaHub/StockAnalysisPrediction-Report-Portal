package data

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestBounds(t *testing.T) {
	r := Request{"00000000-0000-0000-0000-000000000001", 1, 8 << 20, 128}
	if !r.valid() {
		t.Fatal("valid reservation refused")
	}
	for _, bad := range []Request{{"../other", 1, 512, 1}, {r.NamespaceID, 0, 512, 1}, {r.NamespaceID, 1, 513, 1}, {r.NamespaceID, 1, 0, 1}, {r.NamespaceID, 1, 512, 0}} {
		if bad.valid() {
			t.Fatal("unsafe reservation accepted", bad)
		}
	}
}

func TestRootIdentityBounds(t *testing.T) {
	good := RootIdentity{FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", Inode: 1}
	if !good.valid() {
		t.Fatal("valid root identity refused")
	}
	for _, bad := range []RootIdentity{
		{}, {FilesystemUUID: good.FilesystemUUID}, {FilesystemUUID: "00000000-0000-0000-0000-000000000000", Inode: 1},
		{FilesystemUUID: "01234567-89AB-cdef-0123-456789abcdef", Inode: 1},
		{FilesystemUUID: "../root", Inode: 1},
	} {
		if bad.valid() {
			t.Fatal("invalid physical root identity accepted", bad)
		}
	}
}

func TestCheckpointRequestBounds(t *testing.T) {
	good := CheckpointRequest{
		CheckpointID: "00000000-0000-0000-0000-000000000001", NamespaceID: "00000000-0000-0000-0000-000000000002", ReleaseSHA256: strings.Repeat("a", 64),
		ProjectID: 1, DataFormat: 1, MaxArchiveBytes: 1 << 20, ExpectedDevice: 1, ExpectedNamespaceInode: 2, WriterFence: 1,
	}
	if !good.valid() || !sha256Value(good.ReleaseSHA256) {
		t.Fatal("valid checkpoint request refused")
	}
	for name, mutate := range map[string]func(*CheckpointRequest){
		"checkpoint": func(r *CheckpointRequest) { r.CheckpointID = "" },
		"namespace":  func(r *CheckpointRequest) { r.NamespaceID = "" },
		"project":    func(r *CheckpointRequest) { r.ProjectID = 0 },
		"format":     func(r *CheckpointRequest) { r.DataFormat = 0 },
		"size":       func(r *CheckpointRequest) { r.MaxArchiveBytes = 0 },
		"device":     func(r *CheckpointRequest) { r.ExpectedDevice = 0 },
		"inode":      func(r *CheckpointRequest) { r.ExpectedNamespaceInode = 0 },
		"fence":      func(r *CheckpointRequest) { r.WriterFence = 0 },
		"digest":     func(r *CheckpointRequest) { r.ReleaseSHA256 = strings.Repeat("g", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := good
			mutate(&changed)
			if changed.valid() {
				t.Fatal("malformed checkpoint request accepted")
			}
		})
	}
}

func TestCheckpointObservationIsOpaque(t *testing.T) {
	var observation CheckpointObservation
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("zero checkpoint observation accepted")
	}
	if _, err := json.Marshal(observation); err == nil {
		t.Fatal("checkpoint observation serialized")
	}
	if err := json.Unmarshal([]byte(`{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}`), &observation); err == nil {
		t.Fatal("checkpoint observation decoded")
	}
}
