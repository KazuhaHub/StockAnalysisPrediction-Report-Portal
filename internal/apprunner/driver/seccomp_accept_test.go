package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Digests computed at 4bcc3d5b, the commit the production runner (v2026.48.1) was built from, by building the
// plans of cpusetPlans there. The v1 profile is the option that commit built; the plan digests are the policy
// bodies it created containers from. A container in production carries exactly these bytes.
const (
	goldenV1OptionSHA256 = "981fc0c0c4ee290a969bed30c93e8ea443a4a0c42a0bdbadddd4f9f24eb142f5"
)

var goldenV1PlanSHA256 = map[string]map[string]string{
	testAppCPUs: {
		"candidate":          "e74b980ec97271ff0ba8278a48c2b9270c6d71255b8a674ce3bbfefba37a1114",
		"migration":          "5c6d9aa3bd6671762f9a4b22b0764f76c5c50a8ef9d4fcacdf1efbe357164c5a",
		"writable candidate": "77d3610c2242825345fe663e93957e876eede07f277adeec15b76dd9de380fc1",
		"update start " + UpdateStartWritableSandboxProfile: "d88d06d6fe819d6f6c66639fe79e6c515c8324af6390d5e217b4275ad4d67dbe",
		"update start " + UpdateStartReadOnlySandboxProfile: "6cb58c8fc95e28c01c71b1e1c99fb56c77879d1e179b3e7f322a11675432fc9c",
	},
	"": {
		"candidate":          "68e789d2eb95e28e8ff5d6c8a0d2c9424532ef2fe485c63e2201a04840a4a51a",
		"migration":          "9fa17da98942302fa76c6efc1483b7c4ef030b4b76c7a21db75474b8945c1d90",
		"writable candidate": "9c030bcb3d753e2c8217304fa5619bd3fd1beb9e89066ee8f0a9c46c3852593b",
		"update start " + UpdateStartWritableSandboxProfile: "0e14611b5acd7f0f1e59aa6d0a78370568c4b0856d899cd7f3a6d8279d8b5d63",
		"update start " + UpdateStartReadOnlySandboxProfile: "68f150c49c087a91e17355370fc416030e30318493d7947136b6ad8da473afac",
	},
}

func v1Policy(t *testing.T) seccompPolicy {
	t.Helper()
	if len(seccompPolicies) != 2 {
		t.Fatal("the accepted seccomp set is not exactly current and v1", len(seccompPolicies))
	}
	return seccompPolicies[1]
}

func TestSeccompAcceptedSetIsClosedOrderedAndNarrower(t *testing.T) {
	if len(seccompPolicies) != 2 || seccompPolicies[0].name != candidateSeccompProfile || seccompPolicies[0].option != candidateSeccompOption {
		t.Fatal("current policy is not first in the accepted set")
	}
	v1 := v1Policy(t)
	if v1.name != "ioctl-terminal-socket/v1-dev" || v1.sha != goldenV1OptionSHA256 || containerHash([]byte(v1.option)) != goldenV1OptionSHA256 {
		t.Fatal("the rebuilt v1 option differs from the one 4bcc3d5b built", v1.name, v1.sha)
	}
	rules := func(option string) map[uint64]bool {
		var p struct {
			Syscalls []struct {
				Names []string `json:"names"`
				Args  []struct{ Value uint64 }
			}
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(option, "seccomp=")), &p) != nil {
			t.Fatal("invalid option")
		}
		out := map[uint64]bool{}
		for _, r := range p.Syscalls {
			for _, n := range r.Names {
				if n == "ioctl" {
					out[r.Args[0].Value] = true
				}
			}
		}
		return out
	}
	old, current := rules(v1.option), rules(candidateSeccompOption)
	for request := range old {
		if !current[request] {
			t.Fatalf("v1 allows %#x, which the current policy does not", request)
		}
	}
	if len(old) != 5 || len(current) != 7 || !current[0x5450] || !current[0x5451] || old[0x5450] || old[0x5451] {
		t.Fatal("v2 minus v1 is not exactly FIONCLEX and FIOCLEX", old, current)
	}
}

func TestSeccompModes(t *testing.T) {
	v1, v2 := v1Policy(t).sha, seccompPolicies[0].sha
	for name, tc := range map[string]struct {
		sha  string
		mode SeccompMode
		want bool
	}{
		"current in current":  {v2, SeccompCurrent, true},
		"v1 in current":       {v1, SeccompCurrent, false},
		"current in live":     {v2, SeccompLive, true},
		"v1 in live":          {v1, SeccompLive, true},
		"current in history":  {v2, SeccompHistory, true},
		"v1 in history":       {v1, SeccompHistory, true},
		"unknown in live":     {strings.Repeat("a", 64), SeccompLive, false},
		"empty in history":    {"", SeccompHistory, false},
		"unknown mode":        {v2, SeccompMode(99), false},
		"unknown in current":  {strings.Repeat("a", 64), SeccompCurrent, false},
		"unknown in history":  {strings.Repeat("a", 64), SeccompHistory, false},
		"v1 digest uppercase": {strings.ToUpper(v1), SeccompHistory, false},
	} {
		if SeccompAccepted(tc.sha, tc.mode) != tc.want {
			t.Fatalf("%s: SeccompAccepted = %v", name, !tc.want)
		}
	}
	if CurrentSeccompSHA256() != v2 || DataWriterPolicySHA256() != v2 || UpdateStartSeccompSHA256(UpdateStartReadOnlySandboxProfile) != v2 {
		t.Fatal("the current digest accessors disagree")
	}
}

// Live and history acceptance are separate switches, so live acceptance of v1 can be retired once nothing
// runs under it while old receipts stay readable.
func TestSeccompLiveAcceptanceRetiresIndependentlyOfHistory(t *testing.T) {
	saved := seccompPolicies
	t.Cleanup(func() { seccompPolicies = saved })
	retired := append([]seccompPolicy(nil), saved...)
	retired[1].live = false
	seccompPolicies = retired
	if SeccompAccepted(retired[1].sha, SeccompLive) || !SeccompAccepted(retired[1].sha, SeccompHistory) {
		t.Fatal("retiring live acceptance changed history acceptance")
	}
	base := cpusetPlans(t, testAppCPUs)["candidate"]
	v1 := withSeccomp(base, saved[1])
	if got := selectRecordedPlan(base, v1.bodySHA); got.bodySHA != base.bodySHA {
		t.Fatal("a retired live policy still selects a plan")
	}
}

func TestV1PlansReproduceTheProductionDigests(t *testing.T) {
	for cpus, want := range goldenV1PlanSHA256 {
		plans := cpusetPlans(t, cpus)
		if len(plans) != len(want) {
			t.Fatal("plan kinds changed", len(plans))
		}
		for kind, plan := range plans {
			v1 := withSeccomp(plan, v1Policy(t))
			if v1.bodySHA != want[kind] {
				t.Fatalf("cpuset %q %s: v1 plan digest %s, 4bcc3d5b built %s", cpus, kind, v1.bodySHA, want[kind])
			}
			if !validContainerPlan(v1) || v1.seccomp != "ioctl-terminal-socket/v1-dev" || plan.seccomp != candidateSeccompProfile || v1.bodySHA == plan.bodySHA {
				t.Fatalf("%s: variant plans are not distinct valid plans", kind)
			}
		}
	}
}

func TestPlansRefuseMixedOrUnknownSeccomp(t *testing.T) {
	v1 := v1Policy(t)
	for kind, plan := range cpusetPlans(t, testAppCPUs) {
		mutate := func(change func(*containerCreate)) containerPlan {
			var body containerCreate
			if json.Unmarshal(plan.body, &body) != nil {
				t.Fatal("body")
			}
			change(&body)
			delete(body.Labels, containerLabelPrefix+"policy-sha256")
			policy, _ := json.Marshal(body)
			body.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
			raw, _ := json.Marshal(body)
			out := plan
			out.body, out.bodySHA = raw, containerHash(raw)
			return out
		}
		for name, p := range map[string]containerPlan{
			"v1 label with v2 option": mutate(func(b *containerCreate) { b.Labels[containerLabelPrefix+"seccomp-profile"] = v1.name }),
			"v2 label with v1 option": mutate(func(b *containerCreate) { b.HostConfig.SecurityOpt[1] = v1.option }),
			"unknown option":          mutate(func(b *containerCreate) { b.HostConfig.SecurityOpt[1] = "seccomp=unconfined" }),
			"unknown label":           mutate(func(b *containerCreate) { b.Labels[containerLabelPrefix+"seccomp-profile"] = "other/v9" }),
			"no option":               mutate(func(b *containerCreate) { b.HostConfig.SecurityOpt = b.HostConfig.SecurityOpt[:1] }),
		} {
			if validContainerPlan(p) {
				t.Fatalf("%s: %s accepted", kind, name)
			}
		}
		// A plan that names a policy its body does not carry is refused even when both are individually known.
		mixed := withSeccomp(plan, v1)
		mixed.seccomp = candidateSeccompProfile
		if validContainerPlan(mixed) {
			t.Fatalf("%s: a v1 body under the current policy name was accepted", kind)
		}
	}
}

func TestSelectRecordedPlanUsesTheRecordedDigestOnly(t *testing.T) {
	for kind, base := range cpusetPlans(t, testAppCPUs) {
		v1 := withSeccomp(base, v1Policy(t))
		if got := selectRecordedPlan(base, v1.bodySHA); got.bodySHA != v1.bodySHA || got.seccomp != v1.seccomp {
			t.Fatalf("%s: the recorded v1 digest did not select the v1 plan", kind)
		}
		for name, recorded := range map[string]string{"empty": "", "current": base.bodySHA, "unknown": strings.Repeat("e", 64), "garbage": "x"} {
			if got := selectRecordedPlan(base, recorded); got.bodySHA != base.bodySHA {
				t.Fatalf("%s: a %s digest selected another plan", kind, name)
			}
		}
	}
}

func TestInspectionOfLiveV1Container(t *testing.T) {
	inspect := func(live, planned containerPlan) error {
		v := containerInspectFixture(t, live)
		client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := json.Marshal(v)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		}))
		_, err := inspectCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), planned)
		return err
	}
	for kind, current := range cpusetPlans(t, testAppCPUs) {
		v1 := withSeccomp(current, v1Policy(t))
		if err := inspect(v1, v1); err != nil {
			t.Fatalf("%s: a live v1 container was refused against its own plan: %v", kind, err)
		}
		if err := inspect(v1, current); !errors.Is(err, ErrContainerConflict) {
			t.Fatalf("%s: a live v1 container was adopted by the current plan: %v", kind, err)
		}
		if err := inspect(current, v1); !errors.Is(err, ErrContainerConflict) {
			t.Fatalf("%s: a live current container was adopted by the v1 plan: %v", kind, err)
		}
		unknown := current
		var body containerCreate
		json.Unmarshal(current.body, &body)
		body.HostConfig.SecurityOpt[1] = "seccomp=unconfined"
		unknown.body, _ = json.Marshal(body)
		for _, planned := range []containerPlan{current, v1} {
			if err := inspect(unknown, planned); !errors.Is(err, ErrContainerConflict) {
				t.Fatalf("%s: a container with an unknown profile was adopted: %v", kind, err)
			}
		}
	}
}

// historyDaemon is a daemon whose single container reports the state it was last put in.
type historyDaemon struct {
	t        *testing.T
	plan     containerPlan
	status   string
	requests []string
}

func (d *historyDaemon) client() *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		d.requests = append(d.requests, r.Method+" "+r.URL.Path)
		body := io.NopCloser(strings.NewReader(""))
		switch {
		case r.Method == http.MethodGet:
			v := containerInspectFixture(d.t, d.plan)
			v["State"] = map[string]any{"Status": d.status, "Running": d.status == "running", "ExitCode": 0}
			raw, _ := json.Marshal(v)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		case strings.HasSuffix(r.URL.Path, "/stop"):
			d.status = "exited"
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: body}, nil
		case r.Method == http.MethodDelete:
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: body}, nil
		}
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: body}, nil
	}))
}

func (d *historyDaemon) count(prefix string) int {
	n := 0
	for _, r := range d.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func TestHistoricalPlanStopsAndRemovesButNeverCreatesOrStarts(t *testing.T) {
	id := strings.Repeat("c", 64)
	for kind, current := range cpusetPlans(t, testAppCPUs) {
		v1 := withSeccomp(current, v1Policy(t))
		d := &historyDaemon{t: t, plan: v1, status: "running"}
		if state, err := stopCandidateContainerHTTP(context.Background(), d.client(), id, v1); err != nil || state != "exited" || d.count("POST") != 1 {
			t.Fatalf("%s: stop of a v1 container: %v %q %v", kind, err, state, d.requests)
		}
		if err := removeCandidateContainerHTTP(context.Background(), d.client(), id, v1); err != nil || d.count("DELETE") != 1 {
			t.Fatalf("%s: removal of a v1 container: %v %v", kind, err, d.requests)
		}

		d = &historyDaemon{t: t, plan: v1, status: "created"}
		if _, err := createCandidateContainerHTTP(context.Background(), d.client(), v1); err == nil {
			t.Fatalf("%s: a v1 container was created", kind)
		}
		if _, err := startCandidateContainerHTTP(context.Background(), d.client(), id, v1); err == nil {
			t.Fatalf("%s: a v1 container was started", kind)
		}
		if d.count("POST") != 0 {
			t.Fatalf("%s: a historical plan reached the daemon with %v", kind, d.requests)
		}
		// Already running is still observable.
		d = &historyDaemon{t: t, plan: v1, status: "running"}
		if state, err := startCandidateContainerHTTP(context.Background(), d.client(), id, v1); err != nil || state != "running" || d.count("POST") != 0 {
			t.Fatalf("%s: observing a running v1 container: %v %q %v", kind, err, state, d.requests)
		}
	}
	migration := cpusetPlans(t, testAppCPUs)["migration"]
	d := &historyDaemon{t: t, plan: withSeccomp(migration, v1Policy(t)), status: "created"}
	if _, err := runMigrationJobHTTP(context.Background(), d.client(), withSeccomp(migration, v1Policy(t)), 1, 1); err == nil || d.count("POST") != 0 {
		t.Fatalf("a v1 migration job was run: %v %v", err, d.requests)
	}
}

func TestUpdateStartSourceIsPlannedByItsRecordedPolicy(t *testing.T) {
	plans := cpusetPlans(t, testAppCPUs)
	current := plans["update start "+UpdateStartWritableSandboxProfile]
	v1 := withSeccomp(current, v1Policy(t))
	if !validUpdateStartSourcePolicy(v1.bodySHA, current) || !validUpdateStartSourcePolicy(current.bodySHA, current) {
		t.Fatal("a recorded current or v1 digest was refused")
	}
	if validUpdateStartSourcePolicy(strings.Repeat("e", 64), current) || validUpdateStartSourcePolicy("", current) {
		t.Fatal("an unmatched or empty source digest was accepted")
	}
}

func TestMigrationCleanupTriesEveryAcceptedPolicy(t *testing.T) {
	current := cpusetPlans(t, testAppCPUs)["migration"]
	v1 := withSeccomp(current, v1Policy(t))
	d := &historyDaemon{t: t, plan: v1, status: "exited"}
	got := migrationCleanupPlan(context.Background(), d.client(), strings.Repeat("c", 64), current)
	if got.seccomp != v1.seccomp {
		t.Fatal("cleanup did not match the leftover v1 job")
	}
	d = &historyDaemon{t: t, plan: current, status: "exited"}
	if got := migrationCleanupPlan(context.Background(), d.client(), strings.Repeat("c", 64), current); got.seccomp != current.seccomp {
		t.Fatal("cleanup moved off the current plan for a current job")
	}
}
