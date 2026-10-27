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
}
type containerConfigInspection struct {
	Image, User, Hostname, WorkingDir, StopSignal                                       string
	StopTimeout                                                                         int
	NetworkDisabled, AttachStdin, AttachStdout, AttachStderr, OpenStdin, StdinOnce, Tty bool
	Env                                                                                 []string
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !containerSHA.MatchString(nativeID) || !validContainerPlan(plan) {
		return "", ErrInvalidResponse
	}
	var expected containerCreate
	if json.Unmarshal(plan.body, &expected) != nil {
		return "", ErrInvalidResponse
	}
	var actual containerInspection
	if err := readJSON(ctx, client, "/v"+APIVersion+"/containers/"+nativeID+"/json", &actual); err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	if actual.ID != nativeID || actual.Name != "/"+plan.name || actual.Image != expected.Image || !validProjection(actual.Config, &containerConfigInspection{}) || !validProjection(actual.HostConfig, &containerHostConfig{}) || !validProjection(actual.HostConfig, &containerForbiddenOptions{}) {
		return "", ErrContainerConflict
	}
	var config containerConfigInspection
	var host containerHostConfig
	if json.Unmarshal(actual.Config, &config) != nil || json.Unmarshal(actual.HostConfig, &host) != nil {
		return "", ErrContainerConflict
	}
	if config.Image != expected.Image || config.User != expected.User || config.Hostname != expected.Hostname || config.WorkingDir != expected.WorkingDir || config.StopSignal != expected.StopSignal || config.StopTimeout != expected.StopTimeout || config.NetworkDisabled != expected.NetworkDisabled || config.AttachStdin || config.AttachStdout || config.AttachStderr || config.OpenStdin || config.StdinOnce || config.Tty || !reflect.DeepEqual(config.Healthcheck.Test, expected.Healthcheck.Test) || !reflect.DeepEqual(host, expected.HostConfig) {
		return "", ErrContainerConflict
	}
	for label, value := range expected.Labels {
		if config.Labels[label] != value {
			return "", ErrContainerConflict
		}
	}
	for label := range config.Labels {
		if strings.HasPrefix(label, containerLabelPrefix) {
			if _, ok := expected.Labels[label]; !ok {
				return "", ErrContainerConflict
			}
		}
	}
	for _, env := range expected.Env {
		key, _, _ := strings.Cut(env, "=")
		found := 0
		for _, observed := range config.Env {
			if strings.HasPrefix(observed, key+"=") {
				if observed != env {
					return "", ErrContainerConflict
				}
				found++
			}
		}
		if found != 1 {
			return "", ErrContainerConflict
		}
	}
	// Inspect options that are intentionally absent from the create policy and
	// could otherwise add host authority while the projected policy still matches.
	var options map[string]json.RawMessage
	if json.Unmarshal(actual.HostConfig, &options) != nil {
		return "", ErrContainerConflict
	}
	for _, name := range []string{"CapAdd", "Binds", "Devices", "DeviceCgroupRules", "DeviceRequests", "VolumesFrom", "Links", "PortBindings", "ExtraHosts", "Dns", "DnsOptions", "DnsSearch", "GroupAdd", "Sysctls", "StorageOpt", "Annotations"} {
		var value any
		if raw, ok := options[name]; ok {
			if json.Unmarshal(raw, &value) != nil {
				return "", ErrContainerConflict
			}
			switch v := value.(type) {
			case nil:
			case []any:
				if len(v) > 0 {
					return "", ErrContainerConflict
				}
			case map[string]any:
				if len(v) > 0 {
					return "", ErrContainerConflict
				}
			default:
				return "", ErrContainerConflict
			}
		}
	}
	for _, name := range []string{"PublishAllPorts", "OomKillDisable"} {
		if raw, ok := options[name]; ok && string(raw) != "false" && string(raw) != "null" {
			return "", ErrContainerConflict
		}
	}
	for _, name := range []string{"CgroupParent", "ContainerIDFile", "VolumeDriver", "Isolation"} {
		if raw, ok := options[name]; ok && string(raw) != `""` {
			return "", ErrContainerConflict
		}
	}
	if raw, ok := options["Runtime"]; ok && string(raw) != `"runc"` && string(raw) != `""` {
		return "", ErrContainerConflict
	}
	if len(actual.Mounts) != len(expected.HostConfig.Mounts) {
		return "", ErrContainerConflict
	}
	for _, mount := range expected.HostConfig.Mounts {
		matched := 0
		for _, observed := range actual.Mounts {
			if observed.Destination == mount.Target {
				if observed.Type != mount.Type || observed.Source != mount.Source || observed.RW == mount.ReadOnly || observed.Propagation != "rprivate" {
					return "", ErrContainerConflict
				}
				matched++
			}
		}
		if matched != 1 {
			return "", ErrContainerConflict
		}
	}
	var state containerStateInspection
	if !validProjection(actual.State, &state) || json.Unmarshal(actual.State, &state) != nil {
		return "", ErrContainerConflict
	}
	if state.Paused || state.Restarting || state.Dead {
		return "", ErrContainerUncertain
	}
	if (state.Status == "created" || state.Status == "exited") && !state.Running || state.Status == "running" && state.Running {
		if err := ctx.Err(); err != nil {
			return "", errors.Join(ErrContainerUncertain, err)
		}
		return state.Status, nil
	}
	return "", ErrContainerUncertain
}

// Removal is non-forced and preserves volumes. It inspects exact native identity,
// ownership and policy first; it never deletes a running or unknown container.
// Like create, this remains package-private until signed lifecycle integration.
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
