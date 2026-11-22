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
	var first appplatform.PublisherTrust
	if err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, current appplatform.PublisherTrust) error {
		_, err := platformRefreshActor(e, actor, facts, control)
		first = current
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
	apps := platformMarketGroupApps(catalogue.Releases)
	previews := platformMarketFetchPreviews(r.Context(), client, apps, platformMarketTrustedPublishers(first))
	// Recheck authority after network IO before publishing discovery hints. The trust read here is also what
	// the previews are verified against, so a display never outlives a revocation.
	var trust appplatform.PublisherTrust
	var now int64
	if err = s.withPlatformTrustActorContext(r.Context(), actor, func(e migExec, facts platformActorFacts, control platformActorControl, current appplatform.PublisherTrust) error {
		refreshed, err := platformRefreshActor(e, actor, facts, control)
		trust, now = current, refreshed.nowMS
		return err
	}); err != nil {
		platformManagementError(w, err)
		return
	}
	platformMarketApplyTrust(apps, previews, trust, now)
	writeJSON(w, map[string]any{"catalogue_sha256": catalogue.SHA256, "releases": catalogue.Releases, "apps": apps, "publisher_verified": false})
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
	job, err := s.platformStageJobs.start(platformPackageKey(pkg.entry), digest, actor, store, func(ctx context.Context, job *platformMarketStageJob) (platformStagedRelease, error) {
		defer client.close()
		// The verification and storage step waits for a slot of its own, so a long download does not hold one and
		// a finished one is never thrown away for want of one. It runs as the first requester still authorised.
		admit := func(ctx context.Context) (platformActorSession, func(), error) {
			slot, err := s.platformDocumentsWait(ctx)
			if err != nil {
				return platformActorSession{}, nil, err
			}
			var last error = errAppPlatformActorUnavailable
			for _, who := range s.platformStageJobs.requesters(job) {
				if last = s.withPlatformTrustActorContext(ctx, who, func(migExec, platformActorFacts, platformActorControl, appplatform.PublisherTrust) error { return nil }); last == nil {
					return who, slot, nil
				}
			}
			slot()
			return platformActorSession{}, nil, last
		}
		return s.fetchAndStagePlatformPackageAdmitted(ctx, actor, client, pkg.entry, pkg.descriptor, pkg.signature, admit)
	})
	if err != nil {
		platformManagementError(w, err)
		return
	}
	timer := time.NewTimer(s.platformStageWait())
	defer timer.Stop()
	select {
	case <-job.done:
		if v, ok := s.platformStageJobs.view(digest); ok && v.job == job && !v.running {
			if v.err != nil {
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

// platformDocumentsWait takes a document slot, waiting for one until ctx ends. Only two exist and user routes
// share them, so a background step that needs one must wait rather than fail a finished download.
func (s *Server) platformDocumentsWait(ctx context.Context) (func(), error) {
	for {
		if slot, ok := s.platformDocuments.acquire(); ok {
			return slot, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
