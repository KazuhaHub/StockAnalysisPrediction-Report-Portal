package app

import (
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Fails open if a managed package is staged before Portal stores its blobs.
func TestPlatformOfflineRefusesManagedPackageUntilBlobsAreStored(t *testing.T) {
	managed := appplatform.OfflineEnvelope{Inspection: appplatform.OfflineEnvelopeInspection{Execution: "managed"}}
	if err := platformOfflineStageable(managed); !errors.Is(err, appplatform.ErrManagedOfflineNotReady) {
		t.Fatalf("managed package staged: %v", err)
	}
	static := appplatform.OfflineEnvelope{Inspection: appplatform.OfflineEnvelopeInspection{Execution: "static"}}
	if err := platformOfflineStageable(static); err != nil {
		t.Fatal(err)
	}
}
