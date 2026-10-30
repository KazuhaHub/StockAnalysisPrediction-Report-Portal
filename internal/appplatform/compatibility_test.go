package appplatform

import (
	"errors"
	"strings"
	"testing"
)

func compatibilityFixture(t *testing.T, managed bool) Manifest {
	t.Helper()
	parsed, err := ParseDescriptor(descriptorFixture(t, managed))
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Manifest
}

func compatibilityTarget() CompatibilityTarget {
	return CompatibilityTarget{
		PlatformAPI: "2.3.1",
		Runner:      &RunnerCompatibility{Protocol: "1.4.0", Platform: "linux/amd64", BackendProtocol: "portal.backend/1"},
	}
}

func TestCompatibilityStaticDoesNotRequireRunner(t *testing.T) {
	manifest := compatibilityFixture(t, false)
	result, err := EvaluateCompatibility(manifest, CompatibilityTarget{PlatformAPI: "2.3.1"})
	if err != nil || !result.Compatible || len(result.Issues) != 0 {
		t.Fatalf("static application refused without runner: %+v %v", result, err)
	}
}

func TestCompatibilityRangeSemantics(t *testing.T) {
	cases := []struct {
		name, constraint, version string
		compatible                bool
	}{
		{"lower inclusive", ">=2.0.0 <3.0.0", "2.0.0", true},
		{"upper exclusive", ">=2.0.0 <3.0.0", "3.0.0", false},
		{"numeric minor", ">=2.9.0 <3.0.0", "2.10.0", true},
		{"conjunction", ">2.0.0 <=2.3.1", "2.3.1", true},
		{"exact", "2.3.1", "2.3.1", true},
		{"exact refusal", "2.3.1", "2.3.2", false},
		{"caret major", "^2.1.0", "2.9.0", true},
		{"caret major refusal", "^2.1.0", "3.0.0", false},
		{"caret zero", "^0.2.3", "0.3.0", false},
		{"tilde", "~2.3.0", "2.3.9", true},
		{"tilde refusal", "~2.3.0", "2.4.0", false},
		{"wildcard", "2.*", "2.99.0", true},
		{"any explicit", "*", "19.0.0", true},
		{"or", ">=1.0.0 <2.0.0 || >=3.0.0 <4.0.0", "3.1.0", true},
		{"or gap", ">=1.0.0 <2.0.0 || >=3.0.0 <4.0.0", "2.1.0", false},
		{"hyphen", "2.0.0 - 2.3.1", "2.3.1", true},
		{"metadata ignored", "=2.3.1", "2.3.1+build.9", true},
		{"prerelease excluded", ">=2.0.0 <3.0.0", "2.4.0-beta.1", false},
		{"prerelease explicit", ">=2.0.0-0 <3.0.0", "2.4.0-beta.1", true},
		{"prerelease branch isolation", ">=3.0.0-beta.1 || >=2.0.0 <3.0.0", "2.4.0-beta.1", false},
		{"prerelease numeric", ">=2.0.0-beta.2 <2.0.0", "2.0.0-beta.10", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := compatibilityFixture(t, false)
			manifest.Compatibility.PlatformAPI = tc.constraint
			result, err := EvaluateCompatibility(manifest, CompatibilityTarget{PlatformAPI: tc.version})
			if err != nil || result.Compatible != tc.compatible {
				t.Fatalf("constraint=%q version=%q: %+v %v", tc.constraint, tc.version, result, err)
			}
			if !tc.compatible && (len(result.Issues) != 1 || result.Issues[0] != PlatformAPIMismatch) {
				t.Fatalf("wrong mismatch: %+v", result)
			}
		})
	}
}

func TestCompatibilityManagedRequiresCurrentRunnerContract(t *testing.T) {
	cases := []struct {
		name   string
		change func(*CompatibilityTarget)
		issues []CompatibilityIssue
	}{
		{"ready", func(*CompatibilityTarget) {}, nil},
		{"absent", func(t *CompatibilityTarget) { t.Runner = nil }, []CompatibilityIssue{RunnerRequired}},
		{"runner version", func(t *CompatibilityTarget) { t.Runner.Protocol = "2.0.0" }, []CompatibilityIssue{RunnerProtocolMismatch}},
		{"architecture", func(t *CompatibilityTarget) { t.Runner.Platform = "linux/ppc64le" }, []CompatibilityIssue{RuntimePlatformMismatch}},
		{"backend protocol", func(t *CompatibilityTarget) { t.Runner.BackendProtocol = "portal.backend/2" }, []CompatibilityIssue{BackendProtocolMismatch}},
		{"all mismatches", func(t *CompatibilityTarget) {
			t.PlatformAPI = "3.0.0"
			t.Runner.Protocol = "2.0.0"
			t.Runner.Platform = "windows/amd64"
			t.Runner.BackendProtocol = "portal.backend/2"
		}, []CompatibilityIssue{PlatformAPIMismatch, RunnerProtocolMismatch, RuntimePlatformMismatch, BackendProtocolMismatch}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := compatibilityTarget()
			tc.change(&target)
			result, err := EvaluateCompatibility(compatibilityFixture(t, true), target)
			if err != nil || result.Compatible != (len(tc.issues) == 0) || len(result.Issues) != len(tc.issues) {
				t.Fatalf("%+v %v", result, err)
			}
			for i, issue := range tc.issues {
				if result.Issues[i] != issue {
					t.Fatalf("wrong issue order: %+v", result)
				}
			}
		})
	}
	manifest := compatibilityFixture(t, true)
	manifest.Backend.Platforms = []string{"linux/arm64"}
	target := compatibilityTarget()
	target.Runner.Platform = "linux/arm64"
	result, err := EvaluateCompatibility(manifest, target)
	if err != nil || !result.Compatible {
		t.Fatalf("declared arm64 refused: %+v %v", result, err)
	}
}

func TestCompatibilityRefusesInvalidDeclarationsAndTargets(t *testing.T) {
	for _, constraint := range []string{"", " ", ">=", "><2.0.0", "banana", ">=2.0.0 ||", "|| >=2.0.0", ">=2.0.0 || || <3.0.0", strings.Repeat("*", 97), "*\n", "2.0.0\x00"} {
		for _, managed := range []bool{false, true} {
			manifest := compatibilityFixture(t, managed)
			manifest.Compatibility.PlatformAPI = constraint
			result, err := EvaluateCompatibility(manifest, compatibilityTarget())
			if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
				t.Errorf("invalid range %q accepted: %+v %v", constraint, result, err)
			}
			manifest = compatibilityFixture(t, managed)
			manifest.Compatibility.RunnerProtocol = constraint
			result, err = EvaluateCompatibility(manifest, compatibilityTarget())
			if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
				t.Errorf("invalid runner declaration %q accepted: %+v %v", constraint, result, err)
			}
		}
	}
	for _, version := range []string{"", "2", "2.0", "v2.0.0", "02.0.0", "2.0.0-beta.01", " 2.0.0", "2.0.0\n", "18446744073709551616.0.0", strings.Repeat("1", 129)} {
		target := compatibilityTarget()
		target.PlatformAPI = version
		result, err := EvaluateCompatibility(compatibilityFixture(t, true), target)
		if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
			t.Errorf("invalid host version %q accepted: %+v %v", version, result, err)
		}
		target = compatibilityTarget()
		target.Runner.Protocol = version
		result, err = EvaluateCompatibility(compatibilityFixture(t, true), target)
		if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
			t.Errorf("invalid runner version %q accepted: %+v %v", version, result, err)
		}
	}
	manifest := compatibilityFixture(t, false)
	manifest.Format = "unsupported"
	result, err := EvaluateCompatibility(manifest, compatibilityTarget())
	if !errors.Is(err, ErrInvalidDescriptor) || result.Compatible {
		t.Fatalf("invalid declaration admitted: %+v %v", result, err)
	}
	for _, change := range []func(*RunnerCompatibility){
		func(r *RunnerCompatibility) { r.Platform = "" },
		func(r *RunnerCompatibility) { r.Platform = "linux/amd64\n" },
		func(r *RunnerCompatibility) { r.BackendProtocol = "" },
		func(r *RunnerCompatibility) { r.BackendProtocol = "portal.backend/1\n" },
	} {
		target := compatibilityTarget()
		change(target.Runner)
		result, err := EvaluateCompatibility(compatibilityFixture(t, true), target)
		if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
			t.Errorf("invalid runner target accepted: %+v %v", result, err)
		}
	}
}

func TestCompatibilityDoesNotInterpretCalVerOrMutateInputs(t *testing.T) {
	manifest := compatibilityFixture(t, true)
	target := compatibilityTarget()
	expectedRunner := *target.Runner
	expectedVersion := manifest.Version
	for range 3 {
		result, err := EvaluateCompatibility(manifest, target)
		if err != nil || !result.Compatible {
			t.Fatalf("%+v %v", result, err)
		}
		result.Issues = append(result.Issues, RunnerRequired)
	}
	if *target.Runner != expectedRunner || manifest.Version != expectedVersion {
		t.Fatal("compatibility changed its input")
	}
	target.PlatformAPI = "2026.43.7"
	result, err := EvaluateCompatibility(manifest, target)
	if err != nil || result.Compatible || len(result.Issues) != 1 || result.Issues[0] != PlatformAPIMismatch {
		t.Fatalf("product version treated as platform API: %+v %v", result, err)
	}
	manifest.Version = "18446744073709551616.0.0"
	result, err = EvaluateCompatibility(manifest, compatibilityTarget())
	if !errors.Is(err, ErrInvalidCompatibility) || result.Compatible {
		t.Fatalf("overflowing release accepted: %+v %v", result, err)
	}
}

func FuzzCompatibilityRefusesInvalidInputs(f *testing.F) {
	for _, input := range []string{">=2.0.0 <3.0.0", "*", ">=2.0.0 ||", ">=2.0.0-0", "2.0.0\x00"} {
		f.Add(input, input)
	}
	f.Fuzz(func(t *testing.T, constraint, version string) {
		manifest := compatibilityFixture(t, false)
		manifest.Compatibility.PlatformAPI = constraint
		target := CompatibilityTarget{PlatformAPI: version}
		result, err := EvaluateCompatibility(manifest, target)
		if err != nil && (result.Compatible || len(result.Issues) != 0) {
			t.Fatal("invalid input produced partial or compatible result")
		}
		if err == nil && result.Compatible != (len(result.Issues) == 0) {
			t.Fatal("inconsistent compatibility result")
		}
		if len(constraint) > 96 || len(version) > 128 {
			if err == nil {
				t.Fatal("oversized compatibility input accepted")
			}
		}
	})
}

func TestCompatibilityStaticIgnoresIrrelevantRunnerObservation(t *testing.T) {
	target := CompatibilityTarget{PlatformAPI: "2.3.1", Runner: &RunnerCompatibility{}}
	result, err := EvaluateCompatibility(compatibilityFixture(t, false), target)
	if err != nil || !result.Compatible {
		t.Fatalf("static app coupled to irrelevant runner observation: %+v %v", result, err)
	}
}
