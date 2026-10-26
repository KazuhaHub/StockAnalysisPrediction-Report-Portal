package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformReleaseConflict = errors.New("application release bytes or trust changed")

type platformStagedRelease struct {
	sha256, publisherID, appID, verificationKeyID string
	trustRevision, keyRevision                    int64
	reused                                        bool
}

// This stages immutable declaration/frontend bytes, not an admitted installation
// or a verified OCI image. It never creates an operation or activates app routes.
func (s *Server) stagePlatformRelease(session platformActorSession, descriptor, signature, frontend []byte) (platformStagedRelease, error) {
	if len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(signature) == 0 || len(signature) > appplatform.MaxDescriptorSignatureBytes || len(frontend) == 0 || len(frontend) > appplatform.MaxFrontendArchiveBytes {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	raw, envelope, archive := bytes.Clone(descriptor), bytes.Clone(signature), bytes.Clone(frontend)
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	inspected, err := appplatform.InspectFrontend(parsed.Manifest, archive)
	if err != nil {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	var result platformStagedRelease
	err = s.withPlatformTrustActor(session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) (resultErr error) {
		// All bytes/audits written by this callback roll back if authority expires
		// while waiting for a later lock or doing bounded archive inspection.
		defer func() {
			if resultErr == nil {
				resultErr = platformFinalStoredReleaseTrust(e, session, actor, control, result.sha256, result.trustRevision, trust)
			}
		}()
		verified, err := appplatform.VerifyPublisherDescriptor(raw, envelope, trust, strconv.FormatInt(control.nowMS, 10))
		if err != nil {
			return errAppPlatformReleaseConflict
		}
		result = platformStagedIdentity(verified)
		var existingSHA string
		err = e.queryRow(`SELECT sha256 FROM app_platform_releases WHERE publisher_id=? AND app_id=? AND version=?`, parsed.Manifest.Publisher, parsed.Manifest.ID, parsed.Manifest.Version).Scan(&existingSHA)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if existingSHA != result.sha256 {
				return errAppPlatformReleaseConflict
			}
			existing, err := platformVerifyStoredRelease(e, existingSHA, result.trustRevision, trust, control.nowMS)
			if err != nil {
				return err
			}
			var originalDescriptor, originalSignature []byte
			if err := e.queryRow(`SELECT descriptor,signature FROM app_platform_releases WHERE sha256=?`, existingSHA).Scan(&originalDescriptor, &originalSignature); err != nil {
				return err
			}
			if !bytes.Equal(originalDescriptor, raw) || !bytes.Equal(originalSignature, envelope) {
				return errAppPlatformReleaseConflict
			}
			existing.reused = true
			result = existing
			return nil
		}
		var content []byte
		var size int64
		err = e.queryRow(`SELECT content,archive_bytes FROM app_platform_frontend_artifacts WHERE sha256=?`, inspected.SHA256).Scan(&content, &size)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = e.exec(`INSERT INTO app_platform_frontend_artifacts(sha256,archive_bytes,content,created_ms) VALUES(?,?,?,?)`, inspected.SHA256, inspected.ArchiveBytes, archive, control.nowMS); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if size != inspected.ArchiveBytes || !bytes.Equal(content, archive) {
			return errAppPlatformReleaseConflict
		}
		if _, err = e.exec(`INSERT INTO app_platform_releases(sha256,app_id,publisher_id,version,descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,managed,created_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, result.sha256, parsed.Manifest.ID, parsed.Manifest.Publisher, parsed.Manifest.Version, raw, envelope, verified.KeyID, result.trustRevision, control.nowMS, inspected.SHA256, boolInt(parsed.Manifest.Backend != nil), control.nowMS); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Release       string `json:"release_sha256"`
			TrustRevision int64  `json:"trust_revision"`
		}{result.sha256, result.trustRevision})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.release_staged','app_platform_release',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, result.sha256, string(detail))
		return err
	})
	if err != nil {
		return platformStagedRelease{}, err
	}
	return result, nil
}

func platformStagedIdentity(verified appplatform.VerifiedPublisherDescriptor) platformStagedRelease {
	trustRevision, _ := strconv.ParseInt(verified.PublisherRevision, 10, 64)
	keyRevision, _ := strconv.ParseInt(verified.KeyRevision, 10, 64)
	return platformStagedRelease{sha256: verified.Descriptor.SHA256, publisherID: verified.Descriptor.Manifest.Publisher, appID: verified.Descriptor.Manifest.ID, verificationKeyID: verified.KeyID, trustRevision: trustRevision, keyRevision: keyRevision}
}

// Shared admission boundary for future review/commit callbacks. They must write
// their own intent inside this same transaction, not trust a prior returned value.
func (s *Server) withPlatformTrustActor(session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl, appplatform.PublisherTrust) error) error {
	return s.withPlatformTrustActorContext(context.Background(), session, fn)
}

func (s *Server) withPlatformTrustActorContext(ctx context.Context, session platformActorSession, fn func(migExec, platformActorFacts, platformActorControl, appplatform.PublisherTrust) error) error {
	return s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		return fn(e, actor, control, trust)
	})
}

// Future lifecycle admission uses this callback to bind a current release and its
// expected publisher revision to the transaction that saves reviewed intent.
// Managed execution/compatibility, capabilities and data readiness remain gates.
func (s *Server) withPlatformStagedRelease(session platformActorSession, sha string, expectedTrustRevision int64, fn func(migExec, platformActorFacts, platformActorControl, platformStagedRelease) error) error {
	return s.withPlatformStagedReleaseContext(context.Background(), session, sha, expectedTrustRevision, fn)
}

func (s *Server) withPlatformStagedReleaseContext(ctx context.Context, session platformActorSession, sha string, expectedTrustRevision int64, fn func(migExec, platformActorFacts, platformActorControl, platformStagedRelease) error) error {
	if !platformValidSHA(sha) || expectedTrustRevision <= 0 {
		return errAppPlatformReleaseConflict
	}
	return s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		verified, err := platformVerifyStoredRelease(e, sha, expectedTrustRevision, trust, control.nowMS)
		if err != nil {
			return err
		}
		if err := fn(e, actor, control, verified); err != nil {
			return err
		}
		return platformFinalStoredReleaseTrust(e, session, actor, control, sha, expectedTrustRevision, trust)
	})
}

func (s *Server) recheckPlatformStagedRelease(session platformActorSession, sha string, expectedTrustRevision int64) (platformStagedRelease, error) {
	var result platformStagedRelease
	err := s.withPlatformStagedRelease(session, sha, expectedTrustRevision, func(_ migExec, _ platformActorFacts, _ platformActorControl, verified platformStagedRelease) error {
		result = verified
		return nil
	})
	if err != nil {
		return platformStagedRelease{}, err
	}
	return result, nil
}

func platformVerifyStoredRelease(e migExec, sha string, expectedTrustRevision int64, trust appplatform.PublisherTrust, now int64) (platformStagedRelease, error) {
	var raw, envelope, archive []byte
	var appID, publisher, version, keyID, frontendSHA string
	var managed int
	var archiveBytes int64
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE OF r,f"
	}
	err := e.queryRow(`SELECT r.app_id,r.publisher_id,r.version,r.descriptor,r.signature,r.verification_key_id,r.frontend_sha256,r.managed,f.content,f.archive_bytes FROM app_platform_releases r JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256 WHERE r.sha256=?`+lock, sha).Scan(&appID, &publisher, &version, &raw, &envelope, &keyID, &frontendSHA, &managed, &archive, &archiveBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	if err != nil {
		return platformStagedRelease{}, err
	}
	verified, err := appplatform.VerifyPublisherDescriptor(raw, envelope, trust, strconv.FormatInt(now, 10))
	if err != nil {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	result := platformStagedIdentity(verified)
	m := verified.Descriptor.Manifest
	if result.sha256 != sha || m.ID != appID || m.Publisher != publisher || m.Version != version || verified.KeyID != keyID || m.Frontend.SHA256 != frontendSHA || boolInt(m.Backend != nil) != managed || result.trustRevision != expectedTrustRevision {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	inspected, err := appplatform.InspectFrontend(m, archive)
	if err != nil || inspected.ArchiveBytes != archiveBytes {
		return platformStagedRelease{}, errAppPlatformReleaseConflict
	}
	return result, nil
}

// Only database writes may occur before this final check. All required resource
// locks must already be held; no physical effect is authorized by this helper.
func platformFinalStoredReleaseTrust(e migExec, session platformActorSession, actor platformActorFacts, control platformActorControl, sha string, expectedRevision int64, trust appplatform.PublisherTrust) error {
	var raw, envelope []byte
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	if err := e.queryRow(`SELECT descriptor,signature FROM app_platform_releases WHERE sha256=?`+lock, sha).Scan(&raw, &envelope); err != nil {
		return err
	}
	control, err := platformRefreshActor(e, session, actor, control)
	if err != nil {
		return err
	}
	verified, err := appplatform.VerifyPublisherDescriptor(raw, envelope, trust, strconv.FormatInt(control.nowMS, 10))
	if err != nil || verified.Descriptor.SHA256 != sha {
		return errAppPlatformReleaseConflict
	}
	revision, _ := strconv.ParseInt(verified.PublisherRevision, 10, 64)
	if revision != expectedRevision {
		return errAppPlatformReleaseConflict
	}
	return nil
}
