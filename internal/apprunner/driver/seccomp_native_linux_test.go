//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeccompV1ContainerAgainstARealDaemon creates a container from the v1 plan on a real Docker daemon (the test
// posts the body itself, because the runner's own create path refuses a historical plan), then checks that the
// runner's inspection accepts it under the plan selected by its recorded digest and refuses it under the current
// plan, that the production gate recognises the real `docker inspect` output, and that removal works. The
// container is created and never started, and is removed at the end.
//
//	APP_SECCOMP_PYTHON_IMAGE=python:3.11-slim-bookworm go test ./internal/apprunner/driver -run SeccompV1ContainerAgainstARealDaemon -count=1 -v
//
// The image must already be present locally. Nothing is published.
func TestSeccompV1ContainerAgainstARealDaemon(t *testing.T) {
	reference := os.Getenv("APP_SECCOMP_PYTHON_IMAGE")
	if reference == "" {
		t.Skip("set APP_SECCOMP_PYTHON_IMAGE to a local image to run this check")
	}
	idOut, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", reference).Output()
	if err != nil {
		t.Fatal("the image is not present locally:", err)
	}
	imageID := strings.TrimSpace(string(idOut))

	// Short, because socket paths under the sockets root are limited to 107 bytes.
	root, err := os.MkdirTemp("/tmp", "sg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	raw, digest, descriptorReference := managedImageFixture(t)
	fixture := containerFixture(t)
	owner := fixture.owner
	owner.ReleaseSHA256 = digest
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: descriptorReference, ImageID: imageID, Platform: "linux/amd64"}
	roots := containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sockets")}
	current, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	// The image is not the fixture's, so the plan body names the local image id and the checks run against it.
	v1 := withSeccomp(current, v1Policy(t))
	for _, mount := range []string{
		filepath.Join(roots.Data, owner.NamespaceID),
		filepath.Join(roots.Sockets, owner.InstallationID, owner.Generation, "http"),
		filepath.Join(roots.Sockets, owner.InstallationID, owner.Generation, "broker"),
	} {
		if err := os.MkdirAll(mount, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	defer transport.CloseIdleConnections()
	client := daemonClient(transport)

	create, err := http.NewRequest(http.MethodPost, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/create?"+url.Values{"name": []string{v1.name}}.Encode(), bytes.NewReader(v1.body))
	if err != nil {
		t.Fatal(err)
	}
	create.Header.Set("Content-Type", "application/json")
	response, err := client.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	created, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("the daemon refused the v1 create (%d): %s", response.StatusCode, created)
	}
	var nativeID string
	if i := bytes.Index(created, []byte(`"Id":"`)); i >= 0 {
		nativeID = string(created[i+6 : i+6+64])
	}
	if !containerSHA.MatchString(nativeID) {
		t.Fatal("no container id in", string(created))
	}
	removed := false
	t.Cleanup(func() {
		if !removed {
			_ = exec.Command("docker", "rm", "-f", nativeID).Run()
		}
	})

	if state, err := inspectCandidateContainerHTTP(context.Background(), client, nativeID, v1); err != nil || state != "created" {
		t.Fatalf("a real v1 container is not accepted under its recorded plan: %v %q", err, state)
	}
	if _, err := inspectCandidateContainerHTTP(context.Background(), client, nativeID, current); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a real v1 container was adopted by the current plan: %v", err)
	}
	if got := selectRecordedPlan(current, v1.bodySHA); got.bodySHA != v1.bodySHA {
		t.Fatal("the recorded digest did not select the v1 plan")
	}

	inspected, err := exec.Command("docker", "inspect", nativeID).Output()
	if err != nil {
		t.Fatal(err)
	}
	report := InspectSeccompGate(inspected)
	if !report.RecognisedAsV1 || !report.PolicyDigestReproduced || report.PolicySHA256AfterRepair != current.labelPolicySHA256() {
		t.Fatalf("the gate did not recognise the real container: %+v", report)
	}

	if err := removeCandidateContainerHTTP(context.Background(), client, nativeID, v1); err != nil {
		t.Fatalf("removing a real v1 container: %v", err)
	}
	removed = true
}
