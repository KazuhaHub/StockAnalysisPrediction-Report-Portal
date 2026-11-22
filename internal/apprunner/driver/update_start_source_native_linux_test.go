//go:build linux && (amd64 || arm64)

package driver

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNativeUpdateStartSourceRemoval runs the removal of the stopped backend an enable start replaces against a real
// Docker daemon. It is skipped unless APP_UPDATE_START_SOURCE_DOCKER_SOCKET names the daemon socket. It needs no XFS
// root: it creates containers from the real planners (never started, except the one running case), so it never mounts
// application data. Everything it creates is named by id, carries a random installation id and is removed by id.
func TestNativeUpdateStartSourceRemoval(t *testing.T) {
	socket := os.Getenv("APP_UPDATE_START_SOURCE_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("a Docker daemon for the update-start source removal is not enabled")
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("the throwaway image is built for amd64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second, Transport: allowListTransport{next: &http.Transport{
		Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}}}

	prefix := "rp-test-installorigin-" + randomHex(t, 4)
	image := buildThrowawayImage(t, prefix)
	raw, digest, reference := managedImageFixture(t)
	base, err := os.MkdirTemp("", "rpio")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	roots := containerRoots{Data: filepath.Join(base, "data"), Sockets: filepath.Join(base, "sockets")}
	inspection := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: image, Platform: "linux/amd64"}
	var created []string
	t.Cleanup(func() {
		for _, id := range created {
			exec.Command("docker", "rm", "-f", "-v", id).Run()
		}
	})

	ownerOf := func(installation, resource string, generation, writer string) containerOwner {
		return containerOwner{DeploymentID: randomUUID(t), ControllerEpoch: randomUUID(t), InstallationID: installation,
			OperationID: randomUUID(t), ResourceID: resource, NamespaceID: randomUUID(t), ReleaseSHA256: digest,
			Generation: generation, ControlFence: "3", WriterFence: writer}
	}
	mkdirs := func(o containerOwner) {
		sockets := filepath.Join(roots.Sockets, o.InstallationID, o.Generation)
		for _, dir := range []string{filepath.Join(roots.Data, o.NamespaceID), filepath.Join(sockets, "http"), filepath.Join(sockets, "broker")} {
			if err := os.MkdirAll(dir, 0o777); err != nil {
				t.Fatal(err)
			}
		}
	}
	writableBinding := WritableCandidateBinding{EffectID: randomUUID(t), ClaimID: randomUUID(t),
		PayloadSHA256: strings.Repeat("1", 64), OwnershipSHA256: strings.Repeat("2", 64),
		DataReceiptSHA256: strings.Repeat("3", 64), WriterReceiptSHA256: strings.Repeat("4", 64)}
	updateBinding := UpdateStartBinding{EffectID: randomUUID(t), ClaimID: randomUUID(t), PayloadSHA256: strings.Repeat("5", 64),
		OwnershipSHA256: strings.Repeat("6", 64), CheckpointReceiptSHA256: strings.Repeat("7", 64), PolicyRevision: "2"}
	create := func(plan containerPlan, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		id, err := createCandidateContainerHTTP(ctx, client, plan)
		if err != nil {
			t.Fatal("create:", err)
		}
		created = append(created, id)
		return id
	}
	exists := func(id string) bool {
		_, err := fetchJSONBody(ctx, client, "/v"+APIVersion+"/containers/"+id+"/json", ErrUnavailable)
		return err == nil
	}
	requestFor := func(o containerOwner, native string, origin UpdateStartSourceOrigin, profile string) UpdateStartLaunchRequest {
		source := &UpdateStartSource{OperationID: o.OperationID, ResourceID: o.ResourceID, NativeID: native, ReleaseSHA256: digest,
			Generation: o.Generation, ControlFence: o.ControlFence, WriterFence: o.WriterFence, Origin: origin, SandboxProfile: profile}
		switch origin {
		case UpdateStartSourceWritable:
			source.Writable = &writableBinding
		case UpdateStartSourceUpdate:
			source.Binding = updateBinding
		}
		return UpdateStartLaunchRequest{
			CandidateLaunchRequest: CandidateLaunchRequest{Descriptor: raw, Identity: CandidateIdentity{
				DeploymentID: o.DeploymentID, ControllerEpoch: o.ControllerEpoch, InstallationID: o.InstallationID,
				OperationID: randomUUID(t), ResourceID: randomUUID(t), NamespaceID: o.NamespaceID, ReleaseSHA256: digest,
				Generation: "9", ControlFence: "9", WriterFence: "9"}},
			ReplaceSource: true, Source: source}
	}
	remove := func(request UpdateStartLaunchRequest) error {
		return removeUpdateStartSourceContainer(ctx, client, request, inspection, roots)
	}

	installation := randomUUID(t)
	candidateOwner := ownerOf(installation, randomUUID(t), "1", "1")
	writableOwner := ownerOf(randomUUID(t), randomUUID(t), "1", "1")
	updateOwner := ownerOf(randomUUID(t), randomUUID(t), "2", "2")
	for _, o := range []containerOwner{candidateOwner, writableOwner, updateOwner} {
		mkdirs(o)
	}

	// An unrelated sibling (another installation) must survive every removal below.
	sibling := ownerOf(randomUUID(t), randomUUID(t), "1", "1")
	mkdirs(sibling)
	siblingID := create(planCandidateContainer(raw, inspection, sibling, roots))

	t.Run("install candidate backend is removed exactly", func(t *testing.T) {
		id := create(planCandidateContainer(raw, inspection, candidateOwner, roots))
		if err := remove(requestFor(candidateOwner, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile)); err != nil {
			t.Fatal(err)
		}
		if exists(id) || !exists(siblingID) {
			t.Fatalf("the source exists=%v or the sibling exists=%v", exists(id), exists(siblingID))
		}
		// Already gone: the removal fails closed. Absence is proven by the start's settlement, not by this call.
		if err := remove(requestFor(candidateOwner, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile)); err == nil {
			t.Fatal("removing an absent source succeeded")
		}
	})
	t.Run("install writable backend is removed exactly", func(t *testing.T) {
		id := create(planWritableCandidateContainer(raw, inspection, writableOwner, roots, writableBinding))
		if err := remove(requestFor(writableOwner, id, UpdateStartSourceWritable, UpdateStartWritableSandboxProfile)); err != nil {
			t.Fatal(err)
		}
		if exists(id) || !exists(siblingID) {
			t.Fatal("the writable source was not removed alone")
		}
	})
	t.Run("update backend is still removed by its update-start plan", func(t *testing.T) {
		id := create(planUpdateStartContainer(raw, inspection, updateOwner, roots, updateBinding, UpdateStartReadOnlySandboxProfile))
		if err := remove(requestFor(updateOwner, id, UpdateStartSourceUpdate, UpdateStartReadOnlySandboxProfile)); err != nil {
			t.Fatal(err)
		}
		if exists(id) || !exists(siblingID) {
			t.Fatal("the update source was not removed alone")
		}
	})
	t.Run("refusals leave the container alone", func(t *testing.T) {
		o := ownerOf(randomUUID(t), randomUUID(t), "1", "1")
		mkdirs(o)
		id := create(planCandidateContainer(raw, inspection, o, roots))
		refuse := func(name string, request UpdateStartLaunchRequest) {
			t.Helper()
			if err := remove(request); err == nil || !exists(id) {
				t.Fatalf("%s: removal err=%v, container exists=%v", name, err, exists(id))
			}
		}
		// Another installation's identity in the request.
		other := o
		other.InstallationID = randomUUID(t)
		refuse("another installation", requestFor(other, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile))
		// Labels of another generation or fence than the container carries.
		later := o
		later.Generation, later.WriterFence = "2", "2"
		refuse("generation and fence", requestFor(later, id, UpdateStartSourceUpdate, UpdateStartReadOnlySandboxProfile))
		wrongFence := o
		wrongFence.ControlFence = "4"
		refuse("control fence", requestFor(wrongFence, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile))
		// A container the update-start planner made, named as an install backend.
		u := ownerOf(randomUUID(t), randomUUID(t), "2", "2")
		mkdirs(u)
		updateID := create(planUpdateStartContainer(raw, inspection, u, roots, updateBinding, UpdateStartReadOnlySandboxProfile))
		if err := remove(requestFor(u, updateID, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile)); err == nil || !exists(updateID) {
			t.Fatalf("an update backend was removed as an install backend: %v", err)
		}
		// A running container, with exactly the plan's labels and policy.
		if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
			t.Skipf("the throwaway container could not be started here: %s", out)
		}
		time.Sleep(time.Second)
		refuse("running", requestFor(o, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile))
		if out, err := exec.Command("docker", "stop", "-t", "1", id).CombinedOutput(); err != nil {
			t.Fatalf("stop: %s", out)
		}
		// Once stopped, the same request removes it: the refusal was the running state alone.
		if err := remove(requestFor(o, id, UpdateStartSourceCandidate, UpdateStartReadOnlySandboxProfile)); err != nil || exists(id) {
			t.Fatalf("a stopped container was not removed: %v", err)
		}
	})
	if !exists(siblingID) {
		t.Fatal("the unrelated sibling was removed")
	}
}

// buildThrowawayImage builds a local image whose default command keeps running, named with the test prefix, and
// removes it when the test ends. It is the only image the test creates and it never pulls.
func buildThrowawayImage(t *testing.T, prefix string) string {
	t.Helper()
	name := prefix + ":img"
	build := exec.Command("docker", "build", "--pull=false", "-q", "-t", name, "-")
	build.Stdin = strings.NewReader("FROM ubuntu:24.04\nCMD [\"sleep\",\"300\"]\n")
	out, err := build.CombinedOutput()
	if err != nil {
		t.Skipf("the throwaway image could not be built here: %s", out)
	}
	t.Cleanup(func() { exec.Command("docker", "rmi", "-f", name).Run() })
	id, err := exec.Command("docker", "image", "inspect", name, "--format", "{{.Id}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(id))
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", b)
}

func randomUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
