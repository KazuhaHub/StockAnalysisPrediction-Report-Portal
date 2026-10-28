package driver

import (
	_ "embed"
	"encoding/json"
)

const candidateSeccompProfile = "ioctl-terminal-socket/v1-dev"

// Frozen upstream bytes and licensing are recorded in seccomp/README.md. Never
// fetch a profile from the network while admitting or starting an application.
//
//go:embed seccomp/moby-default.json
var candidateSeccompBase []byte

var candidateSeccompOption, candidateSeccompError = buildCandidateSeccomp(candidateSeccompBase)

// Keep the upstream default-deny syscall rules, replacing its unrestricted ioctl
// allowance with these exact terminal/socket requests. In particular, none of the
// filesystem attribute setters can be reached through ioctl, including compat
// command numbers. The profile is applied by Docker before the image entrypoint.
func buildCandidateSeccomp(base []byte) (string, error) {
	var profile map[string]json.RawMessage
	if json.Unmarshal(base, &profile) != nil || string(profile["defaultAction"]) != `"SCMP_ACT_ERRNO"` {
		return "", ErrInvalidResponse
	}
	var rules []map[string]json.RawMessage
	if json.Unmarshal(profile["syscalls"], &rules) != nil {
		return "", ErrInvalidResponse
	}
	replaced := 0
	for _, rule := range rules {
		var names []string
		if json.Unmarshal(rule["names"], &names) != nil {
			return "", ErrInvalidResponse
		}
		kept := make([]string, 0, len(names))
		for _, name := range names {
			if name != "ioctl" {
				kept = append(kept, name)
				continue
			}
			if len(rule) != 2 || string(rule["action"]) != `"SCMP_ACT_ALLOW"` {
				return "", ErrInvalidResponse
			}
			replaced++
		}
		rule["names"], _ = json.Marshal(kept)
	}
	if replaced != 1 {
		return "", ErrInvalidResponse
	}
	// TCGETS, TIOCSPGRP, TIOCGWINSZ, FIONREAD and FIONBIO have identical values on
	// the supported Linux amd64/arm64 ABIs. No pointer contents are trusted or
	// interpreted by this filter; the kernel still checks the descriptor/argument.
	// Docker init calls TIOCSPGRP even for a non-TTY stdin, requiring ENOTTY
	// from the kernel rather than a seccomp EPERM before executing the image.
	for _, request := range []uint64{0x5401, 0x5410, 0x5413, 0x541b, 0x5421} {
		raw, _ := json.Marshal(seccompIOCTLRule{[]string{"ioctl"}, "SCMP_ACT_ALLOW", []seccompArgument{{1, request, "SCMP_CMP_EQ"}}})
		var rule map[string]json.RawMessage
		if json.Unmarshal(raw, &rule) != nil {
			return "", ErrInvalidResponse
		}
		rules = append(rules, rule)
	}
	profile["syscalls"], _ = json.Marshal(rules)
	raw, err := json.Marshal(profile)
	if err != nil || len(raw) > 16384 {
		return "", ErrInvalidResponse
	}
	return "seccomp=" + string(raw), nil
}

// Only this typed rule is added to the frozen base policy.
type seccompIOCTLRule struct {
	Names  []string          `json:"names"`
	Action string            `json:"action"`
	Args   []seccompArgument `json:"args"`
}
type seccompArgument struct {
	Index int    `json:"index"`
	Value uint64 `json:"value"`
	Op    string `json:"op"`
}
