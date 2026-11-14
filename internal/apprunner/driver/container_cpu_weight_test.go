package driver

import (
	"encoding/json"
	"strings"
	"testing"
)

// assertAppCPUWeight decodes a create body and requires the low scheduling weight next to the
// unchanged hard limits declared by the release.
func assertAppCPUWeight(t *testing.T, name string, body []byte) {
	t.Helper()
	var request containerCreate
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(name, err)
	}
	h := request.HostConfig
	if h.CpuShares != 256 {
		t.Fatalf("%s: CpuShares = %d, want 256", name, h.CpuShares)
	}
	if h.NanoCpus != 500000000 || h.Memory != 256<<20 || h.PidsLimit != 64 {
		t.Fatalf("%s: hard limits changed: nano=%d mem=%d pids=%d", name, h.NanoCpus, h.Memory, h.PidsLimit)
	}
	var raw struct{ HostConfig map[string]any }
	if err := json.Unmarshal(body, &raw); err != nil || raw.HostConfig["CpuShares"] != float64(256) {
		t.Fatal(name, "CpuShares is not on the wire under its Docker name", err)
	}
}

func TestEveryAppContainerPlanCarriesLowCPUWeight(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}

	candidate := containerFixture(t)
	assertAppCPUWeight(t, "candidate", candidate.body)

	owner := candidate.owner
	owner.WriterFence = "1"
	writable, err := planWritableCandidateContainer(raw, image, owner, roots, writableBindingFixture())
	if err != nil {
		t.Fatal(err)
	}
	assertAppCPUWeight(t, "writable candidate", writable.body)

	owner.Generation, owner.WriterFence = "2", "2"
	for _, profile := range []string{UpdateStartWritableSandboxProfile, UpdateStartReadOnlySandboxProfile} {
		update, err := planUpdateStartContainer(raw, image, owner, roots, updateStartBindingFixture(), profile)
		if err != nil {
			t.Fatal(err)
		}
		assertAppCPUWeight(t, "update start "+profile, update.body)
	}

	migration, _, _ := migrationFixture(t)
	assertAppCPUWeight(t, "migration", migration.body)
}

// A plan whose weight differs from the reviewed constant is refused even when its policy-digest label is
// recomputed to match, so the check does not rest on the digest alone. The plan carries no descriptor, so
// the hard limits stay bound to it only through the builder and the digest.
func TestContainerPlanRefusesAnotherCPUWeightEvenWithMatchingDigest(t *testing.T) {
	plan := containerFixture(t)
	for name, mutate := range map[string]func(*containerHostConfig){
		"unset weight":   func(h *containerHostConfig) { h.CpuShares = 0 },
		"minimum weight": func(h *containerHostConfig) { h.CpuShares = 2 },
		"default weight": func(h *containerHostConfig) { h.CpuShares = 1024 },
	} {
		var request containerCreate
		if err := json.Unmarshal(plan.body, &request); err != nil {
			t.Fatal(err)
		}
		mutate(&request.HostConfig)
		delete(request.Labels, containerLabelPrefix+"policy-sha256")
		policy, _ := json.Marshal(request)
		request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
		body, _ := json.Marshal(request)
		changed := plan
		changed.body, changed.bodySHA = body, containerHash(body)
		if validContainerPlan(changed) {
			t.Fatalf("%s: plan with a consistent digest was accepted", name)
		}
	}
}

func TestContainerHostConfigProjectionKeepsStrictKeys(t *testing.T) {
	if !validProjection([]byte(`{"CpuShares":256,"NanoCpus":500000000}`), &containerHostConfig{}) {
		t.Fatal("CpuShares is not an accepted host config key")
	}
	// A case variant or a duplicate of the new key is as refused as one of any other known key.
	for _, body := range []string{`{"cpushares":256}`, `{"CpuShares":256,"CpuShares":2}`} {
		if validProjection([]byte(body), &containerHostConfig{}) {
			t.Fatal("ambiguous CpuShares key accepted", body)
		}
	}
	// The forbidden-options projection is unchanged: an inspected container carrying one is still a
	// conflict, and the weight does not open any of them.
	plan := containerFixture(t)
	var request containerCreate
	if err := json.Unmarshal(plan.body, &request); err != nil || request.HostConfig.CpuShares != 256 {
		t.Fatal("fixture weight", err)
	}
	for _, key := range []string{"CapAdd", "Binds", "Devices", "Sysctls", "CgroupParent", "Runtime"} {
		var forbidden containerForbiddenOptions
		if !validProjection([]byte(`{"`+key+`":null}`), &forbidden) {
			t.Fatal("forbidden option projection rejects its own key", key)
		}
		if validProjection([]byte(`{"`+strings.ToLower(key)+`":null}`), &forbidden) {
			t.Fatal("case-variant forbidden key accepted", key)
		}
	}
}
