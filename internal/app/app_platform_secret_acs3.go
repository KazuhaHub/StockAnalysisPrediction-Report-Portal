package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Signed requests (doc 177). Portal alone holds the AccessKey secret and the optional STS token: it
// opens the credential inside the authority transaction, computes the ACS3 headers for the exact
// request, wipes its copy, and hands the runner only those header values.

// platformACS3Now and platformACS3Nonce are the clock and the nonce source of a signature; tests
// replace them to compare against fixed vectors.
var (
	platformACS3Now   = time.Now
	platformACS3Nonce = platformACS3RandomNonce
)

// platformACS3RandomNonce is 32 lower-case hex characters from the system random source.
func platformACS3RandomNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// platformACS3ListFits reports whether an action list belongs on operation: a valid list for an
// acs3_hmac_sha256 binding, none for any other.
func platformACS3ListFits(operation string, list []platformACS3Action) bool {
	if operation != "acs3_hmac_sha256" {
		return len(list) == 0
	}
	_, ok := platformACS3Spelling(list)
	return ok
}

// checkPlatformACS3CredentialValue opens a credential inside custody and checks that its value is an
// AccessKey (and optional token) in the form of doc 177 section 3.1, then wipes it. It never reports
// the value.
func (s *Server) checkPlatformACS3CredentialValue(id string, revision int64, sealed string) error {
	plain, err := s.openPlatformCredentialValue(id, revision, sealed)
	if err != nil {
		return errAppPlatformCredentialUnavailable
	}
	defer clear(plain)
	credential, err := appplatform.ParseACS3Credential(plain)
	if err != nil {
		return errPlatformManagementInput
	}
	credential.Clear()
	return nil
}

// platformACS3Call is the request a signature covers, as the admit or the foreground request states
// it. The action and version select a pair of the binding's reviewed policy.
type platformACS3Call struct {
	name, action, version                   string
	method, target, contentType, bodySHA256 string
}

func (c platformACS3Call) wellFormed() bool {
	return platformSecretBindingNamePattern.MatchString(c.name) && appplatform.ValidACS3Action(c.action) && appplatform.ValidACS3Version(c.version) &&
		appplatform.ValidHTTPRequestMethod(c.method) && appplatform.ValidACS3Target(c.target) &&
		appplatform.ValidHTTPPolicyHeaderValue(c.contentType) && platformValidSHA(c.bodySHA256)
}

// signPlatformACS3 resolves the binding for one principal and signs the call. It is the viewer
// path's and the service path's common reading; see signPlatformACS3Facts.
func (s *Server) signPlatformACS3(e migExec, installation, principal string, call platformACS3Call) (platformSecretBindingUse, []appplatform.SignedHeader, error) {
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		return platformSecretBindingUse{}, nil, err
	}
	return s.signPlatformACS3Facts(e, installation, principal, call, facts)
}

// signPlatformACS3Facts loads the active binding with exactly the checks a plain credential gets
// (consumed review, credential state and value revision, principal, network policy), then requires
// an acs3_hmac_sha256 binding whose reviewed policy lists this (action, version), all before the
// credential is opened. The returned headers are the closed set of appplatform.ACS3Sign; the caller
// owns and wipes them. A value that is not an AccessKey is refused without signing anything.
func (s *Server) signPlatformACS3Facts(e migExec, installation, principal string, call platformACS3Call, facts platformSecretBindingFacts) (platformSecretBindingUse, []appplatform.SignedHeader, error) {
	if !call.wellFormed() {
		return platformSecretBindingUse{}, nil, errAppPlatformCredentialUnavailable
	}
	use, sealed, err := loadPlatformSecretBindingUseFor(e, installation, call.name, facts, principal, platformBindingDestination{})
	if err != nil {
		return platformSecretBindingUse{}, nil, err
	}
	if use.operation != "acs3_hmac_sha256" || !slices.Contains(strings.Split(use.acs3, ","), call.action+"@"+call.version) {
		return platformSecretBindingUse{}, nil, errAppPlatformCredentialUnavailable
	}
	plain, err := s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil {
		return platformSecretBindingUse{}, nil, fmt.Errorf("%w: %w: %v", errAppPlatformCredentialUnavailable, errPlatformSecretBindingStore, err)
	}
	credential, err := appplatform.ParseACS3Credential(plain)
	clear(plain)
	if err != nil {
		return platformSecretBindingUse{}, nil, errAppPlatformCredentialUnavailable
	}
	defer credential.Clear()
	nonce, err := platformACS3Nonce()
	if err != nil {
		return platformSecretBindingUse{}, nil, fmt.Errorf("%w: %w: %v", errAppPlatformCredentialUnavailable, errPlatformSecretBindingStore, err)
	}
	headers, err := appplatform.ACS3Sign(appplatform.ACS3Input{
		Method: call.method, Target: call.target, ContentType: call.contentType, BodySHA256: call.bodySHA256,
		Action: call.action, Version: call.version, Date: platformACS3Now(), Nonce: nonce,
		AccessKeyID: credential.AccessKeyID, SecurityToken: credential.SecurityToken,
	}, credential.AccessKeySecret)
	if err != nil {
		return platformSecretBindingUse{}, nil, errAppPlatformCredentialUnavailable
	}
	return use, headers, nil
}
