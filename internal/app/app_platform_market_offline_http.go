package app

import (
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
	store, err := s.platformImages()
	if err != nil {
		platformManagementError(w, err)
		return
	}
	// The body streams to a staging file under the package bound; it is never held in memory.
	file, size, key, err := store.streamUpload(
		http.MaxBytesReader(w, r.Body, appplatform.MaxManagedOfflineEnvelopeBytes),
		appplatform.MaxManagedOfflineEnvelopeBytes)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	defer file.Close()
	staged, err := s.stagePlatformPackageFile(r.Context(), actor, file, size, nil)
	if err != nil {
		store.discardPartial(key)
		platformManagementError(w, err)
		return
	}
	// A staged package stays in the cache under its digest until the least-recently-used rule needs the room.
	_, _, _ = store.finishPartial(key)
	writeJSON(w, map[string]any{
		"release_sha256":  staged.release.sha256,
		"app_id":          staged.release.appID,
		"publisher_id":    staged.release.publisherID,
		"trust_revision":  strconv.FormatInt(staged.release.trustRevision, 10),
		"reused":          staged.release.reused,
		"envelope_format": staged.inspection.Format,
		"execution":       staged.inspection.Execution,
		"image_blobs":     staged.inspection.ImageBlobs,
	})
}
