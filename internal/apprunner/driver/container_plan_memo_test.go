package driver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func freshPlanMemo(t *testing.T) {
	t.Helper()
	old := planMemo
	planMemo = newDecisionMemo[containerCreate](64)
	t.Cleanup(func() { planMemo = old })
}

// The memo key must see every field of a plan. If a field is added, this fails until planKey covers it.
func TestPlanMemoKeyCoversEveryPlanField(t *testing.T) {
	if n := reflect.TypeOf(containerPlan{}).NumField(); n != 9 {
		t.Fatalf("containerPlan has %d fields: add the new one to planKey, then update this count", n)
	}
	base := containerFixture(t)
	want := planKey(base)
	mutations := map[string]func(*containerPlan){
		"name":        func(p *containerPlan) { p.name += "x" },
		"body":        func(p *containerPlan) { p.body = append(append([]byte(nil), p.body...), ' ') },
		"bodySHA":     func(p *containerPlan) { p.bodySHA = strings.Repeat("0", 64) },
		"owner":       func(p *containerPlan) { p.owner.WriterFence += "9" },
		"writable":    func(p *containerPlan) { p.writable = &WritableCandidateBinding{EffectID: "e"} },
		"updateStart": func(p *containerPlan) { p.updateStart = &UpdateStartBinding{PolicyRevision: "1"} },
		"migration":   func(p *containerPlan) { p.migration = &MigrationBinding{EffectID: "e"} },
		"profile":     func(p *containerPlan) { p.profile = "other" },
		"cpuset":      func(p *containerPlan) { p.cpuset = "0-1" },
	}
	for name, mutate := range mutations {
		p := base
		mutate(&p)
		if planKey(p) == want {
			t.Fatalf("the plan key ignores %s", name)
		}
	}
	// A field separator cannot be forged by moving bytes between neighbours.
	a, b := base, base
	a.name, a.profile = "ab", "c"
	b.name, b.profile = "a", "bc"
	if planKey(a) == planKey(b) {
		t.Fatal("plan key parts can collide by concatenation")
	}
}

// A plan is validated once; the parsed policy is served from memory afterwards. Plans that are not valid are
// never remembered, including a body edited after planning with its digest recomputed.
func TestPlanValidationIsRememberedAndInvalidPlansAreNot(t *testing.T) {
	freshPlanMemo(t)
	plan := containerFixture(t)
	for i := 0; i < 4; i++ {
		if !validContainerPlan(plan) {
			t.Fatal("valid plan refused", i)
		}
	}
	if hits, misses, size := planMemo.stats(); hits != 3 || misses != 1 || size != 1 {
		t.Fatalf("a plan must be validated once: hits=%d misses=%d size=%d", hits, misses, size)
	}
	body, ok := validatedContainerBody(plan)
	if !ok || body.Image == "" || len(body.HostConfig.Mounts) == 0 {
		t.Fatal("the parsed policy is not served", ok)
	}
	var request containerCreate
	if err := json.Unmarshal(plan.body, &request); err != nil {
		t.Fatal(err)
	}
	request.HostConfig.Memory *= 2
	edited := plan
	edited.body, _ = json.Marshal(request)
	edited.bodySHA = containerHash(edited.body)
	for i := 0; i < 2; i++ {
		if validContainerPlan(edited) {
			t.Fatal("an edited body was accepted with a warm memory")
		}
	}
	cpuset := plan
	cpuset.cpuset = "0-1"
	if validContainerPlan(cpuset) {
		t.Fatal("a plan pinned to another cpuset was accepted with a warm memory")
	}
	if _, _, size := planMemo.stats(); size != 1 {
		t.Fatalf("an invalid plan was remembered: %d entries", size)
	}
}
