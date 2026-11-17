//go:build linux

package transport

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type networkJournalFixture struct{ networkEnrollmentFixture }

func (networkJournalFixture) AdmitControlCommand(context.Context, []byte, []byte) (journal.CommandAdmission, error) {
	return journal.CommandAdmission{}, broker.ErrDenied
}

func TestNetworkUnixPeerAndDisabledEndpoint(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_controller", "wrong_runner", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			raw, sig, body, a, key := networkSessionFixture(t)
			dir, err := os.MkdirTemp("/tmp", "network-transport-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			path := filepath.Join(dir, "control.sock")
			uid := uint32(os.Geteuid())
			controller := uid
			if mode == "wrong_controller" {
				controller++
			}
			store := networkJournalFixture{networkEnrollmentFixture{a}}
			server, err := Listen(path, controller, store)
			if err != nil {
				t.Fatal(err)
			}
			provider, _ := newNetworkProvider(store, 2, 2)
			execution := &networkExecutionFixture{current: provider.current}
			provider.pool = execution
			if mode != "disabled" {
				server.network = provider
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); server.Serve(ctx) }()
			defer func() { cancel(); <-done }()
			owner := uid
			if mode == "wrong_runner" {
				owner++
			}
			conn, err := connectEndpoint(ctx, path, privateProfile(owner))
			if mode == "wrong_runner" {
				if err == nil {
					conn.Close()
					t.Fatal("wrong endpoint owner accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var approvals atomic.Int32
			got, err := exchangeNetworkSession(ctx, conn, raw, sig, body, a, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
				approvals.Add(1)
				return control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
			})
			if mode == "valid" {
				if err != nil || got.Status != 201 || execution.effects.Load() != 1 || approvals.Load() != 4 {
					t.Fatalf("%+v %v effects=%d approvals=%d", got, err, execution.effects.Load(), approvals.Load())
				}
			} else if err == nil || execution.effects.Load() != 0 || approvals.Load() != 0 {
				t.Fatal("unauthorized endpoint read/dispatched request", err)
			}
		})
	}
}

func TestNetworkRootBootstrapRefusesInvalidConfiguration(t *testing.T) {
	if _, err := ListenNetworkController(context.Background(), "/tmp/not-created-network.sock", 1, 1, nil, 1, 1); err == nil {
		t.Fatal("missing enrolled store accepted")
	}
	if _, err := RequestControllerNetwork(context.Background(), "/tmp/not-created-network.sock", 0, 0, nil, nil, nil, control.Authority{}, nil); err == nil {
		t.Fatal("invalid controller accepted")
	}
}
