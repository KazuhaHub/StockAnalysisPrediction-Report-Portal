package driver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The exact ioctl request set, in rule order: TCGETS, TIOCSPGRP, TIOCGWINSZ, FIONREAD, FIONBIO,
// FIONCLEX and FIOCLEX. The last two only toggle close-on-exec; CPython calls FIOCLEX when it opens a script.
var wantIOCTLRequests = []uint64{0x5401, 0x5410, 0x5413, 0x541b, 0x5421, 0x5450, 0x5451}

func TestCandidateSeccompProfileNameNamesTheRequestSet(t *testing.T) {
	// The v1 profile allowed five requests. A different set must not carry the old name.
	if candidateSeccompProfile != "ioctl-terminal-socket/v2-dev" {
		t.Fatal("profile name does not identify the current request set", candidateSeccompProfile)
	}
}

func TestCandidateSeccompAllowsNoFilesystemAttributeIOCTL(t *testing.T) {
	var derived struct {
		Syscalls []struct {
			Names []string `json:"names"`
			Args  []struct {
				Value uint64 `json:"value"`
			} `json:"args"`
		} `json:"syscalls"`
	}
	if candidateSeccompError != nil || json.Unmarshal([]byte(strings.TrimPrefix(candidateSeccompOption, "seccomp=")), &derived) != nil {
		t.Fatal("invalid profile")
	}
	allowed := map[uint64]bool{}
	for _, rule := range derived.Syscalls {
		for _, name := range rule.Names {
			if name != "ioctl" {
				continue
			}
			if len(rule.Args) != 1 {
				t.Fatal("an ioctl rule is not constrained to one request")
			}
			allowed[rule.Args[0].Value] = true
		}
	}
	if len(allowed) != len(wantIOCTLRequests) {
		t.Fatal("unexpected ioctl request set", allowed)
	}
	for _, request := range wantIOCTLRequests {
		if !allowed[request] {
			t.Fatalf("ioctl %#x is not allowed", request)
		}
	}
	// FS_IOC_SETFLAGS and FS_IOC_FSSETXATTR with their 32-bit compat numbers, and the getters.
	for _, request := range []uint64{0x40086602, 0x40046602, 0x401c5820, 0x80086601, 0x801c581f} {
		if allowed[request] {
			t.Fatalf("filesystem attribute ioctl %#x is allowed", request)
		}
	}
}

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
	if !reflect.DeepEqual(base, derived) || len(derivedRules) != len(baseRules)+len(wantIOCTLRequests) {
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
	for i, request := range wantIOCTLRequests {
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
