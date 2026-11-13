package driver

import (
	"context"
	"errors"
	"net/http"
)

// ContainerLimits are the memory and CPU limits a live container was created with: the declared resources of
// the release it runs. Found is false when the daemon has no such container.
type ContainerLimits struct {
	MemoryBytes, NanoCPUs int64
	Found                 bool
}

// readContainerLimits reads one container by its native id. The reply must name the same container.
func readContainerLimits(ctx context.Context, client *http.Client, nativeID string) (ContainerLimits, error) {
	if !containerSHA.MatchString(nativeID) {
		return ContainerLimits{}, ErrInvalidResponse
	}
	var actual struct {
		ID         string `json:"Id"`
		HostConfig struct{ Memory, NanoCpus int64 }
	}
	err := readJSONOrMissing(ctx, client, "/v"+APIVersion+"/containers/"+nativeID+"/json", &actual, errContainerAbsent)
	if errors.Is(err, errContainerAbsent) {
		return ContainerLimits{}, nil
	}
	if err != nil {
		return ContainerLimits{}, err
	}
	if actual.ID != nativeID || actual.HostConfig.Memory < 0 || actual.HostConfig.NanoCpus < 0 {
		return ContainerLimits{}, ErrInvalidResponse
	}
	return ContainerLimits{MemoryBytes: actual.HostConfig.Memory, NanoCPUs: actual.HostConfig.NanoCpus, Found: true}, nil
}

var errContainerAbsent = errors.New("container absent")
