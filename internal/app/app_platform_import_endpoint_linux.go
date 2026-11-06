//go:build linux

package app

import "context"

// The runner's import-checkpoint actions (execute and read-receipt) are reached through the writable
// lifecycle transport. Until that transport is wired to this seam the endpoint refuses, so a Portal
// can never report an import it did not perform.
func platformExecuteImportCheckpoint(context.Context, platformRuntimeEndpoint, platformImportIntent) (platformImportObservation, error) {
	return nil, errAppPlatformRuntimeConflict
}

func platformReadImportCheckpointReceipt(context.Context, platformRuntimeEndpoint, platformImportIntent) (platformImportObservation, error) {
	return nil, errAppPlatformRuntimeConflict
}
