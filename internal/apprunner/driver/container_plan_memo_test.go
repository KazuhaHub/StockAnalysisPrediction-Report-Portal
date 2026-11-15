package driver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func freshPlanMemo(t *testing.T) {
	t.Helper()
	old := planMemo
	planMemo = newDecisionMemo[containerCreate](64)
	t.Cleanup(func() { planMemo = old })
}

// planLeaves lists every leaf of a plan as a path, walking nested structs. A pointer to a struct is walked
// too and contributes a presence leaf ("writable?"). Anything the key encoding cannot cover without
// ambiguity (a map, an interface, a pointer to a non-struct, a pointer below the top level, a slice that is
// not bytes) fails the walk.
func planLeaves(t *testing.T, typ reflect.Type, prefix string, top bool, out *[]string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		path := prefix + f.Name
		switch f.Type.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64:
			*out = append(*out, path)
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.Uint8 {
				t.Fatalf("%s is a %s: the plan key cannot encode it", path, f.Type)
			}
			*out = append(*out, path)
		case reflect.Struct:
			planLeaves(t, f.Type, path+".", false, out)
		case reflect.Pointer:
			if !top || f.Type.Elem().Kind() != reflect.Struct {
				t.Fatalf("%s is a %s: the plan key cannot encode it", path, f.Type)
			}
			*out = append(*out, path+"?")
			planLeaves(t, f.Type.Elem(), path+".", false, out)
		default:
			t.Fatalf("%s is a %s: the plan key cannot encode it", path, f.Type)
		}
	}
}

// mutateLeaf changes exactly one leaf of a plan whose pointers are all present.
func mutateLeaf(t *testing.T, p *containerPlan, path string) {
	t.Helper()
	if strings.HasSuffix(path, "?") {
		f := reflect.ValueOf(p).Elem().FieldByName(strings.TrimSuffix(path, "?"))
		reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem().SetZero()
		return
	}
	v := reflect.ValueOf(p).Elem()
	for _, name := range strings.Split(path, ".") {
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		v = v.FieldByName(name)
	}
	switch v.Kind() {
	case reflect.String:
		reflect.NewAt(v.Type(), v.Addr().UnsafePointer()).Elem().SetString(v.String() + "x")
	case reflect.Bool:
		reflect.NewAt(v.Type(), v.Addr().UnsafePointer()).Elem().SetBool(!v.Bool())
	case reflect.Slice:
		reflect.NewAt(v.Type(), v.Addr().UnsafePointer()).Elem().SetBytes(append(append([]byte(nil), v.Bytes()...), ' '))
	default:
		t.Fatalf("no mutation for %s", path)
	}
}

// The memo key must see every leaf of a plan, nested structs included. If a field is added, the walk finds
// it, this list stops matching, and the key encoding has to be reviewed.
func TestPlanMemoKeyCoversEveryLeafField(t *testing.T) {
	covered := []string{"name", "body", "bodySHA",
		"owner.DeploymentID", "owner.ControllerEpoch", "owner.InstallationID", "owner.OperationID", "owner.ResourceID", "owner.NamespaceID",
		"owner.ReleaseSHA256", "owner.Generation", "owner.ControlFence", "owner.WriterFence",
		"writable?", "writable.EffectID", "writable.ClaimID", "writable.PayloadSHA256", "writable.OwnershipSHA256", "writable.DataReceiptSHA256", "writable.WriterReceiptSHA256",
		"updateStart?", "updateStart.EffectID", "updateStart.ClaimID", "updateStart.PayloadSHA256", "updateStart.OwnershipSHA256", "updateStart.CheckpointReceiptSHA256", "updateStart.PolicyRevision",
		"migration?", "migration.EffectID", "migration.ClaimID", "migration.PayloadSHA256", "migration.OwnershipSHA256", "migration.CheckpointReceiptSHA256",
		"profile", "cpuset"}
	var found []string
	planLeaves(t, reflect.TypeOf(containerPlan{}), "", true, &found)
	slices.Sort(covered)
	slices.Sort(found)
	if !slices.Equal(covered, found) {
		t.Fatalf("containerPlan leaves changed: found %v, covered %v", found, covered)
	}
	build := func() containerPlan {
		p := containerFixture(t)
		p.writable, p.updateStart, p.migration = &WritableCandidateBinding{}, &UpdateStartBinding{}, &MigrationBinding{}
		return p
	}
	want := planKey(build())
	for _, leaf := range covered {
		p := build()
		mutateLeaf(t, &p, leaf)
		if planKey(p) == want {
			t.Fatalf("the plan key ignores %s", leaf)
		}
	}
}

// %+v prints these pairs identically; the key must still tell them apart.
func TestPlanMemoKeyIsUnambiguous(t *testing.T) {
	base := containerFixture(t)
	a, b := base, base
	a.owner.DeploymentID, a.owner.ControllerEpoch = "a ControllerEpoch:b", ""
	b.owner.DeploymentID, b.owner.ControllerEpoch = "a", "b ControllerEpoch:"
	if fmt.Sprintf("%+v", a.owner) != fmt.Sprintf("%+v", b.owner) {
		t.Fatal("the fixture no longer shows the formatting ambiguity")
	}
	if planKey(a) == planKey(b) {
		t.Fatal("two different owners share a plan key")
	}
	a, b = base, base
	a.writable = &WritableCandidateBinding{EffectID: "a ClaimID:b"}
	b.writable = &WritableCandidateBinding{EffectID: "a", ClaimID: "b ClaimID:"}
	if fmt.Sprintf("%+v", *a.writable) != fmt.Sprintf("%+v", *b.writable) {
		t.Fatal("the fixture no longer shows the formatting ambiguity")
	}
	if planKey(a) == planKey(b) {
		t.Fatal("two different bindings share a plan key")
	}
	// Moving bytes between neighbours cannot forge a key either.
	a, b = base, base
	a.name, a.profile = "ab", "c"
	b.name, b.profile = "a", "bc"
	if planKey(a) == planKey(b) {
		t.Fatal("plan key parts can collide by concatenation")
	}
	// An absent binding and a zero binding are different plans.
	a, b = base, base
	b.writable = &WritableCandidateBinding{}
	if planKey(a) == planKey(b) {
		t.Fatal("an absent binding keys like a zero one")
	}
}

// The memoized policy is private to the memo: a caller that edits what it was given changes nothing for
// the next caller, on a hit or on the miss that stored it.
func TestValidatedContainerBodyIsACopy(t *testing.T) {
	freshPlanMemo(t)
	plan := containerFixture(t)
	scribble := func(b *containerCreate) {
		for k := range b.Labels {
			b.Labels[k] = "scribble"
		}
		b.Labels["extra"] = "x"
		b.Env[0] = "scribble"
		b.Healthcheck.Test[0] = "scribble"
		b.HostConfig.CapDrop[0] = "scribble"
		b.HostConfig.SecurityOpt[0] = "scribble"
		b.HostConfig.Mounts[0].Source = "/scribble"
		b.HostConfig.Mounts = b.HostConfig.Mounts[:1]
	}
	var want containerCreate
	if err := json.Unmarshal(plan.body, &want); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // the first call is the miss that stores
		got, ok := validatedContainerBody(plan)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatal("the memoized policy differs from the plan body", i)
		}
		scribble(&got)
	}
	// The clone must be a deep copy of every reference-typed field the policy has.
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Interface, reflect.Chan, reflect.Func:
			t.Fatalf("containerCreate holds a %s the clone cannot copy", typ)
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				walk(typ.Field(i).Type)
			}
		case reflect.Slice, reflect.Pointer, reflect.Array, reflect.Map:
			walk(typ.Elem())
		}
	}
	walk(reflect.TypeOf(containerCreate{}))
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
