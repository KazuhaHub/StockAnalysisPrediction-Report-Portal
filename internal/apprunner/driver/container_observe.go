package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
)

type containerInspection struct {
	ID                 string `json:"Id"`
	Name, Image        string
	Config, HostConfig json.RawMessage
	Mounts             []struct {
		Type, Source, Destination, Propagation string
		RW                                     bool
	}
	State json.RawMessage
}
type containerStateInspection struct {
	Status                            string
	Running, Paused, Restarting, Dead bool
	ExitCode                          int
}
type containerConfigInspection struct {
	Image, User, Hostname, WorkingDir, StopSignal                                       string
	StopTimeout                                                                         int
	NetworkDisabled, AttachStdin, AttachStdout, AttachStderr, OpenStdin, StdinOnce, Tty bool
	Env                                                                                 []string
	Entrypoint, Cmd                                                                     []string
	Labels                                                                              map[string]string
	Healthcheck                                                                         struct{ Test []string }
}

type containerForbiddenOptions struct {
	CapAdd, Binds, Devices, DeviceCgroupRules, DeviceRequests, VolumesFrom, Links json.RawMessage
	PortBindings, ExtraHosts, Dns, DnsOptions, DnsSearch, GroupAdd                json.RawMessage
	Sysctls, StorageOpt, Annotations, PublishAllPorts, OomKillDisable             json.RawMessage
	CgroupParent, ContainerIDFile, VolumeDriver, Isolation, Runtime               json.RawMessage
}

// A result here is only a fresh observation of an exact native resource. It is
// not a grant to start it or acknowledgement of a journalled effect.
func inspectCandidateContainerHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) (string, error) {
	state, err := inspectContainerStateHTTP(ctx, client, nativeID, plan)
	return state.Status, err
}

// inspectContainerStateHTTP is the same exact-ownership inspection, returning the whole state so a
// one-shot job's exit code can be read.
func inspectContainerStateHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) (containerStateInspection, error) {
	state, err := inspectContainerState(ctx, client, nativeID, plan)
	if err != nil {
		return containerStateInspection{}, err
	}
	return state, nil
}

func inspectContainerState(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) (containerStateInspection, error) {
	none := containerStateInspection{}
	if err := ctx.Err(); err != nil {
		return none, err
	}
	if !containerSHA.MatchString(nativeID) || !validContainerPlan(plan) {
		return none, ErrInvalidResponse
	}
	var expected containerCreate
	if json.Unmarshal(plan.body, &expected) != nil {
		return none, ErrInvalidResponse
	}
	var actual containerInspection
	if err := readJSON(ctx, client, "/v"+APIVersion+"/containers/"+nativeID+"/json", &actual); err != nil {
		return none, errors.Join(ErrContainerUncertain, err)
	}
	if actual.ID != nativeID || actual.Name != "/"+plan.name || actual.Image != expected.Image || !validProjection(actual.Config, &containerConfigInspection{}) || !validProjection(actual.HostConfig, &containerHostConfig{}) || !validProjection(actual.HostConfig, &containerForbiddenOptions{}) {
		return none, ErrContainerConflict
	}
	var config containerConfigInspection
	var host containerHostConfig
	if json.Unmarshal(actual.Config, &config) != nil || json.Unmarshal(actual.HostConfig, &host) != nil {
		return none, ErrContainerConflict
	}
	if config.Image != expected.Image || config.User != expected.User || config.Hostname != expected.Hostname || config.WorkingDir != expected.WorkingDir || config.StopSignal != expected.StopSignal || config.StopTimeout != expected.StopTimeout || config.NetworkDisabled != expected.NetworkDisabled || config.AttachStdin || config.AttachStdout || config.AttachStderr || config.OpenStdin || config.StdinOnce || config.Tty || !reflect.DeepEqual(config.Healthcheck.Test, expected.Healthcheck.Test) || !reflect.DeepEqual(host, expected.HostConfig) {
		return none, ErrContainerConflict
	}
	if len(expected.Entrypoint) > 0 && (!reflect.DeepEqual(config.Entrypoint, expected.Entrypoint) || len(config.Cmd) != len(expected.Cmd) || (len(expected.Cmd) > 0 && !reflect.DeepEqual(config.Cmd, expected.Cmd))) {
		return none, ErrContainerConflict
	}
	for label, value := range expected.Labels {
		if config.Labels[label] != value {
			return none, ErrContainerConflict
		}
	}
	for label := range config.Labels {
		if strings.HasPrefix(label, containerLabelPrefix) {
			if _, ok := expected.Labels[label]; !ok {
				return none, ErrContainerConflict
			}
		}
	}
	for _, env := range expected.Env {
		key, _, _ := strings.Cut(env, "=")
		found := 0
		for _, observed := range config.Env {
			if strings.HasPrefix(observed, key+"=") {
				if observed != env {
					return none, ErrContainerConflict
				}
				found++
			}
		}
		if found != 1 {
			return none, ErrContainerConflict
		}
	}
	// Inspect options that are intentionally absent from the create policy and
	// could otherwise add host authority while the projected policy still matches.
	var options map[string]json.RawMessage
	if json.Unmarshal(actual.HostConfig, &options) != nil {
		return none, ErrContainerConflict
	}
	for _, name := range []string{"CapAdd", "Binds", "Devices", "DeviceCgroupRules", "DeviceRequests", "VolumesFrom", "Links", "PortBindings", "ExtraHosts", "Dns", "DnsOptions", "DnsSearch", "GroupAdd", "Sysctls", "StorageOpt", "Annotations"} {
		var value any
		if raw, ok := options[name]; ok {
			if json.Unmarshal(raw, &value) != nil {
				return none, ErrContainerConflict
			}
			switch v := value.(type) {
			case nil:
			case []any:
				if len(v) > 0 {
					return none, ErrContainerConflict
				}
			case map[string]any:
				if len(v) > 0 {
					return none, ErrContainerConflict
				}
			default:
				return none, ErrContainerConflict
			}
		}
	}
	for _, name := range []string{"PublishAllPorts", "OomKillDisable"} {
		if raw, ok := options[name]; ok && string(raw) != "false" && string(raw) != "null" {
			return none, ErrContainerConflict
		}
	}
	for _, name := range []string{"CgroupParent", "ContainerIDFile", "VolumeDriver", "Isolation"} {
		if raw, ok := options[name]; ok && string(raw) != `""` {
			return none, ErrContainerConflict
		}
	}
	if raw, ok := options["Runtime"]; ok && string(raw) != `"runc"` && string(raw) != `""` {
		return none, ErrContainerConflict
	}
	if len(actual.Mounts) != len(expected.HostConfig.Mounts) {
		return none, ErrContainerConflict
	}
	for _, mount := range expected.HostConfig.Mounts {
		matched := 0
		for _, observed := range actual.Mounts {
			if observed.Destination == mount.Target {
				if observed.Type != mount.Type || observed.Source != mount.Source || observed.RW == mount.ReadOnly || observed.Propagation != "rprivate" {
					return none, ErrContainerConflict
				}
				matched++
			}
		}
		if matched != 1 {
			return none, ErrContainerConflict
		}
	}
	var state containerStateInspection
	if !validProjection(actual.State, &state) || json.Unmarshal(actual.State, &state) != nil {
		return none, ErrContainerConflict
	}
	if state.Paused || state.Restarting || state.Dead {
		return none, ErrContainerUncertain
	}
	if (state.Status == "created" || state.Status == "exited") && !state.Running || state.Status == "running" && state.Running {
		if err := ctx.Err(); err != nil {
			return none, errors.Join(ErrContainerUncertain, err)
		}
		return state, nil
	}
	return none, ErrContainerUncertain
}

// Removal is non-forced and preserves volumes. It inspects exact native identity,
// ownership and policy first; it never deletes a running or unknown container.
// Callers reach it only through signed managed-removal journal commands.
func removeCandidateContainerHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) error {
	state, err := inspectCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return err
	}
	if state != "created" && state != "exited" {
		return ErrContainerConflict
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/"+nativeID+"?force=false&v=false", nil)
	if err != nil {
		return ErrInvalidResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	// Drain only a bounded amount; daemon error text never escapes diagnostics.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return ErrContainerUncertain
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return nil
}
