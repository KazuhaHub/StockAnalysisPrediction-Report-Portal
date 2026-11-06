//go:build linux

package transport

import (
	"context"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func TestReadOnlyEndpointCannotDispatchData(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	authority, _, raw, sig := receiptAuthority(t)
	proof, err := readDataResponse(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, authority, true)
	if err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("read-only listener dispatched/minted receipt")
	}
	if backend.calls.Load() != 0 {
		t.Fatal("execution request became generic admission")
	}
}

func TestDataControllerNeedsConcreteConfiguredProvider(t *testing.T) {
	path := t.TempDir() + "/control.sock"
	server, err := ListenDataController(context.Background(), path, 41001, 41001, nil, nil, data.RootIdentity{})
	if err == nil {
		server.Close()
		t.Fatal("unconfigured provider exposed endpoint")
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("failed configuration created socket", err)
	}
}
