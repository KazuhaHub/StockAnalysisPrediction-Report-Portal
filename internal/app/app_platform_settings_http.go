package app

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errPlatformInstallationMissing = errors.New("application installation not found")

type platformSettingsAudience struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type platformSettingsCapability struct {
	Capability string `json:"capability"`
	Granted    bool   `json:"granted"`
	Required   bool   `json:"required"`
}

type platformSettingsBackend struct {
	Required   bool `json:"required"`
	Registered bool `json:"registered"`
	Enabled    bool `json:"enabled"`
}

type platformSettingsData struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Format        int64  `json:"format"`
	QuotaBytes    string `json:"quota_bytes"`
	ObservedBytes string `json:"observed_bytes"`
	Revision      string `json:"revision"`
}

type platformSettingsRecovery struct {
	Count   int                             `json:"count"`
	State   string                          `json:"state,omitempty"`
	Pending bool                            `json:"pending"`
	Points  []platformSettingsRecoveryPoint `json:"points"`
}

type platformSettingsRecoveryPoint struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	CheckpointID    string `json:"checkpoint_id"`
	ReleaseSHA256   string `json:"release_sha256"`
	Format          int64  `json:"format"`
	Bytes           string `json:"bytes"`
	VerifiedMS      string `json:"verified_ms,omitempty"`
	RestoreEligible bool   `json:"restore_eligible"`
}

type platformInstallationSettings struct {
	InstallationID       string                       `json:"installation_id"`
	AppID                string                       `json:"app_id"`
	PublisherID          string                       `json:"publisher_id"`
	DesiredState         string                       `json:"desired_state"`
	Phase                string                       `json:"phase"`
	Generation           string                       `json:"generation"`
	Revision             string                       `json:"revision"`
	PolicyRevision       string                       `json:"policy_revision"`
	CurrentReleaseSHA256 string                       `json:"current_release_sha256,omitempty"`
	Version              string                       `json:"version,omitempty"`
	Display              *appplatform.AppDisplay      `json:"display,omitempty"`
	Audience             []platformSettingsAudience   `json:"audience"`
	Capabilities         []platformSettingsCapability `json:"capabilities"`
	Backend              platformSettingsBackend      `json:"backend"`
	Data                 *platformSettingsData        `json:"data,omitempty"`
	Recovery             platformSettingsRecovery     `json:"recovery"`
	ConfigurationGaps    []string                     `json:"configuration_gaps"`
	RepairEligible       bool                         `json:"repair_eligible"`
	ControlPaused        bool                         `json:"control_paused"`
}

func (s *Server) apiPlatformInstallationSettings(w http.ResponseWriter, r *http.Request, user string) {
	installation := r.PathValue("installation")
	if !platformValidUUID(installation) || len(r.URL.Query()) != 0 {
		platformManagementError(w, errPlatformManagementInput)
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	settings, err := s.platformInstallationSettings(session, installation)
	if err != nil {
		platformManagementError(w, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) platformInstallationSettings(session platformActorSession, installation string) (platformInstallationSettings, error) {
	result := platformInstallationSettings{
		InstallationID:    installation,
		Audience:          []platformSettingsAudience{},
		Capabilities:      []platformSettingsCapability{},
		Recovery:          platformSettingsRecovery{Points: []platformSettingsRecoveryPoint{}},
		ConfigurationGaps: []string{},
	}
	err := s.withAppPlatformActorRead(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		var runtimeID sql.NullString
		var release, version sql.NullString
		var descriptor []byte
		var generation, revision, policyRevision int64
		err := e.queryRow(`SELECT i.app_id,i.publisher_id,i.desired_state,i.phase,
			i.generation,i.revision,i.policy_revision,i.runtime_id,
			r.sha256,r.version,r.descriptor
			FROM app_platform_installations i
			LEFT JOIN app_platform_activations a
			  ON a.id=(
			    SELECT id FROM app_platform_activations
			    WHERE installation_id=i.id
			    ORDER BY CASE WHEN state='active' THEN 0 ELSE 1 END,
			      generation DESC,updated_ms DESC,id DESC LIMIT 1
			  )
			LEFT JOIN app_platform_releases r ON r.sha256=a.release_sha256
			WHERE i.id=?`, installation).Scan(
			&result.AppID, &result.PublisherID, &result.DesiredState,
			&result.Phase, &generation, &revision,
			&policyRevision, &runtimeID, &release, &version,
			&descriptor,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return errPlatformInstallationMissing
		}
		if err != nil {
			return err
		}
		result.Generation = strconv.FormatInt(generation, 10)
		result.Revision = strconv.FormatInt(revision, 10)
		result.PolicyRevision = strconv.FormatInt(policyRevision, 10)
		if release.Valid {
			result.CurrentReleaseSHA256 = release.String
		}
		if version.Valid {
			result.Version = version.String
		}

		required := map[string]bool{}
		if len(descriptor) > 0 {
			parsed, err := appplatform.ParseDescriptor(descriptor)
			if err != nil {
				return err
			}
			manifest := parsed.Manifest
			result.Display = manifest.Presentation.Display
			result.Backend.Required = manifest.Backend != nil
			for _, capability := range manifest.Capabilities.Required {
				required[capability] = true
			}
		}

		rows, err := e.query(`SELECT capability,granted
			FROM app_platform_capability_grants
			WHERE installation_id=? ORDER BY capability LIMIT 257`, installation)
		if err != nil {
			return err
		}
		granted := map[string]bool{}
		for rows.Next() {
			var capability string
			var value int
			if err := rows.Scan(&capability, &value); err != nil {
				rows.Close()
				return err
			}
			if len(result.Capabilities) >= 256 {
				rows.Close()
				return errAppPlatformInstallUnavailable
			}
			granted[capability] = value != 0
			result.Capabilities = append(result.Capabilities, platformSettingsCapability{
				Capability: capability,
				Granted:    value != 0,
				Required:   required[capability],
			})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		missing := make([]string, 0)
		for capability := range required {
			if !granted[capability] {
				missing = append(missing, capability)
			}
		}
		sort.Strings(missing)
		for _, capability := range missing {
			result.ConfigurationGaps = append(result.ConfigurationGaps, "capability:"+capability)
		}

		if runtimeID.Valid {
			var enabled int
			err := e.queryRow(`SELECT enabled
				FROM app_platform_runtime_bindings WHERE id=?`, runtimeID.String).Scan(&enabled)
			switch {
			case err == nil:
				result.Backend.Registered = true
				result.Backend.Enabled = enabled != 0
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		if result.Backend.Required && !result.Backend.Registered {
			result.ConfigurationGaps = append(result.ConfigurationGaps, "backend_not_registered")
		} else if result.Backend.Registered && !result.Backend.Enabled {
			result.ConfigurationGaps = append(result.ConfigurationGaps, "backend_disabled")
		}
		if result.Phase == "setup_required" {
			result.ConfigurationGaps = append(result.ConfigurationGaps, "setup_required")
		}

		rows, err = e.query(`SELECT g.principal_type,
			COALESCE(g.username,''),
			COALESCE(NULLIF(u.display_name,''),g.username,''),
			COALESCE(g.group_id,0),
			COALESCE(NULLIF(ug.name,''),'')
			FROM app_platform_access_grants g
			LEFT JOIN users u ON u.username=g.username
			LEFT JOIN user_groups ug ON ug.id=g.group_id
			WHERE g.installation_id=?
			ORDER BY g.principal_type,g.username,g.group_id LIMIT ?`,
			installation, maxPlatformInstallAudience+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, username, userName, groupName string
			var groupID int64
			if err := rows.Scan(&kind, &username, &userName, &groupID, &groupName); err != nil {
				rows.Close()
				return err
			}
			if len(result.Audience) >= maxPlatformInstallAudience {
				rows.Close()
				return errAppPlatformInstallUnavailable
			}
			item := platformSettingsAudience{Type: kind, ID: username, Name: userName}
			if kind == "ou" {
				item.ID = strconv.FormatInt(groupID, 10)
				item.Name = groupName
				if item.Name == "" {
					item.Name = item.ID
				}
			}
			if item.Name == "" {
				item.Name = item.ID
			}
			result.Audience = append(result.Audience, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}

		var namespace, state string
		var format, quota, observed, dataRevision int64
		err = e.queryRow(`SELECT id,state,data_format,quota_bytes,observed_bytes,revision
			FROM app_platform_data_namespaces
			WHERE origin_installation_id=? OR owner_installation_id=?
			ORDER BY updated_ms DESC,id DESC LIMIT 1`,
			installation, installation).
			Scan(&namespace, &state, &format, &quota, &observed, &dataRevision)
		switch {
		case err == nil:
			result.Data = &platformSettingsData{
				ID: namespace, State: state, Format: format,
				QuotaBytes:    strconv.FormatInt(quota, 10),
				ObservedBytes: strconv.FormatInt(observed, 10),
				Revision:      strconv.FormatInt(dataRevision, 10),
			}
			if state == "review_required" {
				result.ConfigurationGaps = append(result.ConfigurationGaps, "data_review_required")
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		var pending int
		if err := e.queryRow(`SELECT COUNT(*)
			FROM app_platform_operations
			WHERE installation_id=? AND completed_ms IS NULL`,
			installation).Scan(&pending); err != nil {
			return err
		}
		var paused string
		if err := e.queryRow(`SELECT v FROM meta
			WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil {
			return err
		}
		result.ControlPaused = paused != "0"
		if result.ControlPaused {
			result.ConfigurationGaps = append(
				result.ConfigurationGaps, "control_paused")
		}
		if err := e.queryRow(`SELECT COUNT(*)
			FROM app_platform_recovery_sets
			WHERE installation_id=? AND state<>'deleted'`,
			installation).Scan(&result.Recovery.Count); err != nil {
			return err
		}
		if result.Recovery.Count > 0 {
			err := e.queryRow(`SELECT state
				FROM app_platform_recovery_sets
				WHERE installation_id=? AND state<>'deleted'
				ORDER BY updated_ms DESC,id DESC LIMIT 1`,
				installation).Scan(&result.Recovery.State)
			if err != nil {
				return err
			}
			result.Recovery.Pending = result.Recovery.State == "review_required" ||
				result.Recovery.State == "restoring" ||
				result.Recovery.State == "preparing"
			rows, err := e.query(`SELECT id,state,checkpoint_id,release_sha256,
				data_format,bytes,verified_ms
				FROM app_platform_recovery_sets
				WHERE installation_id=? AND state<>'deleted'
				ORDER BY created_ms DESC,id DESC LIMIT 16`,
				installation)
			if err != nil {
				return err
			}
			restoreBaseEligible := !result.ControlPaused &&
				pending == 0 &&
				result.DesiredState == "enabled" &&
				result.Phase == "recovery_required" &&
				result.Backend.Required &&
				result.Backend.Registered &&
				result.Backend.Enabled &&
				result.Data != nil &&
				result.Data.State == "active"
			for rows.Next() {
				var point platformSettingsRecoveryPoint
				var bytes int64
				var verified sql.NullInt64
				if err := rows.Scan(
					&point.ID, &point.State, &point.CheckpointID,
					&point.ReleaseSHA256, &point.Format, &bytes,
					&verified); err != nil {
					rows.Close()
					return err
				}
				point.Bytes = strconv.FormatInt(bytes, 10)
				if verified.Valid {
					point.VerifiedMS = strconv.FormatInt(
						verified.Int64, 10)
				}
				point.RestoreEligible = restoreBaseEligible &&
					point.State == "verified" &&
					point.ReleaseSHA256 == result.CurrentReleaseSHA256
				result.Recovery.Points = append(
					result.Recovery.Points, point)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		if generation >= 2 &&
			!result.ControlPaused &&
			pending == 0 &&
			result.Backend.Required &&
			result.Backend.Registered &&
			result.Backend.Enabled &&
			platformValidSHA(result.CurrentReleaseSHA256) &&
			result.Data != nil &&
			result.Data.State == "active" &&
			((result.DesiredState == "disabled" &&
				(result.Phase == "disabled" ||
					result.Phase == "recovery_required" ||
					result.Phase == "review_required")) ||
				(result.DesiredState == "enabled" &&
					(result.Phase == "pending" ||
						result.Phase == "recovery_required" ||
						result.Phase == "review_required"))) {
			result.RepairEligible = true
		}
		_, err = platformRevalidateActorWithoutPause(
			e, session, actor, control.nowMS)
		return err
	})
	return result, err
}
