//go:build linux

package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNetworkRuntimeUnixAvailabilityAndRefusal(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "wrong_peer"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _, a, _ := networkSessionFixture(t)
			store := networkJournalFixture{networkEnrollmentFixture{a}}
			root, err := os.MkdirTemp("/tmp", "network-profile-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			uid := uint32(os.Geteuid())
			allowed := uid
			if mode == "wrong_peer" {
				allowed++
			}
			path := filepath.Join(root, "control.sock")
			server, err := Listen(path, allowed, store)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "disabled" {
				server.network, _ = newNetworkProvider(store, 2, 3)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			before, _ := store.ControllerDeployment(ctx)
			observation, err := observeNetworkRuntime(ctx, path, privateProfile(uid))
			after, _ := store.ControllerDeployment(ctx)
			if mode == "enabled" {
				if err != nil || observation.Profile().ActiveRequests != 2 || observation.Profile().CacheEntries != 3 || observation.Profile().WritableBackend || observation.Controller().Fingerprint != a.Fingerprint || observation.ReceivedAt().IsZero() || observation.Endpoint().Path != path {
					t.Fatalf("%+v %v", observation.Profile(), err)
				}
			} else if !errors.Is(err, ErrNetworkRuntimeUnavailable) || !observation.ReceivedAt().IsZero() {
				t.Fatal("unsupported endpoint admitted", err)
			}
			if before.Revision != after.Revision || len(server.networkSessionsForTest()) != 0 {
				t.Fatal("read-only probe mutated state")
			}
		})
	}
	if _, err := ObserveControllerNetwork(context.Background(), "/tmp/not-created-profile.sock", 0, 0); err == nil {
		t.Fatal("invalid controller")
	}
	if _, err := ListenNetworkCandidateController(context.Background(), "/tmp/not-created-profile.sock", 1, 1, nil, nil, 1, 1); err == nil {
		t.Fatal("missing native/enrollment accepted")
	}
}
func (s *Server) networkSessionsForTest() []string {
	if s.network == nil {
		return nil
	}
	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	out := []string{}
	for key := range s.network.sessions {
		out = append(out, key)
	}
	return out
}
