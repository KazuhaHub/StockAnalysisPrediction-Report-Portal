package app

import (
	"io"
	"net/http"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) apiPlatformMarketOffline(
	w http.ResponseWriter,
	r *http.Request,
	user string,
) {
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	releaseSlot, ok := s.platformManagementSlot(w)
	if !ok {
		return
	}
	defer releaseSlot()
	raw, err := io.ReadAll(http.MaxBytesReader(
		w, r.Body, appplatform.MaxOfflineEnvelopeBytes))
	if err != nil {
		platformManagementError(w, appplatform.ErrInvalidOfflineEnvelope)
		return
	}
	envelope, err := appplatform.InspectOfflineEnvelope(raw)
	if err == nil {
		err = platformOfflineStageable(envelope)
	}
	if err != nil {
		platformManagementError(w, err)
		return
	}
	result, err := s.stagePlatformReleaseContext(
		r.Context(), actor, envelope.Descriptor,
		envelope.Signature, envelope.Frontend)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"release_sha256":  result.sha256,
		"app_id":          result.appID,
		"publisher_id":    result.publisherID,
		"trust_revision":  strconv.FormatInt(result.trustRevision, 10),
		"reused":          result.reused,
		"envelope_format": envelope.Inspection.Format,
		"execution":       envelope.Inspection.Execution,
	})
}

// platformOfflineStageable refuses a managed package until Portal stores its
// image blobs: staging only the descriptor would create a release whose image
// can never reach a runner.
func platformOfflineStageable(envelope appplatform.OfflineEnvelope) error {
	if envelope.Inspection.Execution == "managed" {
		return appplatform.ErrManagedOfflineNotReady
	}
	return nil
}
