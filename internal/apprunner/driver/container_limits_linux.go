//go:build linux

package driver

import "context"

// ContainerLimits reads the limits of a live container by native id. It is read-only.
func (r *CandidateRuntime) ContainerLimits(ctx context.Context, nativeID string) (ContainerLimits, error) {
	client, closeClient := r.client()
	defer closeClient()
	return readContainerLimits(ctx, client, nativeID)
}
