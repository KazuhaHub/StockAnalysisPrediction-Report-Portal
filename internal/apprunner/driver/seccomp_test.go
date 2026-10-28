package driver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCandidateSeccompPreservesFrozenBaseExceptIOCTL(t *testing.T) {
	if containerHash(candidateSeccompBase) != "6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6" {
		t.Fatal("upstream seccomp base changed without a reviewed update")
	}
	if candidateSeccompError != nil || !strings.HasPrefix(candidateSeccompOption, "seccomp=") {
		t.Fatal("closed seccomp profile unavailable", candidateSeccompError)
	}
	var base, derived map[string]any
	if json.Unmarshal(candidateSeccompBase, &base) != nil || json.Unmarshal([]byte(strings.TrimPrefix(candidateSeccompOption, "seccomp=")), &derived) != nil {
		t.Fatal("invalid profile")
	}
	baseRules, derivedRules := base["syscalls"].([]any), derived["syscalls"].([]any)
	delete(base, "syscalls")
	delete(derived, "syscalls")
	if !reflect.DeepEqual(base, derived) || len(derivedRules) != len(baseRules)+4 {
		t.Fatal("non-ioctl default policy changed")
	}
	for i, raw := range baseRules {
		rule := raw.(map[string]any)
		kept := []any{}
		for _, name := range rule["names"].([]any) {
			if name != "ioctl" {
				kept = append(kept, name)
			}
		}
		rule["names"] = kept
		if !reflect.DeepEqual(rule, derivedRules[i]) {
			t.Fatalf("upstream syscall rule %d broadened/changed", i)
		}
	}
	for i, request := range []uint64{0x5401, 0x5413, 0x541b, 0x5421} {
		expected := map[string]any{
			"names": []any{"ioctl"}, "action": "SCMP_ACT_ALLOW",
			"args": []any{map[string]any{"index": float64(1), "value": float64(request), "op": "SCMP_CMP_EQ"}},
		}
		if !reflect.DeepEqual(expected, derivedRules[len(baseRules)+i]) {
			t.Fatal("ioctl rule is not an exact argument-constrained request")
		}
	}
}

func TestCandidateSeccompRejectsUnexpectedBase(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["ioctl"],"action":"SCMP_ACT_LOG"}]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["ioctl","ioctl"],"action":"SCMP_ACT_ALLOW"}]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["ioctl"],"action":"SCMP_ACT_ALLOW","args":[]}]}`,
	} {
		if option, err := buildCandidateSeccomp([]byte(raw)); err == nil || option != "" {
			t.Fatal("unexpected base accepted", raw)
		}
	}
}
