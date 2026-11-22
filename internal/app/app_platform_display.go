package app

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Holds only descriptor/signature bytes. Directory rendering never reads or
// decompresses the frontend archive and confers no permission to launch it.
type platformDisplayRecord struct {
	release, app, publisher, key, frontend, version string
	raw, signature                                  []byte
	managed                                         int
	coherent                                        bool
}

func platformReadDisplayRecord(e migExec, installation string) (*platformDisplayRecord, error) {
	return platformReadDisplayRecordFor(e, installation, false)
}

// platformReadCurrentReleaseDisplayRecord is the console's view of an installation that has no active
// activation (disabled): it takes the installation's current release the way the settings API does,
// preferring an active activation and otherwise the latest one. Its coherence is not meaningful without
// an active activation, so it is reported as coherent; every signature and identity check still runs.
// The reader directory never calls it.
func platformReadCurrentReleaseDisplayRecord(e migExec, installation string) (*platformDisplayRecord, error) {
	return platformReadDisplayRecordFor(e, installation, true)
}

func platformReadDisplayRecordFor(e migExec, installation string, current bool) (*platformDisplayRecord, error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE OF a,r"
	}
	var record platformDisplayRecord
	var state string
	var generation, policy, currentGeneration, currentPolicy int64
	pick := ` AND a.state='active'`
	if current {
		pick = ` AND a.id=(SELECT id FROM app_platform_activations WHERE installation_id=i.id ORDER BY CASE WHEN state='active' THEN 0 ELSE 1 END,generation DESC,updated_ms DESC,id DESC LIMIT 1)`
	}
	err := e.queryRow(`SELECT r.sha256,r.app_id,r.publisher_id,r.verification_key_id,r.frontend_sha256,r.version,r.descriptor,r.signature,r.managed,a.state,a.generation,a.policy_revision,i.generation,i.policy_revision FROM app_platform_activations a JOIN app_platform_releases r ON r.sha256=a.release_sha256 JOIN app_platform_installations i ON i.id=a.installation_id WHERE a.installation_id=?`+pick+lock, installation).Scan(&record.release, &record.app, &record.publisher, &record.key, &record.frontend, &record.version, &record.raw, &record.signature, &record.managed, &state, &generation, &policy, &currentGeneration, &currentPolicy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.coherent = (current && state != "active") || (generation == currentGeneration && policy == currentPolicy)
	return &record, nil
}
func (r *platformDisplayRecord) verified(trust appplatform.PublisherTrust, now int64, app, publisher string) (*appplatform.AppDisplay, string, bool) {
	verified, err := appplatform.VerifyPublisherDescriptor(r.raw, r.signature, trust, strconv.FormatInt(now, 10))
	if err != nil {
		return nil, "", false
	}
	m := verified.Descriptor.Manifest
	if !r.coherent || verified.Descriptor.SHA256 != r.release || verified.KeyID != r.key || m.ID != app || m.ID != r.app || m.Publisher != publisher || m.Publisher != r.publisher || m.Version != r.version || m.Frontend.SHA256 != r.frontend || boolInt(m.Backend != nil) != r.managed {
		return nil, "", false
	}
	return m.Presentation.Display, platformManifestPublisherName(m), true
}

// The transaction holds the complete trust snapshot stable. Repeat only its
// time-dependent predicates after all signature parsing and database waits.
func (r *platformDisplayRecord) currentKeyTime(trust appplatform.PublisherTrust, now int64) bool {
	for _, publisher := range trust.Publishers {
		if publisher.ID != r.publisher || !publisher.Enabled {
			continue
		}
		for _, key := range publisher.Keys {
			if key.ID != r.key || key.RevokedMS != nil {
				continue
			}
			before, err := strconv.ParseInt(key.NotBeforeMS, 10, 64)
			if err != nil || now < before {
				return false
			}
			if key.NotAfterMS != nil {
				after, err := strconv.ParseInt(*key.NotAfterMS, 10, 64)
				if err != nil || now >= after {
					return false
				}
			}
			return true
		}
	}
	return false
}
