package appplatform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"unicode"
	"unicode/utf8"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

var ErrViewerIdentity = errors.New("application viewer identity unavailable")

// ViewerAccountIdentity and ViewerOUIdentity are trusted host inputs, never
// fields accepted from an app. Creation identities prevent name/ID reuse.
type ViewerAccountIdentity struct {
	Name, Created, Role         string
	Generation, SessionRevision int64
}
type ViewerOUIdentity struct {
	ID      int64
	Created string
}

// DeriveViewerIdentity uses an installation's private identity seed, not its
// rotating backend assertion key. Code, policy, boot and control-key changes do
// not change subject/OU IDs. This pure transformation grants no access: callers
// must resolve current account/session, actual OU ancestry and app authorization.
func DeriveViewerIdentity(appID, installationID string, seed []byte, account ViewerAccountIdentity, chain []ViewerOUIdentity) (protocol.Viewer, error) {
	var zero protocol.Viewer
	if !slugPattern.MatchString(appID) || !backendUUID.MatchString(installationID) || installationID == "00000000-0000-0000-0000-000000000000" || len(seed) != 32 ||
		!viewerIdentityText(account.Name, 320) || !viewerIdentityText(account.Created, 64) || !viewerIdentityText(account.Role, 64) || account.Generation < 0 || account.SessionRevision < 0 || len(chain) < 1 || len(chain) > 64 {
		return zero, ErrViewerIdentity
	}
	derive := func(purpose string, fields ...string) string {
		frame, _ := json.Marshal(append([]string{"portal.app-viewer-identity/1-dev.1", appID, installationID, purpose}, fields...))
		mac := hmac.New(sha256.New, seed)
		mac.Write(frame)
		return hex.EncodeToString(mac.Sum(nil))
	}
	result := protocol.Viewer{Subject: derive("account", account.Name, account.Created, strconv.FormatInt(account.Generation, 10)), SessionRevision: account.SessionRevision, Role: account.Role, OUs: make([]string, 0, len(chain))}
	seen := make(map[int64]bool, len(chain))
	for _, ou := range chain {
		if ou.ID < 1 || seen[ou.ID] || !viewerIdentityText(ou.Created, 64) {
			return zero, ErrViewerIdentity
		}
		seen[ou.ID] = true
		result.OUs = append(result.OUs, derive("ou", strconv.FormatInt(ou.ID, 10), ou.Created))
	}
	return result, nil
}

func viewerIdentityText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
