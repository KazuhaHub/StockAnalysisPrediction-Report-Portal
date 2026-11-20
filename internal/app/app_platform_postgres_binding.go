package app

import (
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The pg_password binding (doc 178, amendment C4). A binding names one reviewed PostgreSQL target
// of the network.postgres grant and the credential that holds the monitor role's username and
// password. It is always the target-scoped policy format (portal.secret-binding-policy/3-dev.1)
// with network_target_sha256 the SHA-256 of the canonical target object, for the service only, and
// valid for as long as that target is one of the current policy.

const (
	platformCapabilityNetworkPostgres = "network.postgres"
	platformPostgresDisclosure        = "connect + SELECT 1 only; use a role with no table privileges"
)

// platformPostgresTargetLabel is how a review and the console name a target.
func platformPostgresTargetLabel(t appplatform.PostgresTarget) string {
	host := t.Host
	if literal := t.Literal(); literal.Is6() {
		host = "[" + host + "]"
	}
	return "postgres://" + host + ":" + strconv.Itoa(t.Port) + "/" + t.Database
}

// platformPostgresPolicy is the grant's probe policy as the facts carry it.
func platformPostgresPolicy(facts platformSecretBindingFacts) (appplatform.ParsedPostgresPolicy, error) {
	parsed, err := appplatform.ParsePostgresPolicy([]byte(facts.postgresPolicyRaw))
	if err != nil {
		return appplatform.ParsedPostgresPolicy{}, errAppPlatformCredentialUnavailable
	}
	return parsed, nil
}

// platformPostgresReviewTarget picks the target a review binds. An empty id is the policy's only
// target and is refused when there are several.
func platformPostgresReviewTarget(facts platformSecretBindingFacts, id string) (digest, label string, err error) {
	policy, err := platformPostgresPolicy(facts)
	if err != nil {
		return "", "", errPlatformManagementInput
	}
	if id == "" && len(policy.Policy.Targets) == 1 {
		id = policy.Policy.Targets[0].ID
	}
	t, ok := policy.Policy.Select(id)
	if !ok {
		return "", "", errPlatformManagementInput
	}
	digest, err = appplatform.PostgresTargetSHA256(t)
	if err != nil {
		return "", "", errPlatformManagementInput
	}
	return digest, platformPostgresTargetLabel(t), nil
}

// platformPostgresTargetPresent says whether the grant's policy has a target with this digest.
func platformPostgresTargetPresent(facts platformSecretBindingFacts, targetSHA string) bool {
	policy, err := platformPostgresPolicy(facts)
	if err != nil {
		return false
	}
	for _, t := range policy.Policy.Targets {
		if digest, err := appplatform.PostgresTargetSHA256(t); err == nil && digest == targetSHA {
			return true
		}
	}
	return false
}

// checkPlatformPostgresCredentialValue opens a credential inside custody and checks that its value
// is a username and password in the form of doc 178 section 4.3, then wipes it. It never reports
// the value.
func (s *Server) checkPlatformPostgresCredentialValue(id string, revision int64, sealed string) error {
	plain, err := s.openPlatformCredentialValue(id, revision, sealed)
	if err != nil {
		return errAppPlatformCredentialUnavailable
	}
	defer clear(plain)
	credential, err := appplatform.ParsePostgresCredential(plain)
	if err != nil {
		return errPlatformManagementInput
	}
	credential.Clear()
	return nil
}

// platformSecretBindingPostgresChoice is a target of the network.postgres grant a pg_password
// binding can be reviewed for: by id and the label a review shows.
type platformSecretBindingPostgresChoice struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

// listPlatformPostgresBindingTargets is the grant's targets, sorted by id, for the review form. A
// grant that cannot be read has none.
func (s *Server) listPlatformPostgresBindingTargets(session platformActorSession, installation string) ([]platformSecretBindingPostgresChoice, error) {
	if !platformValidUUID(installation) {
		return nil, errPlatformManagementInput
	}
	result := []platformSecretBindingPostgresChoice{}
	err := s.withAppPlatformActor(session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if facts, err := loadPlatformSecretBindingFactsKind(e, installation, false, true, false); err == nil && facts.postgresPolicyRaw != "" {
			if policy, err := platformPostgresPolicy(facts); err == nil {
				for _, t := range policy.Policy.Targets {
					result = append(result, platformSecretBindingPostgresChoice{ID: t.ID, Origin: platformPostgresTargetLabel(t)})
				}
			}
		}
		_, err := platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}

// checkPlatformCredentialPairing refuses the pairings a PostgreSQL role must never have: a value
// that is a username and password is bound only as pg_password (never sent as a header, never
// given to an application by raw_read), and pg_password needs such a value. Every other value is
// left to its operation's own checks. It opens the credential inside custody and wipes it.
func (s *Server) checkPlatformCredentialPairing(operation, id string, revision int64, sealed string) error {
	err := s.checkPlatformPostgresCredentialValue(id, revision, sealed)
	if operation == "pg_password" {
		return err
	}
	if err == nil {
		return errPlatformManagementInput
	}
	if errors.Is(err, errAppPlatformCredentialUnavailable) {
		return err
	}
	return nil
}
