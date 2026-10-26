package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

const maxPlatformInstallAudience = 128

type platformAudienceTarget struct {
	kind, username string
	ouID           int64
}
type platformInstallAudience struct {
	Type              string `json:"type"`
	Username          string `json:"username,omitempty"`
	AccountCreated    string `json:"account_created,omitempty"`
	AccountGeneration string `json:"account_generation,omitempty"`
	GroupID           string `json:"group_id,omitempty"`
	GroupCreated      string `json:"group_created,omitempty"`
}

func platformInstallAudienceKey(p platformInstallAudience) string {
	return p.Type + ":" + p.Username + p.GroupID
}
func platformAudienceUser(e migExec, username string, now int64) (platformInstallAudience, error) {
	var zero platformInstallAudience
	if len(username) == 0 || len(username) > 320 || !utf8.ValidString(username) {
		return zero, errAppPlatformPlanChanged
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	var actual, created, expiry string
	var active int
	err := e.queryRow(`SELECT username,COALESCE(created_at,''),COALESCE(active,1),COALESCE(expires_at,'') FROM users WHERE username=?`+lock, username).Scan(&actual, &created, &active, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, errAppPlatformPlanChanged
	}
	if err != nil {
		return zero, err
	}
	if len(actual) == 0 || len(actual) > 320 || !utf8.ValidString(actual) || created == "" || len(created) > 64 || active != 1 {
		return zero, errAppPlatformPlanChanged
	}
	if expiry != "" {
		var zone sql.NullString
		if err := e.queryRow(`SELECT v FROM meta WHERE k='timezone'`).Scan(&zone); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
		location := time.Local
		if zone.Valid && zone.String != "" {
			var err error
			location, err = time.LoadLocation(zone.String)
			if err != nil {
				return zero, errAppPlatformPlanChanged
			}
		}
		if _, err := time.Parse("2006-01-02", expiry); err != nil || time.UnixMilli(now).In(location).Format("2006-01-02") > expiry {
			return zero, errAppPlatformPlanChanged
		}
	}
	generation, err := notificationAccountGeneration(e, actual)
	if err != nil {
		return zero, err
	}
	return platformInstallAudience{Type: "user", Username: actual, AccountCreated: created, AccountGeneration: strconv.FormatInt(generation, 10)}, nil
}
func resolvePlatformInstallAudience(e migExec, targets []platformAudienceTarget, now int64) ([]platformInstallAudience, error) {
	if len(targets) > maxPlatformInstallAudience {
		return nil, errAppPlatformPlanChanged
	}
	var result []platformInstallAudience
	for _, target := range targets {
		var principal platformInstallAudience
		switch target.kind {
		case "user":
			if target.ouID != 0 {
				return nil, errAppPlatformPlanChanged
			}
			var err error
			principal, err = platformAudienceUser(e, target.username, now)
			if err != nil {
				return nil, err
			}
		case "ou":
			if target.username != "" || target.ouID < 1 {
				return nil, errAppPlatformPlanChanged
			}
			chain, err := platformAudienceChain(e, platformActorAccount{groupID: target.ouID})
			if err != nil {
				return nil, errAppPlatformPlanChanged
			}
			for _, group := range chain {
				if group.id == target.ouID {
					principal = platformInstallAudience{Type: "ou", GroupID: strconv.FormatInt(group.id, 10), GroupCreated: group.created}
					break
				}
			}
			if principal.GroupCreated == "" || len(principal.GroupCreated) > 64 {
				return nil, errAppPlatformPlanChanged
			}
		default:
			return nil, errAppPlatformPlanChanged
		}
		result = append(result, principal)
	}
	sort.Slice(result, func(i, j int) bool {
		return platformInstallAudienceKey(result[i]) < platformInstallAudienceKey(result[j])
	})
	for i := 1; i < len(result); i++ {
		if platformInstallAudienceKey(result[i]) == platformInstallAudienceKey(result[i-1]) {
			return nil, errAppPlatformPlanChanged
		}
	}
	return result, nil
}
func validatePlatformInstallAudience(e migExec, expected []platformInstallAudience, now int64) error {
	if len(expected) > maxPlatformInstallAudience {
		return errAppPlatformPlanChanged
	}
	targets := make([]platformAudienceTarget, 0, len(expected))
	for _, p := range expected {
		target := platformAudienceTarget{kind: p.Type, username: p.Username}
		if p.Type == "ou" {
			id, err := strconv.ParseInt(p.GroupID, 10, 64)
			if err != nil || strconv.FormatInt(id, 10) != p.GroupID {
				return errAppPlatformPlanChanged
			}
			target.ouID = id
		}
		targets = append(targets, target)
	}
	current, err := resolvePlatformInstallAudience(e, targets, now)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(current)
	b, _ := json.Marshal(expected)
	// nil is the canonical no-audience projection, preserving older dev plans.
	if string(a) != string(b) {
		return errAppPlatformPlanChanged
	}
	return nil
}
