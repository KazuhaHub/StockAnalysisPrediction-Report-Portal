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

	"golang.org/x/sys/unix"
)

func requireMountAcceptance(t *testing.T) {
	t.Helper()
	if os.Getenv("PORTAL_APP_RUNNER_CONTAINER_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable mount acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("mount acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
}
func nativeMountFixture(t *testing.T) (containerPlan, candidateDataPin, containerCreate) {
	t.Helper()
	requireMountAcceptance(t)
	root, err := os.MkdirTemp("/run", "portal-pins-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	base := containerFixture(t)
	raw, _, ref := managedImageFixture(t)
	image := BackendImageInspection{ReleaseSHA256: base.owner.ReleaseSHA256, Reference: ref, ImageID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Platform: "linux/amd64"}
	plan, err := planCandidateContainer(raw, image, base.owner, containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sock")})
	if err != nil {
		t.Fatal(err)
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil {
		t.Fatal("invalid policy")
	}
	var data candidateDataPin
	for _, mount := range request.HostConfig.Mounts {
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			t.Fatal(err)
		}
		switch mount.Target {
		case "/data":
			var st unix.Stat_t
			if err := unix.Stat(mount.Source, &st); err != nil {
				t.Fatal(err)
			}
			data = candidateDataPin{Device: uint64(st.Dev), Inode: st.Ino}
		case "/run/portal-app/http":
			if err := os.Chown(mount.Source, 65532, 65532); err != nil {
				t.Fatal(err)
			}
		case "/run/portal-app/broker":
			if err := os.Chmod(mount.Source, 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return plan, data, request
}
func TestNativeCandidateMountPins(t *testing.T) {
	plan, data, request := nativeMountFixture(t)
	pins, err := pinCandidateMounts(context.Background(), plan, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := pins.validate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	// Socket contents can change without changing the mounted directory identity.
	httpDir := request.HostConfig.Mounts[1].Source
	if err := os.WriteFile(filepath.Join(httpDir, "fixture"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := pins.validate(context.Background(), plan); err != nil {
		t.Fatal("normal socket directory content change refused", err)
	}
	if err := pins.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pins.validate(context.Background(), plan); err == nil {
		t.Fatal("closed pins survived")
	}
}
func TestNativeCandidateMountPinsRejectSubstitution(t *testing.T) {
	for _, name := range []string{"data inode", "symlink", "parent replacement", "permissions", "owner", "wrong plan"} {
		t.Run(name, func(t *testing.T) {
			plan, data, request := nativeMountFixture(t)
			pins, err := pinCandidateMounts(context.Background(), plan, data)
			if err != nil {
				t.Fatal(err)
			}
			defer pins.Close()
			source := request.HostConfig.Mounts[0].Source
			switch name {
			case "data inode":
				if err := os.Rename(source, source+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(source, source+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source+".old", source); err != nil {
					t.Fatal(err)
				}
			case "parent replacement":
				parent := filepath.Dir(source)
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(source, 0700); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(source, 0777); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if err := os.Chown(source, 65532, 65532); err != nil {
					t.Fatal(err)
				}
			case "wrong plan":
				plan = containerFixture(t)
			}
			if err := pins.validate(context.Background(), plan); err == nil {
				t.Fatal("substituted native directory accepted")
			}
		})
	}
}
func TestNativeCandidateMountPinsRefuseUnsafeInitialState(t *testing.T) {
	for _, name := range []string{"data proof", "http owner", "broker write", "symlink parent", "cancel"} {
		t.Run(name, func(t *testing.T) {
			plan, data, request := nativeMountFixture(t)
			ctx := context.Background()
			switch name {
			case "data proof":
				data.Inode++
			case "http owner":
				if err := os.Chown(request.HostConfig.Mounts[1].Source, 0, 0); err != nil {
					t.Fatal(err)
				}
			case "broker write":
				if err := os.Chmod(request.HostConfig.Mounts[2].Source, 0777); err != nil {
					t.Fatal(err)
				}
			case "symlink parent":
				parent := filepath.Dir(request.HostConfig.Mounts[0].Source)
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+".old", parent); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			pins, err := pinCandidateMounts(ctx, plan, data)
			if err == nil {
				pins.Close()
				t.Fatal("unsafe native mount input accepted")
			}
		})
	}
}

func TestNativeCandidateMountPinsCreateBoundary(t *testing.T) {
	for _, when := range []string{"before", "during"} {
		t.Run(when, func(t *testing.T) {
			plan, data, request := nativeMountFixture(t)
			pins, err := pinCandidateMounts(context.Background(), plan, data)
			if err != nil {
				t.Fatal(err)
			}
			defer pins.Close()
			mutate := func() {
				if err := os.Chmod(request.HostConfig.Mounts[0].Source, 0777); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				mutate()
				return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"Id":"` + strings.Repeat("c", 64) + `","Warnings":[]}`))}, nil
			}))
			if when == "before" {
				mutate()
			}
			id, err := createPinnedCandidateHTTP(context.Background(), client, plan, pins)
			if err == nil || id != "" {
				t.Fatal("changed mount accepted create result", id, err)
			}
			if when == "before" && calls != 0 {
				t.Fatal("unsafe mount reached daemon")
			}
			if when == "during" && (calls != 1 || !errors.Is(err, ErrContainerUncertain)) {
				t.Fatal("post-effect substitution lost uncertainty", calls, err)
			}
		})
	}
}

func TestNativeCandidateMountPinsStartBoundary(t *testing.T) {
	plan, data, request := nativeMountFixture(t)
	pins, err := pinCandidateMounts(context.Background(), plan, data)
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	if err := os.Chmod(request.HostConfig.Mounts[0].Source, 0777); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := daemonClient(transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected daemon access")
	}))
	state, err := startPinnedCandidateHTTP(context.Background(), client, strings.Repeat("c", 64), plan, pins)
	if err == nil || state != "" || calls != 0 {
		t.Fatal("changed mount reached start", state, err, calls)
	}
}
