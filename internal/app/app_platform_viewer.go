package app

import (
	"context"
	"encoding/base64"
	"strings"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Private host read boundary. There is no browser identity input or bearer token.
// Return facts only after the complete launch gate and its final session/trust/
// epoch recheck commit. Managed apps remain denied by that gate for now.
func (s *Server) readPlatformLaunchViewerContext(ctx context.Context, session platformActorSession, installationID string) (protocol.Viewer, error) {
	var result protocol.Viewer
	_, err := s.withPlatformLaunchAccountReadContext(ctx, session, installationID, func(e migExec, binding platformLaunchAccess, manifest appplatform.Manifest, account platformActorAccount) error {
		var err error
		result, err = s.platformViewerIdentity(e, account, binding.installationID, manifest.ID)
		return err
	})
	if err != nil {
		return protocol.Viewer{}, err
	}
	return result, nil
}

// Caller holds current account, OU/principal and installation authority in one
// transaction. Facts alone authorize no backend route or platform capability.
func (s *Server) platformViewerIdentity(e migExec, account platformActorAccount, installationID, appID string) (protocol.Viewer, error) {
	var zero protocol.Viewer
	chain, err := platformAudienceChain(e, account)
	if err != nil {
		return zero, err
	}
	ous := make([]appplatform.ViewerOUIdentity, 0, len(chain))
	for _, ou := range chain {
		ous = append(ous, appplatform.ViewerOUIdentity{ID: ou.id, Created: ou.created})
	}
	// A reader must never lazily create/repair the keyring. Read the durable pair
	// from this transaction rather than trusting a previously cached data key.
	stored, err := readStoredKeyring(e)
	if err != nil || !stored.exists {
		return zero, errAppPlatformLaunchUnavailable
	}
	salt, err := base64.StdEncoding.DecodeString(stored.salt)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	kek, err := s.kek(salt)
	defer clear(kek)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	// openStoredKeyring can rewrap under a previous configured key. This read
	// boundary intentionally does not invoke that mutating recovery path.
	dek, err := aesOpen(kek, stored.wrapped, []byte("dek"))
	defer clear(dek)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	var sealed string
	if err := e.queryRow(`SELECT sealed_identity_seed FROM app_platform_installations WHERE id=? AND app_id=?`, installationID, appID).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, sealPrefix) {
		return zero, errAppPlatformLaunchUnavailable
	}
	seed, err := aesOpen(dek, strings.TrimPrefix(sealed, sealPrefix), aad("app-platform", installationID))
	defer clear(seed)
	if err != nil {
		return zero, errAppPlatformLaunchUnavailable
	}
	return appplatform.DeriveViewerIdentity(appID, installationID, seed, appplatform.ViewerAccountIdentity{Name: account.username, Created: account.created, Generation: account.generation, SessionRevision: account.sessionRevision, Role: account.role}, ous)
}
