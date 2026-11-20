//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunDataQuery(context.Context, platformRuntimeEndpoint, platformDataQueryRequest) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformExecuteSetDataQuota(context.Context, platformRuntimeEndpoint, platformSetDataQuotaIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadSetDataQuota(context.Context, platformRuntimeEndpoint, platformSetDataQuotaIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformExecuteImportData(context.Context, platformRuntimeEndpoint, platformImportDataIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadImportData(context.Context, platformRuntimeEndpoint, platformImportDataIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
