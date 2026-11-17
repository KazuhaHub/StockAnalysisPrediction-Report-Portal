package appplatform

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// ErrInvalidCompatibility denotes malformed declarations or target information,
// rather than a valid release that does not match the current host.
var ErrInvalidCompatibility = errors.New("invalid application compatibility")

// CompatibilityTarget describes protocol versions, not the Portal CalVer release
// or its stable/beta channel. A nil runner means managed execution is unavailable.
type CompatibilityTarget struct {
	PlatformAPI string
	Runner      *RunnerCompatibility
}

type RunnerCompatibility struct {
	Protocol        string
	Platform        string
	BackendProtocol string
}

type CompatibilityIssue string

const (
	PlatformAPIMismatch     CompatibilityIssue = "platform_api_mismatch"
	RunnerRequired          CompatibilityIssue = "runner_required"
	RunnerProtocolMismatch  CompatibilityIssue = "runner_protocol_mismatch"
	RuntimePlatformMismatch CompatibilityIssue = "runtime_platform_mismatch"
	BackendProtocolMismatch CompatibilityIssue = "backend_protocol_mismatch"
)

// CompatibilityResult is only a compatibility observation. Even a compatible
// release still needs artifact trust, permission, capacity and data admission.
// Its zero value never permits execution.
type CompatibilityResult struct {
	Compatible bool
	Issues     []CompatibilityIssue
}

var (
	runtimePlatformPattern = regexp.MustCompile(`^[a-z0-9]{1,24}/[a-z0-9]{1,24}$`)
	backendProtocolPattern = regexp.MustCompile(`^portal\.backend/[1-9][0-9]{0,3}$`)
)

// EvaluateCompatibility performs bounded, side-effect-free protocol and runtime
// matching. Range semantics are pinned to Masterminds/semver v3.5.0, including its
// explicit prerelease opt-in. No global semver coercion flags are modified.
func EvaluateCompatibility(m Manifest, target CompatibilityTarget) (CompatibilityResult, error) {
	platformRange, err := compatibilityRange(m.Compatibility.PlatformAPI)
	if err != nil {
		return CompatibilityResult{}, err
	}
	runnerRange, err := compatibilityRange(m.Compatibility.RunnerProtocol)
	if err != nil {
		return CompatibilityResult{}, err
	}
	if !validManifest(m) {
		return CompatibilityResult{}, ErrInvalidDescriptor
	}
	// Parsing intentionally permits declarations without doing admission work.
	// Comparison has a finite numeric domain; refuse releases outside it here.
	if _, err := strictProtocolVersion(m.Version); err != nil {
		return CompatibilityResult{}, ErrInvalidCompatibility
	}
	platformVersion, err := strictProtocolVersion(target.PlatformAPI)
	if err != nil {
		return CompatibilityResult{}, err
	}

	result := CompatibilityResult{}
	if !platformRange.Check(platformVersion) {
		result.Issues = append(result.Issues, PlatformAPIMismatch)
	}
	if m.Backend != nil {
		if target.Runner == nil {
			result.Issues = append(result.Issues, RunnerRequired)
		} else {
			runnerVersion, err := strictProtocolVersion(target.Runner.Protocol)
			if err != nil || !runtimePlatformPattern.MatchString(target.Runner.Platform) || !backendProtocolPattern.MatchString(target.Runner.BackendProtocol) {
				return CompatibilityResult{}, ErrInvalidCompatibility
			}
			if !runnerRange.Check(runnerVersion) {
				result.Issues = append(result.Issues, RunnerProtocolMismatch)
			}
			if !slices.Contains(m.Backend.Platforms, target.Runner.Platform) {
				result.Issues = append(result.Issues, RuntimePlatformMismatch)
			}
			if m.Backend.Protocol != target.Runner.BackendProtocol {
				result.Issues = append(result.Issues, BackendProtocolMismatch)
			}
		}
	}
	result.Compatible = len(result.Issues) == 0
	return result, nil
}

func compatibilityRange(value string) (*semver.Constraints, error) {
	if !rangePattern.MatchString(value) {
		return nil, ErrInvalidCompatibility
	}
	// The upstream parser treats empty branches as a wildcard. Requiring an
	// explicit expression in every alternative prevents accidental fail-open.
	for _, branch := range strings.Split(value, "||") {
		if strings.TrimSpace(branch) == "" {
			return nil, ErrInvalidCompatibility
		}
	}
	constraint, err := semver.NewConstraint(value)
	if err != nil {
		return nil, ErrInvalidCompatibility
	}
	return constraint, nil
}

func strictProtocolVersion(value string) (*semver.Version, error) {
	if !validVersion(value) {
		return nil, ErrInvalidCompatibility
	}
	version, err := semver.StrictNewVersion(value)
	if err != nil {
		return nil, ErrInvalidCompatibility
	}
	return version, nil
}
