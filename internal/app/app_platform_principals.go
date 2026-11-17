package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Principal directory for app grant pickers (doc 181). A v2 backend only ever sees the
// current viewer, so an administrator could not grant "OU Research can see component X"
// before a member of that OU had opened the app. This is the administrator-only directory
// that makes the grant possible: opaque principals equal to what each user and OU will
// later present as the Viewer, with a display label, limited to the installation audience.
const (
	maxPlatformPrincipalRequestBytes = 16 << 10
	platformPrincipalPageMax         = 50
	platformPrincipalResolveMax      = 100
	platformPrincipalQueryMax        = 64
	platformPrincipalCursorMax       = 128
	platformPrincipalLabelMax        = 160
	platformPrincipalCallsPerMinute  = 30
)

var (
	errPlatformPrincipalDenied = errors.New("principal directory not permitted")
	errPlatformPrincipalRate   = errors.New("principal directory rate limited")
	platformPrincipalPattern   = regexp.MustCompile(`^(user|ou):[0-9a-f]{64}$`)
)

type platformPrincipalBinding struct {
	Generation     string `json:"generation"`
	PolicyRevision string `json:"policyRevision"`
	ReleaseSHA256  string `json:"releaseSha256"`
}

func (b platformPrincipalBinding) valid() bool {
	return platformPositiveCounter(b.Generation) && platformPositiveCounter(b.PolicyRevision) && platformValidSHA(b.ReleaseSHA256)
}

type platformPrincipalSearchRequest struct {
	Binding platformPrincipalBinding `json:"binding"`
	Kind    string                   `json:"kind"`
	Query   string                   `json:"query"`
	After   string                   `json:"after"`
	Limit   *int                     `json:"limit"`
}

type platformPrincipalResolveRequest struct {
	Binding    platformPrincipalBinding `json:"binding"`
	Principals []string                 `json:"principals"`
}

// platformPrincipalEntry is one listable principal. parent is only set for OUs.
type platformPrincipalEntry struct {
	principal, label, parent string
}

type platformPrincipalDirectory struct {
	users, ous []platformPrincipalEntry // each sorted by principal
}

type platformPrincipalUserItem struct {
	Principal string `json:"principal"`
	Label     string `json:"label"`
}

type platformPrincipalOUItem struct {
	Principal string  `json:"principal"`
	Label     string  `json:"label"`
	Parent    *string `json:"parent"`
}

func platformPrincipalInvalid(w http.ResponseWriter) {
	jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
}

func platformPrincipalDenied(w http.ResponseWriter) {
	jsonErrorCode(w, 403, "not_permitted", "principal directory not permitted")
}

func platformPrincipalFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlatformPrincipalRate):
		w.Header().Set("Retry-After", "60")
		jsonErrorCode(w, 429, "rate_limited", "too many principal directory calls")
	case errors.Is(err, errPlatformPrincipalDenied), errors.Is(err, errAppPlatformLaunchUnavailable), errors.Is(err, errAppPlatformActorUnavailable):
		platformPrincipalDenied(w)
	default:
		jsonErrorCode(w, 500, "db_error", "principal directory unavailable")
	}
}

func truncatePlatformPrincipalLabel(label string) string {
	label = strings.ToValidUTF8(label, "")
	if len(label) <= platformPrincipalLabelMax {
		return label
	}
	cut := platformPrincipalLabelMax
	for cut > 0 && !utf8.RuneStart(label[cut]) {
		cut--
	}
	return label[:cut]
}

func platformPrincipalCursor(principal string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(principal))
}

// platformPrincipalAfter decodes an opaque cursor for kind. An empty cursor starts at the beginning.
func platformPrincipalAfter(kind, cursor string) (string, bool) {
	if cursor == "" {
		return "", true
	}
	if len(cursor) > platformPrincipalCursorMax {
		return "", false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return "", false
	}
	value := string(raw)
	if !platformPrincipalPattern.MatchString(value) || !strings.HasPrefix(value, kind+":") {
		return "", false
	}
	return value, true
}

// platformPrincipalSpace computes the result space of doc 181 section 3.3 from the current rows:
// accounts that would pass the launch audience check now, and the granted OUs with their current
// descendants. Nothing is cached, so a moved user or reparented OU is gone from the next call.
func (s *Server) platformPrincipalSpace(e migExec, deriver appplatform.ViewerPrincipalDeriver, installationID string, nowMS int64, zone string) (platformPrincipalDirectory, error) {
	var zero platformPrincipalDirectory
	snapshot, err := platformAudienceSnapshot(e)
	if err != nil {
		return zero, err
	}
	graph := snapshot.graph
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	// Audience rows: an OU grant names the OU instance by ID and creation identity; a user grant
	// names the account instance by name, creation identity and generation.
	grantedOUs := map[int64]string{}
	rows, err := e.query(`SELECT group_id,group_created FROM app_platform_access_grants WHERE installation_id=? AND principal_type='ou'`+lock, installationID)
	if err != nil {
		return zero, err
	}
	for rows.Next() {
		var id sql.NullInt64
		var created sql.NullString
		if err := rows.Scan(&id, &created); err != nil {
			rows.Close()
			return zero, err
		}
		if id.Valid && created.Valid {
			grantedOUs[id.Int64] = created.String
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	type accountKey struct {
		name, created string
		generation    int64
	}
	grantedUsers := map[accountKey]bool{}
	rows, err = e.query(`SELECT username,account_created,account_generation FROM app_platform_access_grants WHERE installation_id=? AND principal_type='user'`+lock, installationID)
	if err != nil {
		return zero, err
	}
	for rows.Next() {
		var name, created sql.NullString
		var generation sql.NullInt64
		if err := rows.Scan(&name, &created, &generation); err != nil {
			rows.Close()
			return zero, err
		}
		if name.Valid && created.Valid && generation.Valid {
			grantedUsers[accountKey{name.String, created.String, generation.Int64}] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}

	// The launch rule (platformAudienceChain): an OU chain with an OU that has no creation identity
	// cannot present a viewer, so neither the OU nor an account under it is listable.
	usableChain := func(id int64) ([]int64, bool) {
		chain, err := graph.actualChain(id)
		if err != nil {
			return nil, false
		}
		for _, member := range chain {
			if graph.nodes[member].created == "" {
				return nil, false
			}
		}
		return chain, true
	}

	// OUs: every granted OU that still is the same instance, and all its current descendants.
	inSpace := map[int64]bool{}
	for id, created := range grantedOUs {
		node, ok := graph.nodes[id]
		if !ok || node.created == "" || node.created != created {
			continue
		}
		subtree, err := graph.subtree(id)
		if err != nil {
			continue
		}
		for _, member := range subtree {
			if _, ok := usableChain(member); ok {
				inSpace[member] = true
			}
		}
	}
	result := platformPrincipalDirectory{ous: make([]platformPrincipalEntry, 0, len(inSpace))}
	ouPrincipal := make(map[int64]string, len(inSpace))
	for id := range inSpace {
		node := graph.nodes[id]
		value, err := deriver.OU(appplatform.ViewerOUIdentity{ID: id, Created: node.created})
		if err != nil {
			continue
		}
		ouPrincipal[id] = "ou:" + value
	}
	for id, principal := range ouPrincipal {
		// The nearest ancestor that is itself inside the result space.
		parent := ""
		node := graph.nodes[id]
		for steps, up := 0, node.parent; up != 0 && steps < 64; steps, up = steps+1, graph.nodes[up].parent {
			if candidate, ok := ouPrincipal[up]; ok {
				parent = candidate
				break
			}
		}
		result.ous = append(result.ous, platformPrincipalEntry{principal: principal, label: truncatePlatformPrincipalLabel(node.name), parent: parent})
	}

	// Users: active, unexpired accounts whose current chain or account instance holds a grant.
	generations := map[string]int64{}
	rows, err = e.query(`SELECT source_key,generation FROM notification_source_cycles WHERE source_key LIKE 'notification-account:%'`)
	if err != nil {
		return zero, err
	}
	for rows.Next() {
		var key string
		var generation int64
		if err := rows.Scan(&key, &generation); err != nil {
			rows.Close()
			return zero, err
		}
		generations[key] = generation
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return zero, errAppPlatformActorUnavailable
	}
	today := time.UnixMilli(nowMS).In(location).Format("2006-01-02")
	rows, err = e.query(`SELECT username,COALESCE(display_name,''),COALESCE(created_at,''),group_id,COALESCE(expires_at,'') FROM users WHERE COALESCE(active,1)=1`)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, display, created, expires string
		var group sql.NullInt64
		if err := rows.Scan(&name, &display, &created, &group, &expires); err != nil {
			return zero, err
		}
		if expires != "" {
			if _, err := time.Parse("2006-01-02", expires); err != nil || today > expires {
				continue
			}
		}
		generation := generations[notificationAccountKey(name)]
		leaf := group.Int64
		if leaf == 0 {
			leaf = graph.defaultID
		}
		chain, ok := usableChain(leaf)
		if !ok {
			continue
		}
		allowed := grantedUsers[accountKey{name, created, generation}]
		if !allowed {
			for _, id := range chain {
				if want, ok := grantedOUs[id]; ok && graph.nodes[id].created == want {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			continue
		}
		value, err := deriver.Account(appplatform.ViewerAccountIdentity{Name: name, Created: created, Generation: generation})
		if err != nil {
			continue
		}
		label := display
		if strings.TrimSpace(label) == "" {
			label = name
		}
		result.users = append(result.users, platformPrincipalEntry{principal: "user:" + value, label: truncatePlatformPrincipalLabel(label)})
	}
	if err := rows.Err(); err != nil {
		return zero, err
	}
	byPrincipal := func(a, b platformPrincipalEntry) int { return strings.Compare(a.principal, b.principal) }
	slices.SortFunc(result.users, byPrincipal)
	slices.SortFunc(result.ous, byPrincipal)
	return result, nil
}

// platformPrincipalAccess runs the one read transaction every call uses: exact launch binding,
// current session, enabled and ready installation, granted capability, a Portal administrator who
// passes the app audience, and then the directory itself. Nothing here queries users earlier.
func (s *Server) platformPrincipalAccess(ctx context.Context, session platformActorSession, installation string, expected platformPrincipalBinding, use func(platformPrincipalDirectory) error) (int64, error) {
	var group int64
	_, err := s.withPlatformLaunchAccountReadContext(ctx, session, installation, func(e migExec, current platformLaunchAccess, manifest appplatform.Manifest, account platformActorAccount) error {
		if current.generation != expected.Generation || current.policyRevision != expected.PolicyRevision || current.releaseSHA != expected.ReleaseSHA256 {
			return errPlatformPrincipalDenied
		}
		var granted int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND granted=1 AND policy_revision=?`, installation, current.policyRevision).Scan(&granted); err != nil {
			return err
		}
		declared := slices.Contains(manifest.Capabilities.Required, "principals.read") || slices.Contains(manifest.Capabilities.Optional, "principals.read")
		if granted != 1 || !declared || account.role != "admin" {
			return errPlatformPrincipalDenied
		}
		// The ceiling counts only a caller that already passed every check above: a real, ready
		// installation the administrator may use. Unknown installation ids never reach the limiter,
		// so they cannot grow its table and flush an exhausted viewer's counter.
		if !s.platformPrincipalRate.allow(account.username+"\x00"+installation, platformPrincipalCallsPerMinute, time.Minute, time.Now()) {
			return errPlatformPrincipalRate
		}
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		seed, err := s.readPlatformInstallationSeed(e, installation, manifest.ID)
		defer clear(seed)
		if err != nil {
			return err
		}
		deriver, err := appplatform.NewViewerPrincipalDeriver(manifest.ID, installation, seed)
		if err != nil {
			return errAppPlatformLaunchUnavailable
		}
		directory, err := s.platformPrincipalSpace(e, deriver, installation, now, account.zone)
		if err != nil {
			return err
		}
		group = account.groupID
		return use(directory)
	})
	return group, err
}
