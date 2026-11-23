package driver

import (
	"encoding/json"
	"testing"
)

// inspectDocument is what `docker inspect <container>` prints for the container of a plan: a one-element array
// whose Config also carries what the image adds (environment, labels), which the plan does not own.
func inspectDocument(t *testing.T, plan containerPlan, mutate func(map[string]any)) []byte {
	t.Helper()
	v := containerInspectFixture(t, plan)
	config := v["Config"].(map[string]any)
	env := config["Env"].([]any)
	config["Env"] = append([]any{"PATH=/usr/local/bin:/usr/bin", "LANG=C.UTF-8"}, append(reverseAny(env), "PYTHON_VERSION=3.11.9")...)
	config["Labels"].(map[string]any)["org.opencontainers.image.title"] = "image label"
	config["Entrypoint"] = []any{"/usr/bin/python"}
	config["Cmd"] = []any{"-m", "app"}
	if mutate != nil {
		mutate(v)
	}
	raw, err := json.Marshal([]any{v})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func reverseAny(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func TestSeccompGateRecognisesAContainerCreatedUnderV1(t *testing.T) {
	for _, cpus := range []string{testAppCPUs, ""} {
		current := cpusetPlans(t, cpus)["candidate"]
		v1 := withSeccomp(current, v1Policy(t))
		report := InspectSeccompGate(inspectDocument(t, v1, nil))
		if !report.RecognisedAsV1 || report.Profile != "v1" || !report.PolicyDigestReproduced || report.Problem != "" {
			t.Fatalf("cpuset %q: a v1 container was not recognised: %+v", cpus, report)
		}
		// The label is part of the body whose digest the golden test pins to what 4bcc3d5b built.
		if report.PolicySHA256 != v1.labelPolicySHA256() || v1.bodySHA != goldenV1PlanSHA256[cpus]["candidate"] {
			t.Fatalf("cpuset %q: the container's policy digest %s is not the v1 plan's", cpus, report.PolicySHA256)
		}
		if report.PolicySHA256AfterRepair != current.labelPolicySHA256() {
			t.Fatalf("cpuset %q: the digest after repair is not the current plan's", cpus)
		}
		if report.PolicySHA256AfterRepair == "" || report.PolicySHA256AfterRepair == report.PolicySHA256 {
			t.Fatalf("cpuset %q: no distinct digest after repair: %+v", cpus, report)
		}
	}
}

func TestSeccompGateRefusesWhatIsNotAnExactV1Container(t *testing.T) {
	current := cpusetPlans(t, testAppCPUs)["candidate"]
	v1 := withSeccomp(current, v1Policy(t))
	setLabel := func(name, value string) func(map[string]any) {
		return func(v map[string]any) { v["Config"].(map[string]any)["Labels"].(map[string]any)[name] = value }
	}
	cases := map[string]struct {
		raw     []byte
		profile string
	}{
		"current profile":     {inspectDocument(t, current, nil), "current"},
		"policy label drift":  {inspectDocument(t, v1, setLabel(containerLabelPrefix+"policy-sha256", goldenV1PlanSHA256[testAppCPUs]["migration"])), "v1"},
		"profile label drift": {inspectDocument(t, v1, setLabel(containerLabelPrefix+"seccomp-profile", candidateSeccompProfile)), "v1"},
		"limit drift": {inspectDocument(t, v1, func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Memory"] = float64(1 << 20)
		}), "v1"},
		"unknown option": {inspectDocument(t, v1, func(v map[string]any) {
			v["HostConfig"].(map[string]any)["SecurityOpt"] = []any{"no-new-privileges:true", "seccomp=unconfined"}
		}), "unknown"},
		"not json":   {[]byte("nope"), ""},
		"empty list": {[]byte("[]"), ""},
	}
	for name, tc := range cases {
		report := InspectSeccompGate(tc.raw)
		if report.RecognisedAsV1 || report.Problem == "" {
			t.Fatalf("%s: accepted: %+v", name, report)
		}
		if report.Profile != tc.profile {
			t.Fatalf("%s: profile %q, want %q", name, report.Profile, tc.profile)
		}
	}
}
