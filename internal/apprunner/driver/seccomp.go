package driver

import (
	_ "embed"
	"encoding/json"
)

const (
	candidateSeccompProfile   = "ioctl-terminal-socket/v2-dev"
	candidateSeccompProfileV1 = "ioctl-terminal-socket/v1-dev"
)

// The ioctl requests each policy admits, in rule order. v1 is the policy the runner shipped before FIONCLEX and
// FIOCLEX were added; it is a strict subset of the current one and is rebuilt from the same frozen base.
var (
	candidateSeccompRequests   = []uint64{0x5401, 0x5410, 0x5413, 0x541b, 0x5421, 0x5450, 0x5451}
	candidateSeccompRequestsV1 = []uint64{0x5401, 0x5410, 0x5413, 0x541b, 0x5421}
)

// seccompPolicy is one entry of the closed set of policies a container may carry. live says a running or stopped
// container under it may still be observed, stopped and removed; history says a recorded receipt that names it
// is still readable. The two are separate so live acceptance can be retired once nothing runs under the policy,
// while its old receipts stay readable. A new container is only ever created under the first entry.
type seccompPolicy struct {
	name, option, sha string
	live, history     bool
}

// seccompPolicies is the only place the accepted set exists. The current policy is first.
var seccompPolicies = buildSeccompPolicies()

func buildSeccompPolicies() []seccompPolicy {
	var out []seccompPolicy
	if candidateSeccompError != nil {
		return out
	}
	out = append(out, seccompPolicy{name: candidateSeccompProfile, option: candidateSeccompOption, sha: containerHash([]byte(candidateSeccompOption)), live: true, history: true})
	if option, err := buildSeccompOption(candidateSeccompBase, candidateSeccompRequestsV1); err == nil {
		out = append(out, seccompPolicy{name: candidateSeccompProfileV1, option: option, sha: containerHash([]byte(option)), live: true, history: true})
	}
	return out
}

// SeccompMode says what a caller is about to do with a policy digest.
type SeccompMode int

const (
	// SeccompCurrent is for creating, starting, binding or settling a new start: the current policy only.
	SeccompCurrent SeccompMode = iota
	// SeccompLive is for observing, stopping or removing an existing container.
	SeccompLive
	// SeccompHistory is for reading a recorded receipt.
	SeccompHistory
)

// SeccompAccepted reports whether the policy digest is acceptable for what the caller is about to do.
func SeccompAccepted(sha string, mode SeccompMode) bool {
	for i, p := range seccompPolicies {
		if p.sha != sha {
			continue
		}
		switch mode {
		case SeccompCurrent:
			return i == 0
		case SeccompLive:
			return p.live
		case SeccompHistory:
			return p.history
		}
	}
	return false
}

// CurrentSeccompSHA256 is the digest of the option every new container is created under.
func CurrentSeccompSHA256() string {
	if len(seccompPolicies) == 0 {
		return ""
	}
	return seccompPolicies[0].sha
}

// seccompSHAOf is the digest of the option the plan's body carries.
func seccompSHAOf(plan containerPlan) string {
	if p, ok := seccompPolicyNamed(plan.seccomp); ok {
		return p.sha
	}
	return ""
}

func seccompPolicyNamed(name string) (seccompPolicy, bool) {
	for _, p := range seccompPolicies {
		if p.name == name {
			return p, true
		}
	}
	return seccompPolicy{}, false
}

// Frozen upstream bytes and licensing are recorded in seccomp/README.md. Never
// fetch a profile from the network while admitting or starting an application.
//
//go:embed seccomp/moby-default.json
var candidateSeccompBase []byte

var candidateSeccompOption, candidateSeccompError = buildCandidateSeccomp(candidateSeccompBase)

// Keep the upstream default-deny syscall rules, replacing its unrestricted ioctl
// allowance with these exact terminal/socket/close-on-exec requests. In particular, none of the
// filesystem attribute setters can be reached through ioctl, including compat
// command numbers. The profile is applied by Docker before the image entrypoint.
func buildCandidateSeccomp(base []byte) (string, error) {
	return buildSeccompOption(base, candidateSeccompRequests)
}

func buildSeccompOption(base []byte, requests []uint64) (string, error) {
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
	// TCGETS, TIOCSPGRP, TIOCGWINSZ, FIONREAD, FIONBIO, FIONCLEX and FIOCLEX have
	// identical values on the supported Linux amd64/arm64 ABIs. No pointer contents are trusted or
	// interpreted by this filter; the kernel still checks the descriptor/argument.
	// Docker init calls TIOCSPGRP even for a non-TTY stdin, requiring ENOTTY
	// from the kernel rather than a seccomp EPERM before executing the image.
	// FIOCLEX and FIONCLEX only set or clear close-on-exec on a descriptor the
	// process already holds. CPython 3.11 calls FIOCLEX when it opens a script
	// file and treats only ENOTTY/EACCES as a reason to fall back to fcntl, so an
	// EPERM here made every "python script.py" fail.
	for _, request := range requests {
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
