package driver

import (
	"strings"
	"testing"
)

// The backend an enable start replaces is the install's own when the installation's latest lifecycle operation is its
// install: generation 1, writer fence 1, no update-start binding. It is accepted as such and as nothing else.
func TestUpdateStartSourceAcceptsTheInstallBackendByOrigin(t *testing.T) {
	const (
		operation = "70000000-0000-4000-8000-000000000201"
		resource  = "70000000-0000-4000-8000-000000000202"
		target    = "70000000-0000-4000-8000-000000000203"
	)
	release := strings.Repeat("e", 64)
	request := UpdateStartLaunchRequest{CandidateLaunchRequest: CandidateLaunchRequest{Identity: CandidateIdentity{
		ResourceID: target, ReleaseSHA256: release, Generation: "2", WriterFence: "3"}}}
	candidate := UpdateStartSource{
		OperationID: operation, ResourceID: resource, NativeID: strings.Repeat("a", 64), ReleaseSHA256: release,
		Generation: "1", ControlFence: "1", WriterFence: "1", Origin: UpdateStartSourceCandidate,
		SandboxProfile: UpdateStartReadOnlySandboxProfile,
	}
	if !validUpdateStartSource(candidate, request) {
		t.Fatal("the install's read-only backend was refused as a source")
	}
	writable := candidate
	writable.Origin, writable.SandboxProfile = UpdateStartSourceWritable, UpdateStartWritableSandboxProfile
	if validUpdateStartSource(writable, request) {
		t.Fatal("a writable install backend without its binding was accepted")
	}
	writable.Writable = &WritableCandidateBinding{
		EffectID: "70000000-0000-4000-8000-000000000204", ClaimID: "70000000-0000-4000-8000-000000000205",
		PayloadSHA256: strings.Repeat("1", 64), OwnershipSHA256: strings.Repeat("2", 64),
		DataReceiptSHA256: strings.Repeat("3", 64), WriterReceiptSHA256: strings.Repeat("4", 64),
	}
	if !validUpdateStartSource(writable, request) {
		t.Fatal("the install's writable backend was refused as a source")
	}
	later := writable
	later.Generation, later.WriterFence = "2", "2"
	if validUpdateStartSource(later, request) {
		t.Fatal("an install-origin source above generation 1 was accepted")
	}
	update := candidate
	update.Origin, update.Generation, update.WriterFence = UpdateStartSourceUpdate, "2", "2"
	if validUpdateStartSource(update, request) {
		t.Fatal("an update-origin source without its binding was accepted")
	}
	update.Binding = updateStartBindingFixture()
	if !validUpdateStartSource(update, request) {
		t.Fatal("an update-origin source with its binding was refused")
	}
}

// The source's plan is built from the journal's fields, not from the install's request. For it to name the install's
// container, the plan the install planners made from the install's own identity must be reproduced exactly by the
// plan of the source built from those fields: the same container name, the same labels, the same create body.
func TestUpdateStartSourcePlanReproducesTheInstallContainer(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	id := func(n string) string { return n + "0000000-0000-4000-8000-000000000000" }
	installed := CandidateIdentity{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"),
		ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: digest, Generation: "1", ControlFence: "2", WriterFence: "1"}
	binding := WritableCandidateBinding{EffectID: id("7"), ClaimID: id("8"), PayloadSHA256: strings.Repeat("1", 64),
		OwnershipSHA256: strings.Repeat("2", 64), DataReceiptSHA256: strings.Repeat("3", 64), WriterReceiptSHA256: strings.Repeat("4", 64)}
	// The start the enable replaces the backend of runs in another operation of the same installation and namespace.
	target := CandidateIdentity{DeploymentID: installed.DeploymentID, ControllerEpoch: installed.ControllerEpoch, InstallationID: installed.InstallationID,
		OperationID: id("a"), ResourceID: id("b"), NamespaceID: installed.NamespaceID, ReleaseSHA256: digest, Generation: "2", ControlFence: "5", WriterFence: "3"}
	request := func(origin UpdateStartSourceOrigin) UpdateStartLaunchRequest {
		source := &UpdateStartSource{OperationID: installed.OperationID, ResourceID: installed.ResourceID, NativeID: strings.Repeat("a", 64),
			ReleaseSHA256: digest, Generation: installed.Generation, ControlFence: installed.ControlFence, WriterFence: installed.WriterFence, Origin: origin}
		if origin == UpdateStartSourceWritable {
			source.Writable = &binding
		}
		return UpdateStartLaunchRequest{CandidateLaunchRequest: CandidateLaunchRequest{Descriptor: raw, Identity: target}, ReplaceSource: true, Source: source}
	}
	same := func(name string, want containerPlan, err error, got containerPlan, gotErr error) {
		t.Helper()
		if err != nil || gotErr != nil {
			t.Fatalf("%s: %v %v", name, err, gotErr)
		}
		if got.name != want.name || got.bodySHA != want.bodySHA || string(got.body) != string(want.body) {
			t.Fatalf("%s: the source plan is not the install container's: %s vs %s", name, got.name, want.name)
		}
		if got.name != "portal-app-"+installed.ResourceID {
			t.Fatalf("%s: unexpected container name %s", name, got.name)
		}
	}
	candidate, err := planCandidateContainer(raw, image, candidateOwner(installed), roots)
	got, gotErr := planUpdateStartSource(request(UpdateStartSourceCandidate), image, roots)
	same("candidate", candidate, err, got, gotErr)
	writable, err := planWritableCandidateContainer(raw, image, candidateOwner(installed), roots, binding)
	got, gotErr = planUpdateStartSource(request(UpdateStartSourceWritable), image, roots)
	same("writable", writable, err, got, gotErr)
	if candidate.bodySHA == writable.bodySHA {
		t.Fatal("the candidate and writable plans are the same container")
	}
	// A source built with another generation or fence is another container.
	later := request(UpdateStartSourceCandidate)
	later.Source.Generation, later.Source.WriterFence = "2", "2"
	if other, otherErr := planUpdateStartSource(later, image, roots); otherErr != nil || other.bodySHA == candidate.bodySHA {
		t.Fatal("a source of another generation planned the install's container", otherErr)
	}
}
