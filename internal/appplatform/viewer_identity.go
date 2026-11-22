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

// LegacyAccountCreated stands in for the creation marker of accounts that
// predate users.created_at (NULL or empty there). Real markers are timestamps,
// which never equal this word. Viewer derivation and user-principal audience
// grants (their write and every compare) pass the stored value through
// AccountCreatedMarker so they agree; DeriveViewerIdentity itself stays strict.
// Existing users.created_at values must never be backfilled: the viewer subjects
// and user-principal grants of legacy accounts depend on that value staying empty.
// Stored identities elsewhere (storage owners, review plans, session scope) keep
// the raw value and must not use this marker.
const LegacyAccountCreated = "legacy"

// AccountCreatedMarker maps an empty stored creation value to LegacyAccountCreated
// and returns every real value unchanged.
func AccountCreatedMarker(created string) string {
	if created == "" {
		return LegacyAccountCreated
	}
	return created
}

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
	deriver := ViewerPrincipalDeriver{appID: appID, installationID: installationID, seed: seed}
	result := protocol.Viewer{Subject: deriver.account(account), SessionRevision: account.SessionRevision, Role: account.Role, OUs: make([]string, 0, len(chain))}
	seen := make(map[int64]bool, len(chain))
	for _, ou := range chain {
		if ou.ID < 1 || seen[ou.ID] || !viewerIdentityText(ou.Created, 64) {
			return zero, ErrViewerIdentity
		}
		seen[ou.ID] = true
		result.OUs = append(result.OUs, deriver.ou(ou))
	}
	return result, nil
}

// ViewerPrincipalDeriver derives exactly the account and OU values a viewer
// carries, one at a time, for the principal directory. It shares the viewer's
// frame so a listed principal equals what the same user or OU later presents.
// It borrows the seed: the caller owns and clears it after the last derivation.
type ViewerPrincipalDeriver struct {
	appID, installationID string
	seed                  []byte
}

func NewViewerPrincipalDeriver(appID, installationID string, seed []byte) (ViewerPrincipalDeriver, error) {
	if !slugPattern.MatchString(appID) || !backendUUID.MatchString(installationID) || installationID == "00000000-0000-0000-0000-000000000000" || len(seed) != 32 {
		return ViewerPrincipalDeriver{}, ErrViewerIdentity
	}
	return ViewerPrincipalDeriver{appID: appID, installationID: installationID, seed: seed}, nil
}

// Account returns the 64 hex value of a viewer Subject; Role and SessionRevision are not inputs.
func (d ViewerPrincipalDeriver) Account(account ViewerAccountIdentity) (string, error) {
	if !viewerIdentityText(account.Name, 320) || !viewerIdentityText(account.Created, 64) || account.Generation < 0 {
		return "", ErrViewerIdentity
	}
	return d.account(account), nil
}

// OU returns the 64 hex value of one entry of a viewer's OU chain.
func (d ViewerPrincipalDeriver) OU(ou ViewerOUIdentity) (string, error) {
	if ou.ID < 1 || !viewerIdentityText(ou.Created, 64) {
		return "", ErrViewerIdentity
	}
	return d.ou(ou), nil
}

func (d ViewerPrincipalDeriver) account(a ViewerAccountIdentity) string {
	return d.derive("account", a.Name, a.Created, strconv.FormatInt(a.Generation, 10))
}

func (d ViewerPrincipalDeriver) ou(o ViewerOUIdentity) string {
	return d.derive("ou", strconv.FormatInt(o.ID, 10), o.Created)
}

func (d ViewerPrincipalDeriver) derive(purpose string, fields ...string) string {
	frame, _ := json.Marshal(append([]string{"portal.app-viewer-identity/1-dev.1", d.appID, d.installationID, purpose}, fields...))
	mac := hmac.New(sha256.New, d.seed)
	mac.Write(frame)
	return hex.EncodeToString(mac.Sum(nil))
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
