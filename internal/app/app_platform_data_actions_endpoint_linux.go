//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunDataQuery(ctx context.Context, endpoint platformRuntimeEndpoint, request platformDataQueryRequest) (transport.WritableLifecycleObservation, error) {
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.DataQueryAnswer, request.raw, request.signature, request.authority, request.descriptor, nil)
}

func platformExecuteSetDataQuota(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSetDataQuotaIntent) (transport.WritableLifecycleObservation, error) {
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.SetDataQuotaExecute, intent.raw, intent.signature, intent.authority, intent.descriptor, nil)
}

func platformReadSetDataQuota(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformSetDataQuotaIntent) (transport.WritableLifecycleObservation, error) {
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.SetDataQuotaRead, intent.raw, intent.readSignature, intent.authority, intent.descriptor, nil)
}

func platformExecuteImportData(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportDataIntent) (transport.WritableLifecycleObservation, error) {
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.ImportDataExecute, intent.raw, intent.signature, intent.authority, intent.descriptor, nil)
}

func platformReadImportData(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportDataIntent) (transport.WritableLifecycleObservation, error) {
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.ImportDataRead, intent.raw, intent.readSignature, intent.authority, intent.descriptor, nil)
}
