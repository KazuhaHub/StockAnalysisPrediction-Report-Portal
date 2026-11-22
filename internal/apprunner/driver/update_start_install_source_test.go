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
