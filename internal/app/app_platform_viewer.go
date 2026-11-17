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
// epoch recheck commit. Unsupported/incomplete managed histories stay denied.
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
	dek, err := s.readPlatformDEK(e)
	defer clear(dek)
	if err != nil {
		return zero, err
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

// Never initialize, repair or rewrap keyring rows while admitting a reader.
// The returned key is an owned buffer that the caller must clear.
func (s *Server) readPlatformDEK(e migExec) ([]byte, error) {
	stored, err := readStoredKeyring(e)
	if err != nil || !stored.exists {
		return nil, errAppPlatformLaunchUnavailable
	}
	salt, err := base64.StdEncoding.DecodeString(stored.salt)
	if err != nil {
		return nil, errAppPlatformLaunchUnavailable
	}
	kek, err := s.kek(salt)
	defer clear(kek)
	if err != nil {
		return nil, errAppPlatformLaunchUnavailable
	}
	// openStoredKeyring can rewrap under a previous configured key. This read
	// boundary intentionally does not invoke that mutating recovery path.
	dek, err := aesOpen(kek, stored.wrapped, []byte("dek"))
	if err != nil {
		clear(dek)
		return nil, errAppPlatformLaunchUnavailable
	}
	return dek, nil
}
