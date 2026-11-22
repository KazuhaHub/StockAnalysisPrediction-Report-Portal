package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) apiPlatformMarketCatalogue(w http.ResponseWriter, r *http.Request, user string) {
	client := newPlatformMarketClient()
	defer client.close()
	s.apiPlatformMarketCatalogueClient(w, r, user, client)
}

func (s *Server) apiPlatformMarketCatalogueClient(w http.ResponseWriter, r *http.Request, user string, client *platformMarketClient) {
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
	client := newPlatformMarketClient()
	defer client.close()
	s.apiPlatformMarketStageClient(w, r, user, client)
}

func (s *Server) apiPlatformMarketStageClient(w http.ResponseWriter, r *http.Request, user string, client *platformMarketClient) {
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
	var once sync.Once
	release := func() { once.Do(releaseSlot) }
	defer release()
	staged, pkg, err := s.resolvePlatformMarketRelease(r.Context(), actor, address, digest, client)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	if pkg == nil {
		writeJSON(w, platformStagedBody(staged))
		return
	}
	// The package comes down in the background, detached from this request: the slot is not held while it does.
	release()
	store, err := s.platformImages()
	if err != nil {
		platformManagementError(w, err)
		return
	}
	job, err := s.platformStageJobs.start(platformPackageKey(pkg.entry), digest, store, func(ctx context.Context) (platformStagedRelease, error) {
		defer client.close()
		// The verification and storage step takes a slot of its own, so a long download does not hold one.
		admit := func() (func(), error) {
			slot, ok := s.platformDocuments.acquire()
			if !ok {
				return nil, errPlatformManagementBusy
			}
			return slot, nil
		}
		return s.fetchAndStagePlatformPackageAdmitted(ctx, actor, client, pkg.entry, pkg.descriptor, pkg.signature, admit)
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	timer := time.NewTimer(platformMarketStageWait)
	defer timer.Stop()
	select {
	case <-job.done:
		if v, ok := s.platformStageJobs.view(digest, false); ok && v.job == job && !v.running {
			if v.err != nil {
				s.platformStageJobs.forget(job)
				platformManagementError(w, v.err)
				return
			}
			writeJSON(w, platformStagedBody(v.result))
			return
		}
	case <-timer.C:
	case <-r.Context().Done():
	}
	writeJSONStatus(w, http.StatusAccepted, job.downloading())
}
