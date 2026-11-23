package driver

import (
	"encoding/json"
	"strings"
)

// SeccompGateReport is the read-only verdict of InspectSeccompGate on one container.
type SeccompGateReport struct {
	// Profile is "v1", "current" or "unknown", by the exact bytes of the container's seccomp option.
	Profile string `json:"profile"`
	// ProfileLabel is the container's seccomp-profile label.
	ProfileLabel string `json:"profile_label"`
	// PolicySHA256 is the container's policy-sha256 label.
	PolicySHA256 string `json:"policy_sha256"`
	// PolicyDigestReproduced says the digest of the create policy rebuilt from the container's own inspected
	// configuration, by this binary's encoder, equals PolicySHA256.
	PolicyDigestReproduced bool `json:"policy_digest_reproduced"`
	// PolicySHA256AfterRepair is the digest of the same policy under the current profile: what a container
	// recreated by a repair carries.
	PolicySHA256AfterRepair string `json:"policy_sha256_after_repair,omitempty"`
	// RecognisedAsV1 is true only when the option bytes are exactly v1's, the label names v1, and the policy
	// digest is reproduced. Only then can the runner select the v1 plan for this container.
	RecognisedAsV1 bool   `json:"recognised_as_v1"`
	Problem        string `json:"problem,omitempty"`
}

// The environment variables a plan sets, in plan order. Docker adds the image's own variables to an inspected
// container, which the plan neither sets nor compares.
var planEnvironmentKeys = []string{
	"PORTAL_APP_HTTP_SOCKET", "PORTAL_APP_BROKER_SOCKET",
	"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "PORTAL_APP_PROXY_LISTEN",
}

// InspectSeccompGate takes the JSON `docker inspect <container>` prints (an array of one object, or the object)
// and reports whether this binary recognises the container's seccomp option and policy-sha256 as the previous
// profile's. It reads nothing else, contacts no daemon and changes nothing.
//
// The policy digest is checked by rebuilding the create policy from what the container itself reports, the way
// the runner's own inspection compares a container with its plan, and hashing it as every plan builder does.
func InspectSeccompGate(inspectJSON []byte) SeccompGateReport {
	report := SeccompGateReport{Profile: ""}
	raw := inspectJSON
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		if len(list) != 1 {
			report.Problem = "expected exactly one inspected container"
			return report
		}
		raw = list[0]
	}
	var doc struct {
		Config struct {
			Image, User, Hostname, WorkingDir, StopSignal string
			StopTimeout                                   int
			NetworkDisabled                               bool
			Env                                           []string
			Entrypoint, Cmd                               []string
			Labels                                        map[string]string
			Healthcheck                                   struct{ Test []string }
		}
		HostConfig containerHostConfig
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Config.Image == "" || doc.Config.Labels == nil {
		report.Problem = "not a docker inspect document of a container"
		return report
	}
	report.Profile = "unknown"
	opts := doc.HostConfig.SecurityOpt
	if len(opts) != 2 || opts[0] != "no-new-privileges:true" {
		report.Problem = "security options are not the managed pair"
		return report
	}
	var matched seccompPolicy
	for i, p := range seccompPolicies {
		if opts[1] == p.option {
			matched = p
			report.Profile = "current"
			if i > 0 {
				report.Profile = "v1"
			}
		}
	}
	report.ProfileLabel = doc.Config.Labels[containerLabelPrefix+"seccomp-profile"]
	report.PolicySHA256 = doc.Config.Labels[containerLabelPrefix+"policy-sha256"]
	if report.Profile == "unknown" {
		report.Problem = "seccomp option is not a profile of the accepted set"
		return report
	}
	if report.ProfileLabel != matched.name {
		report.Problem = "seccomp-profile label does not name the profile of the option"
		return report
	}
	create := containerCreate{
		Image: doc.Config.Image, User: doc.Config.User, Hostname: doc.Config.Hostname, WorkingDir: doc.Config.WorkingDir,
		NetworkDisabled: doc.Config.NetworkDisabled, StopSignal: doc.Config.StopSignal, StopTimeout: doc.Config.StopTimeout,
		Labels: map[string]string{}, HostConfig: doc.HostConfig,
	}
	create.Healthcheck.Test = doc.Config.Healthcheck.Test
	for _, key := range planEnvironmentKeys {
		for _, value := range doc.Config.Env {
			if strings.HasPrefix(value, key+"=") {
				create.Env = append(create.Env, value)
			}
		}
	}
	for name, value := range doc.Config.Labels {
		if strings.HasPrefix(name, containerLabelPrefix) && name != containerLabelPrefix+"policy-sha256" {
			create.Labels[name] = value
		}
	}
	if doc.Config.Labels[containerLabelPrefix+"format"] == "migration/1-dev.1" {
		create.Entrypoint, create.Cmd = doc.Config.Entrypoint, doc.Config.Cmd
	}
	digested, err := json.Marshal(create)
	if err != nil {
		report.Problem = "the create policy cannot be encoded"
		return report
	}
	report.PolicyDigestReproduced = containerHash(digested) == report.PolicySHA256
	if !report.PolicyDigestReproduced {
		report.Problem = "policy-sha256 is not the digest of the policy rebuilt from the container's configuration"
		return report
	}
	create.Labels[containerLabelPrefix+"policy-sha256"] = report.PolicySHA256
	body, err := json.Marshal(create)
	if err != nil {
		report.Problem = "the create policy cannot be encoded"
		return report
	}
	if len(seccompPolicies) > 0 {
		report.PolicySHA256AfterRepair = withSeccomp(containerPlan{body: body}, seccompPolicies[0]).labelPolicySHA256()
	}
	report.RecognisedAsV1 = report.Profile == "v1"
	if !report.RecognisedAsV1 {
		report.Problem = "container is under the current profile, not v1"
	}
	return report
}

// labelPolicySHA256 reads the policy-sha256 label of the plan's body.
func (p containerPlan) labelPolicySHA256() string {
	var request containerCreate
	if json.Unmarshal(p.body, &request) != nil {
		return ""
	}
	return request.Labels[containerLabelPrefix+"policy-sha256"]
}
