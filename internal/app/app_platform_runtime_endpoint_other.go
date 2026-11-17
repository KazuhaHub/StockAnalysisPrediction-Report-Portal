//go:build !linux

package app

import "context"

func platformObserveRuntimeEndpoint(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
	return platformRuntimeObservation{}, errAppPlatformRuntimeConflict
}
