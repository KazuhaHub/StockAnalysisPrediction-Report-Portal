package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const testAppCPUs = "1-3"

// cpusetPlans builds one plan for every way the runner creates an app container, all from the same roots.
func cpusetPlans(t *testing.T, appCPUs string) map[string]containerPlan {
	t.Helper()
	raw, digest, reference := managedImageFixture(t)
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps", AppCPUs: appCPUs}
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	owner := containerOwner{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"), ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: digest, Generation: "1", ControlFence: "2", WriterFence: "3"}
	plans := map[string]containerPlan{}
	candidate, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	plans["candidate"] = candidate

	owner.WriterFence = "1"
	writable, err := planWritableCandidateContainer(raw, image, owner, roots, writableBindingFixture())
	if err != nil {
		t.Fatal(err)
	}
	plans["writable candidate"] = writable

	owner.Generation, owner.WriterFence = "2", "2"
	for _, profile := range []string{UpdateStartWritableSandboxProfile, UpdateStartReadOnlySandboxProfile} {
		update, err := planUpdateStartContainer(raw, image, owner, roots, updateStartBindingFixture(), profile)
		if err != nil {
			t.Fatal(err)
		}
		plans["update start "+profile] = update
	}

	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	data := d["data"].(map[string]any)
	data["schema"], data["readable_schemas"] = 2, []int{2}
	data["migrations"] = []any{map[string]any{"from": 1, "to": 2, "entrypoint": []string{"/app/migrate", "--apply"}, "wall_clock_seconds": 60}}
	migrated, _ := json.Marshal(d)
	parsed, err := appplatform.ParseDescriptor(migrated)
	if err != nil {
		t.Fatal(err)
	}
	owner.ReleaseSHA256, owner.WriterFence = parsed.SHA256, "6"
	image.ReleaseSHA256, image.Reference = parsed.SHA256, parsed.Manifest.Backend.Image
	binding := MigrationBinding{EffectID: id("7"), ClaimID: id("8"), PayloadSHA256: strings.Repeat("a", 64), OwnershipSHA256: strings.Repeat("b", 64), CheckpointReceiptSHA256: strings.Repeat("c", 64)}
	migration, err := planMigrationContainer(migrated, image, owner, roots, binding, parsed.Manifest.Data.Migrations[0])
	if err != nil {
		t.Fatal(err)
	}
	plans["migration"] = migration
	return plans
}

func hostConfigOnTheWire(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var raw struct{ HostConfig map[string]any }
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	return raw.HostConfig
}

func TestEveryAppContainerPlanCarriesTheAppCPUSet(t *testing.T) {
	plans := cpusetPlans(t, testAppCPUs)
	if len(plans) != 5 {
		t.Fatalf("expected five create paths, got %d", len(plans))
	}
	for name, plan := range plans {
		var request containerCreate
		if err := json.Unmarshal(plan.body, &request); err != nil || request.HostConfig.CpusetCpus != testAppCPUs {
			t.Fatalf("%s: CpusetCpus = %q, want %q (%v)", name, request.HostConfig.CpusetCpus, testAppCPUs, err)
		}
		// The key is Docker's exact name, next to the unchanged hard limits.
		if got := hostConfigOnTheWire(t, plan.body)["CpusetCpus"]; got != testAppCPUs {
			t.Fatalf("%s: wire CpusetCpus = %v", name, got)
		}
		if request.HostConfig.NanoCpus != 500000000 || request.HostConfig.CpuShares != appContainerCPUShares {
			t.Fatalf("%s: other CPU limits changed: %+v", name, request.HostConfig)
		}
		if !validContainerPlan(plan) {
			t.Fatalf("%s: a plan from the builder is refused", name)
		}
	}
}

func TestContainerPlanRefusesAnotherCPUSetEvenWithMatchingDigest(t *testing.T) {
	for name, plan := range cpusetPlans(t, testAppCPUs) {
		for variant, set := range map[string]string{"unset": "", "includes the reserved CPU": "0-3", "subset": "1-2", "one CPU": "1", "other CPUs": "2-3", "not canonical": "1,2,3"} {
			var request containerCreate
			if err := json.Unmarshal(plan.body, &request); err != nil {
				t.Fatal(err)
			}
			request.HostConfig.CpusetCpus = set
			delete(request.Labels, containerLabelPrefix+"policy-sha256")
			policy, _ := json.Marshal(request)
			request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
			body, _ := json.Marshal(request)
			changed := plan
			changed.body, changed.bodySHA = body, containerHash(body)
			if validContainerPlan(changed) {
				t.Fatalf("%s: a plan with CpusetCpus %q (%s) and a consistent digest was accepted", name, set, variant)
			}
		}
	}
}

func TestContainerPlanRefusesACPUSetItWasNotBuiltFor(t *testing.T) {
	// The configured set travels with the plan; a body that pins CPUs the plan does not name is not the plan.
	plan := cpusetPlans(t, testAppCPUs)["candidate"]
	plan.cpuset = "1-2"
	if validContainerPlan(plan) {
		t.Fatal("a plan whose body disagrees with its configured CPU set was accepted")
	}
	plan.cpuset = ""
	if validContainerPlan(plan) {
		t.Fatal("a pinned body was accepted for a plan configured with no CPU set")
	}
	plan.cpuset = "not a list"
	if validContainerPlan(plan) {
		t.Fatal("a malformed configured CPU set was accepted")
	}
}

func TestNoReservedCPUsLeavesTheCPUSetUnset(t *testing.T) {
	for name, plan := range cpusetPlans(t, "") {
		if _, present := hostConfigOnTheWire(t, plan.body)["CpusetCpus"]; present {
			t.Fatalf("%s: CpusetCpus is on the wire with nothing reserved", name)
		}
		if plan.cpuset != "" || !validContainerPlan(plan) {
			t.Fatalf("%s: plan with nothing reserved is not valid or carries a set", name)
		}
		// Docker's empty echo of an unset cpuset is the same plan; a pinned body is not.
		var request containerCreate
		if err := json.Unmarshal(plan.body, &request); err != nil || request.HostConfig.CpusetCpus != "" {
			t.Fatalf("%s: %q %v", name, request.HostConfig.CpusetCpus, err)
		}
	}
}

func TestPlanRefusesAMalformedAppCPUSet(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	owner := containerOwner{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"), ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: digest, Generation: "1", ControlFence: "2", WriterFence: "3"}
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	for _, set := range []string{"x", "3-1", "1,,2", "-1", "1,2,3", "1-3\n", " 1"} {
		roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps", AppCPUs: set}
		if _, err := planCandidateContainer(raw, image, owner, roots); err == nil {
			t.Fatalf("CPU set %q planned", set)
		}
	}
}

func TestContainerHostConfigProjectionKnowsCpusetCpus(t *testing.T) {
	if !validProjection([]byte(`{"CpusetCpus":"1-3","CpuShares":256}`), &containerHostConfig{}) {
		t.Fatal("CpusetCpus is not an accepted host config key")
	}
	for _, body := range []string{`{"cpusetcpus":"1-3"}`, `{"CpusetCpus":"1-3","CpusetCpus":"0"}`} {
		if validProjection([]byte(body), &containerHostConfig{}) {
			t.Fatal("ambiguous CpusetCpus key accepted", body)
		}
	}
}

func inspectWith(t *testing.T, plan containerPlan, mutate func(map[string]any)) error {
	t.Helper()
	v := containerInspectFixture(t, plan)
	mutate(v["HostConfig"].(map[string]any))
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(v)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
	_, err := inspectCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
	return err
}

func TestInspectionComparesCpusetCpusExactly(t *testing.T) {
	pinned := cpusetPlans(t, testAppCPUs)["candidate"]
	// Docker echoes the string it was given.
	if err := inspectWith(t, pinned, func(h map[string]any) {}); err != nil {
		t.Fatalf("an exact echo of the app set was refused: %v", err)
	}
	for name, set := range map[string]any{"unset": "", "reserved CPU included": "0-3", "other set": "2-3", "equivalent spelling": "1,2,3"} {
		if err := inspectWith(t, pinned, func(h map[string]any) { h["CpusetCpus"] = set }); !errors.Is(err, ErrContainerConflict) {
			t.Fatalf("%s: a container pinned to %q was adopted: %v", name, set, err)
		}
	}
	// With nothing reserved, Docker echoes an empty string and a pinned container is foreign.
	open := cpusetPlans(t, "")["candidate"]
	if err := inspectWith(t, open, func(h map[string]any) { h["CpusetCpus"] = "" }); err != nil {
		t.Fatalf("Docker's empty echo refused: %v", err)
	}
	if err := inspectWith(t, open, func(h map[string]any) { h["CpusetCpus"] = "1-3" }); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a pinned container was adopted for an unpinned plan: %v", err)
	}
}

func TestAppCPUSetRequiresDaemonSupport(t *testing.T) {
	supported := Report{CPUSetSupported: true}
	if err := checkAppCPUSet(supported, testAppCPUs); err != nil {
		t.Fatal(err)
	}
	if err := checkAppCPUSet(Report{}, testAppCPUs); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a daemon without cpuset support was accepted: %v", err)
	}
	for _, set := range []string{"x", "3-1", "1,2,3"} {
		if err := checkAppCPUSet(supported, set); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("malformed set %q accepted: %v", set, err)
		}
	}
	// Nothing reserved asks nothing of the daemon.
	if err := checkAppCPUSet(Report{}, ""); err != nil {
		t.Fatal(err)
	}
	var options runtimeOptions
	WithAppCPUSet(testAppCPUs)(&options)
	if options.appCPUs != testAppCPUs {
		t.Fatal("WithAppCPUSet does not configure the runtime")
	}
}

func inspectLiveAgainst(t *testing.T, live, planned containerPlan, mutate func(map[string]any)) (int, error) {
	t.Helper()
	v := containerInspectFixture(t, live)
	if mutate != nil {
		mutate(v)
	}
	calls := 0
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" {
			t.Fatal("a cpuset conflict caused a mutation", r.Method)
		}
		raw, _ := json.Marshal(v)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
	err := removeCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), planned)
	return calls, err
}

func TestInspectionNamesACPUSetChangeAndNeverAdoptsIt(t *testing.T) {
	for name, tc := range map[string]struct{ live, planned string }{
		"host grew":         {"1-3", "1-7"},
		"host shrank":       {"1-7", "1-3"},
		"reservation added": {"", "1-3"},
		"reservation gone":  {"1-3", ""},
	} {
		live := cpusetPlans(t, tc.live)["candidate"]
		planned := cpusetPlans(t, tc.planned)["candidate"]
		calls, err := inspectLiveAgainst(t, live, planned, nil)
		var changed *CPUSetChangedError
		if !errors.Is(err, ErrContainerConflict) || !errors.Is(err, ErrContainerCPUSetChanged) || !errors.As(err, &changed) ||
			changed.Live != tc.live || changed.Planned != tc.planned || !strings.Contains(err.Error(), CPUSetChangedReason) || calls != 1 {
			t.Fatalf("%s: err = %v (calls %d)", name, err, calls)
		}
	}
	// A container that differs in anything besides the cpuset, and the policy digest that carries it, is a plain
	// conflict: naming the cpuset would hide the real cause.
	live, planned := cpusetPlans(t, "1-3")["candidate"], cpusetPlans(t, "1-7")["candidate"]
	_, err := inspectLiveAgainst(t, live, planned, func(v map[string]any) { v["HostConfig"].(map[string]any)["NetworkMode"] = "host" })
	if !errors.Is(err, ErrContainerConflict) || errors.Is(err, ErrContainerCPUSetChanged) {
		t.Fatalf("a foreign network was reported as a cpuset change: %v", err)
	}
	_, err = inspectLiveAgainst(t, live, planned, func(v map[string]any) {
		v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"generation"] = "99"
	})
	if !errors.Is(err, ErrContainerConflict) || errors.Is(err, ErrContainerCPUSetChanged) {
		t.Fatalf("another generation was reported as a cpuset change: %v", err)
	}
	// An exact match is still observed.
	if err := inspectWith(t, live, func(map[string]any) {}); err != nil {
		t.Fatalf("an exact match was refused: %v", err)
	}
}

func TestCreateNamesUnavailableCPUs(t *testing.T) {
	plan := cpusetPlans(t, testAppCPUs)["candidate"]
	reply := func(status int, body string) *http.Client {
		return daemonClient(transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}))
	}
	_, err := createCandidateContainerHTTP(context.Background(), reply(400, `{"message":"Requested CPUs are not available - requested 1-3, available: 0-2"}`), plan)
	if !errors.Is(err, ErrAppCPUsUnavailable) || !errors.Is(err, ErrContainerUncertain) || !strings.Contains(err.Error(), AppCPUsUnavailableReason) {
		t.Fatalf("err = %v", err)
	}
	for _, other := range []string{`{"message":"no space left on device"}`, ``, `not json`} {
		_, err := createCandidateContainerHTTP(context.Background(), reply(500, other), plan)
		if errors.Is(err, ErrAppCPUsUnavailable) || !errors.Is(err, ErrContainerUncertain) {
			t.Fatalf("body %q: err = %v", other, err)
		}
	}
}
