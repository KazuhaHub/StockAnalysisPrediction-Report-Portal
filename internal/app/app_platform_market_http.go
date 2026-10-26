package app

import (
	"net/http"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) apiPlatformMarketCatalogue(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "catalogue_url")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	address, err := platformManagementString(fields, "catalogue_url")
	if err != nil || appplatform.ValidateCatalogueURL(address) != nil {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	actor, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		_, err := platformRefreshActor(e, actor, facts, control)
		return err
	}); err != nil {
		platformManagementError(w, err)
		return
	}
	releaseSlot, ok := s.platformManagementSlot(w)
	if !ok {
		return
	}
	defer releaseSlot()
	client := newPlatformMarketClient()
	defer client.close()
	raw, err := client.fetch(r.Context(), address, appplatform.MaxCatalogueBytes)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	catalogue, err := appplatform.ParseCatalogue(raw)
	if err != nil {
		platformManagementError(w, errPlatformMarketFetch)
		return
	}
	// Recheck authority after network IO before publishing discovery hints.
	if err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		_, err := platformRefreshActor(e, actor, facts, control)
		return err
	}); err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"catalogue_sha256": catalogue.SHA256, "releases": catalogue.Releases, "publisher_verified": false})
}

func (s *Server) apiPlatformMarketStage(w http.ResponseWriter, r *http.Request, user string) {
	fields, err := platformManagementRead(w, r, "catalogue_url", "release_sha256")
	if err != nil {
		platformManagementError(w, err)
		return
	}
	address, addressErr := platformManagementString(fields, "catalogue_url")
	digest, digestErr := platformManagementString(fields, "release_sha256")
	if addressErr != nil || digestErr != nil || appplatform.ValidateCatalogueURL(address) != nil || !platformValidSHA(digest) {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
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
	client := newPlatformMarketClient()
	defer client.close()
	result, err := s.fetchAndStagePlatformMarketRelease(r.Context(), actor, address, digest, client)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, map[string]any{"release_sha256": result.sha256, "app_id": result.appID, "publisher_id": result.publisherID, "trust_revision": strconv.FormatInt(result.trustRevision, 10), "reused": result.reused})
}
