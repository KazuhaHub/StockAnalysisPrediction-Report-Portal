package app

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

const maxPlatformInstallAudience = 128

// errPlatformUserAudience is a user principal in an install plan: opening an installation is per OU only.
var errPlatformUserAudience = errors.New("install access is per organisational unit; user principals are not accepted")

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
func resolvePlatformInstallAudience(e migExec, targets []platformAudienceTarget, now int64) ([]platformInstallAudience, error) {
	if len(targets) > maxPlatformInstallAudience {
		return nil, errAppPlatformPlanChanged
	}
	var result []platformInstallAudience
	for _, target := range targets {
		var principal platformInstallAudience
		switch target.kind {
		case "user":
			// Access is an OU policy (doc 190, D7); there are no per-user grants.
			return nil, errPlatformUserAudience
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
