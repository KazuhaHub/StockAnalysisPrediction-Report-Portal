package app

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformMarketPreviewApps        = 64
	platformMarketPreviewConcurrency = 4
	platformMarketPreviewBudget      = 10 * time.Second
)

type platformMarketVersion struct {
	Version          string `json:"version"`
	DescriptorSHA256 string `json:"descriptor_sha256"`
}

// platformMarketApp is one application of a catalogue: its versions newest first and, when the newest
// version's signed descriptor verified against current publisher trust, how to show it. It is a listing aid
// only; installing still downloads and verifies the exact release.
type platformMarketApp struct {
	PublisherID      string                  `json:"publisher_id"`
	AppID            string                  `json:"app_id"`
	Versions         []platformMarketVersion `json:"versions"`
	Display          *appplatform.AppDisplay `json:"display,omitempty"`
	PublisherTrusted bool                    `json:"publisher_trusted"`

	newest appplatform.CatalogueRelease
}

// platformMarketGroupApps folds catalogue releases into one entry per (publisher, app), in order of first
// appearance, with versions sorted newest first by SemVer precedence.
func platformMarketGroupApps(releases []appplatform.CatalogueRelease) []*platformMarketApp {
	apps := []*platformMarketApp{}
	index := map[[2]string]*platformMarketApp{}
	for _, release := range releases {
		key := [2]string{release.PublisherID, release.AppID}
		app := index[key]
		if app == nil {
			app = &platformMarketApp{PublisherID: release.PublisherID, AppID: release.AppID, Versions: []platformMarketVersion{}, newest: release}
			index[key] = app
			apps = append(apps, app)
		}
		app.Versions = append(app.Versions, platformMarketVersion{Version: release.Version, DescriptorSHA256: release.DescriptorSHA256})
		if appplatform.CompareVersions(release.Version, app.newest.Version) > 0 {
			app.newest = release
		}
	}
	for _, app := range apps {
		sort.SliceStable(app.Versions, func(i, j int) bool {
			return appplatform.CompareVersions(app.Versions[i].Version, app.Versions[j].Version) > 0
		})
	}
	return apps
}

type platformMarketPreview struct{ descriptor, signature []byte }

// platformMarketFetchPreviews downloads the newest descriptor and signature of each of the first apps, a few at
// a time and within one budget. A failure leaves that app without a preview.
func platformMarketFetchPreviews(ctx context.Context, client *platformMarketClient, apps []*platformMarketApp) map[*platformMarketApp]platformMarketPreview {
	ctx, cancel := context.WithTimeout(ctx, platformMarketPreviewBudget)
	defer cancel()
	if len(apps) > platformMarketPreviewApps {
		apps = apps[:platformMarketPreviewApps]
	}
	var mu sync.Mutex
	previews := map[*platformMarketApp]platformMarketPreview{}
	slots := make(chan struct{}, platformMarketPreviewConcurrency)
	var wg sync.WaitGroup
	for _, app := range apps {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			descriptor, err := client.fetchResumable(ctx, app.newest.DescriptorURL, appplatform.MaxDescriptorBytes, 3)
			if err != nil || platformDigest(descriptor) != app.newest.DescriptorSHA256 {
				return
			}
			signature, err := client.fetchResumable(ctx, app.newest.SignatureURL, appplatform.MaxDescriptorSignatureBytes, 3)
			if err != nil {
				return
			}
			mu.Lock()
			previews[app] = platformMarketPreview{descriptor, signature}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return previews
}

// platformMarketApplyTrust marks each app's publisher as trusted or not and attaches the display block of
// every preview that verifies against the given trust snapshot and names the catalogue entry it came from.
func platformMarketApplyTrust(apps []*platformMarketApp, previews map[*platformMarketApp]platformMarketPreview, trust appplatform.PublisherTrust, nowMS int64) {
	known := map[string]bool{}
	for _, publisher := range trust.Publishers {
		known[publisher.ID] = true
	}
	for _, app := range apps {
		app.PublisherTrusted = known[app.PublisherID]
		preview, ok := previews[app]
		if !ok {
			continue
		}
		verified, err := appplatform.VerifyPublisherDescriptor(preview.descriptor, preview.signature, trust, strconv.FormatInt(nowMS, 10))
		if err != nil {
			continue
		}
		manifest := verified.Descriptor.Manifest
		if manifest.ID != app.AppID || manifest.Publisher != app.PublisherID || manifest.Version != app.newest.Version {
			continue
		}
		app.Display = manifest.Presentation.Display
	}
}
