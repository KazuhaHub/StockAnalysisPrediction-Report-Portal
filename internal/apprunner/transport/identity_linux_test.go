//go:build linux

package transport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerProfileRejectsInvalidIdentity(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "runner-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, identity := range [][2]uint32{{0, 41001}, {41001, 0}, {^uint32(0), 41001}, {41001, ^uint32(0)}} {
		if server, err := ListenController(filepath.Join(dir, "control.sock"), identity[0], identity[1], &recorder{}); err == nil {
			server.Close()
			t.Fatal("invalid controller identity admitted")
		}
		if err := ExchangeController(context.Background(), filepath.Join(dir, "control.sock"), identity[0], identity[1], []byte("{}"), make([]byte, 64)); err == nil {
			t.Fatal("invalid client identity admitted")
		}
	}
}
