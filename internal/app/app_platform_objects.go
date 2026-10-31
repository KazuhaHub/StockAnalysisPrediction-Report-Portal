package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformObjectAccountBytes   int64 = 32 << 20
	platformObjectAccountObjects int64 = 512
)

type platformObjectSummary struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
	Label      string `json:"label"`
	MediaType  string `json:"mediaType"`
	SizeBytes  int64  `json:"sizeBytes"`
	Revision   int64  `json:"revision"`
	CreatedMS  int64  `json:"createdAt"`
	UpdatedMS  int64  `json:"updatedAt"`
}

type platformObjectDetail struct {
	platformObjectSummary
	Content []byte
}

type platformObjectResult struct {
	Action     string
	Items      []platformObjectSummary
	NextCursor string
	Object     *platformObjectDetail
	DeletedID  string
}

type platformPluginTableRow struct {
	ID          string
	AppID       string
	TableName   string
	Format      int64
	UsedBytes   int64
	UsedObjects int64
	Revision    int64
	CreatedMS   int64
	UpdatedMS   int64
}

type platformStoredObject struct {
	ID                  string
	InstallationID      string
	OwnerUsername       string
	OwnerAccountCreated string
	OwnerGeneration     int64
	Collection          string
	Label               string
	MediaType           string
	Content             []byte
	ContentSHA256       string
	SizeBytes           int64
	Revision            int64
	IdempotencyKey      string
	CreatedMS           int64
	UpdatedMS           int64
}

func platformObjectDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func lockPlatformObjectStorage(e migExec) (int64, int64, error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var usedBytes, usedObjects int64
	err := e.queryRow(`SELECT used_bytes,used_objects FROM app_platform_object_storage WHERE id=1`+lock).Scan(&usedBytes, &usedObjects)
	return usedBytes, usedObjects, err
}

func readPlatformPluginTable(e migExec, appID string, lock bool) (platformPluginTableRow, error) {
	var row platformPluginTableRow
	query := `SELECT id,app_id,table_name,data_format,used_bytes,used_objects,revision,created_ms,updated_ms FROM app_platform_plugin_tables WHERE app_id=?`
	if lock && e.s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	err := e.queryRow(query, appID).Scan(&row.ID, &row.AppID, &row.TableName, &row.Format, &row.UsedBytes, &row.UsedObjects, &row.Revision, &row.CreatedMS, &row.UpdatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return row, errPlatformObjectNotFound
	}
	if err != nil {
		return row, err
	}
	if !platformValidUUID(row.ID) || !platformDirectoryAppID.MatchString(row.AppID) || row.AppID != appID || row.TableName != platformPluginDataTableName(appID) || !platformPluginDataTablePattern.MatchString(row.TableName) || row.Format != 1 || row.UsedBytes < 0 || row.UsedObjects < 0 || row.Revision < 1 {
		return row, fmt.Errorf("plugin data registry row is invalid")
	}
	return row, nil
}

func ensurePlatformPluginTable(e migExec, appID string) (platformPluginTableRow, error) {
	if !platformDirectoryAppID.MatchString(appID) {
		return platformPluginTableRow{}, errPlatformObjectNotFound
	}
	row, err := readPlatformPluginTable(e, appID, true)
	if err == nil {
		if !e.tableExists(row.TableName) {
			return row, fmt.Errorf("plugin data table is missing")
		}
		return row, nil
	}
	if !errors.Is(err, errPlatformObjectNotFound) {
		return row, err
	}
	id, err := platformNewUUID()
	if err != nil {
		return row, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return row, err
	}
	table := platformPluginDataTableName(appID)
	if _, err := e.exec(`INSERT INTO app_platform_plugin_tables(id,app_id,table_name,data_format,used_bytes,used_objects,revision,created_ms,updated_ms) VALUES(?,?,?,1,0,0,1,?,?)`, id, appID, table, now, now); err != nil {
		return row, err
	}
	if err := createPlatformPluginDataTable(e, table); err != nil {
		return row, err
	}
	return readPlatformPluginTable(e, appID, true)
}

func platformObjectAccountUsageForInstallation(e migExec, plugin platformPluginTableRow, installation, username, accountCreated string, generation int64) (int64, int64, error) {
	var usedBytes, usedObjects int64
	err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM `+plugin.TableName+` WHERE installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=?`,
		installation, username, accountCreated, generation).Scan(&usedBytes, &usedObjects)
	return usedBytes, usedObjects, err
}

func platformObjectSummaryOf(row platformStoredObject) platformObjectSummary {
	return platformObjectSummary{row.ID, row.Collection, row.Label, row.MediaType, row.SizeBytes, row.Revision, row.CreatedMS, row.UpdatedMS}
}

func platformObjectCAS(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errPlatformObjectConflict
	}
	return nil
}

func platformObjectSelect(table string) string {
	return `SELECT id,installation_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms FROM ` + table
}

func scanPlatformStoredObject(scan func(...any) error) (platformStoredObject, error) {
	var row platformStoredObject
	var content []byte
	var digest, idempotency string
	err := scan(&row.ID, &row.InstallationID, &row.OwnerUsername, &row.OwnerAccountCreated, &row.OwnerGeneration,
		&row.Collection, &row.Label, &row.MediaType, &content, &digest, &row.SizeBytes, &row.Revision,
		&idempotency, &row.CreatedMS, &row.UpdatedMS)
	if content != nil {
		row.Content = append([]byte(nil), content...)
	}
	row.ContentSHA256, row.IdempotencyKey = digest, idempotency
	return row, err
}

func readPlatformStoredObject(e migExec, plugin platformPluginTableRow, installation, owner, accountCreated string, generation int64, id string, lock bool) (platformStoredObject, error) {
	query := platformObjectSelect(plugin.TableName) + ` WHERE id=? AND installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=?`
	if lock && e.s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	row, err := scanPlatformStoredObject(e.queryRow(query, id, installation, owner, accountCreated, generation).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return platformStoredObject{}, errPlatformObjectNotFound
	}
	if err != nil {
		return platformStoredObject{}, err
	}
	if row.SizeBytes > maxPlatformObjectBytes || int64(len(row.Content)) != row.SizeBytes || platformObjectDigest(row.Content) != row.ContentSHA256 {
		return platformStoredObject{}, errPlatformObjectNotFound
	}
	return row, nil
}

func createPlatformObject(e migExec, installation, appID string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	usedBytes, usedObjects, err := lockPlatformObjectStorage(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	plugin, err := ensurePlatformPluginTable(e, appID)
	if err != nil {
		return platformObjectResult{}, err
	}
	digest := platformObjectDigest(request.content)
	idempotency := platformObjectSelect(plugin.TableName) + ` WHERE installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND idempotency_key=?`
	existing, err := scanPlatformStoredObject(e.queryRow(idempotency, installation, account.username, account.created, account.generation, request.requestID).Scan)
	if err == nil {
		if existing.Collection != request.collection || existing.Label != request.label || existing.MediaType != request.mediaType || existing.SizeBytes != int64(len(request.content)) || existing.ContentSHA256 != digest || !bytes.Equal(existing.Content, request.content) {
			return platformObjectResult{}, errPlatformObjectConflict
		}
		return platformObjectResult{Action: "create", Object: &platformObjectDetail{platformObjectSummaryOf(existing), append([]byte(nil), existing.Content...)}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return platformObjectResult{}, err
	}
	accountBytes, accountObjects, err := platformObjectAccountUsageForInstallation(e, plugin, installation, account.username, account.created, account.generation)
	if err != nil {
		return platformObjectResult{}, err
	}
	quota := platformObjectQuota{
		global:  platformObjectUsage{usedBytes, usedObjects, platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects},
		plugin:  platformObjectUsage{plugin.UsedBytes, plugin.UsedObjects, platformPluginMaxBytes, platformPluginMaxObjects},
		account: platformObjectUsage{accountBytes, accountObjects, platformObjectAccountBytes, platformObjectAccountObjects},
	}
	if err := admitPlatformObjectQuota(quota, int64(len(request.content)), 1, 0, 0); err != nil {
		return platformObjectResult{}, err
	}
	id, err := platformNewUUID()
	if err != nil {
		return platformObjectResult{}, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`INSERT INTO `+plugin.TableName+`(id,installation_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)`,
		id, installation, account.username, account.created, account.generation, request.collection, request.label, request.mediaType, request.content, digest, len(request.content), request.requestID, now, now); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes+?,used_objects=used_objects+1,revision=revision+1,updated_ms=? WHERE id=1`, len(request.content), now); err != nil {
		return platformObjectResult{}, err
	}
	if err := updatePlatformPluginUsage(e, plugin, int64(len(request.content)), 1, now); err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, plugin, installation, account.username, account.created, account.generation, id, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "create", Object: &platformObjectDetail{platformObjectSummaryOf(row), append([]byte(nil), row.Content...)}}, nil
}

func updatePlatformPluginUsage(e migExec, plugin platformPluginTableRow, deltaBytes, deltaObjects, now int64) error {
	changed, err := e.exec(`UPDATE app_platform_plugin_tables SET used_bytes=used_bytes+?,used_objects=used_objects+?,revision=revision+1,updated_ms=? WHERE id=?`, deltaBytes, deltaObjects, now, plugin.ID)
	return platformObjectCAS(changed, err)
}

func listPlatformObjects(e migExec, installation, appID string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	plugin, err := readPlatformPluginTable(e, appID, false)
	if errors.Is(err, errPlatformObjectNotFound) {
		return platformObjectResult{Action: "list", Items: []platformObjectSummary{}}, nil
	}
	if err != nil {
		return platformObjectResult{}, err
	}
	where := `installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND collection=?`
	args := []any{installation, account.username, account.created, account.generation, request.collection}
	if request.cursor != "" {
		millisRaw, id, _ := strings.Cut(request.cursor, ":")
		millis, _ := strconv.ParseInt(millisRaw, 10, 64)
		where += ` AND (updated_ms>? OR (updated_ms=? AND id>?))`
		args = append(args, millis, millis, id)
	}
	rows, err := e.query(platformObjectSelect(plugin.TableName)+` WHERE `+where+` ORDER BY updated_ms,id LIMIT ?`, append(args, request.limit+1)...)
	if err != nil {
		return platformObjectResult{}, err
	}
	defer rows.Close()
	items := make([]platformObjectSummary, 0, request.limit)
	var last platformStoredObject
	for rows.Next() {
		row, err := scanPlatformStoredObject(rows.Scan)
		if err != nil {
			return platformObjectResult{}, err
		}
		if len(items) == request.limit {
			return platformObjectResult{Action: "list", Items: items, NextCursor: fmt.Sprintf("%d:%s", last.UpdatedMS, last.ID)}, nil
		}
		last = row
		items = append(items, platformObjectSummaryOf(row))
	}
	if err := rows.Err(); err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "list", Items: items}, nil
}

func getPlatformObject(e migExec, installation, appID string, account platformActorAccount, id string) (platformObjectResult, error) {
	plugin, err := readPlatformPluginTable(e, appID, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, plugin, installation, account.username, account.created, account.generation, id, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "get", Object: &platformObjectDetail{platformObjectSummaryOf(row), append([]byte(nil), row.Content...)}}, nil
}

func updatePlatformObject(e migExec, installation, appID string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	usedBytes, usedObjects, err := lockPlatformObjectStorage(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	plugin, err := readPlatformPluginTable(e, appID, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, plugin, installation, account.username, account.created, account.generation, request.id, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	if err := checkPlatformObjectRevision(row.Revision, request.expectedRevision); err != nil {
		return platformObjectResult{}, err
	}
	accountBytes, accountObjects, err := platformObjectAccountUsageForInstallation(e, plugin, installation, account.username, account.created, account.generation)
	if err != nil {
		return platformObjectResult{}, err
	}
	quota := platformObjectQuota{
		global:  platformObjectUsage{usedBytes, usedObjects, platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects},
		plugin:  platformObjectUsage{plugin.UsedBytes, plugin.UsedObjects, platformPluginMaxBytes, platformPluginMaxObjects},
		account: platformObjectUsage{accountBytes, accountObjects, platformObjectAccountBytes, platformObjectAccountObjects},
	}
	if err := admitPlatformObjectQuota(quota, int64(len(request.content)), 1, row.SizeBytes, 1); err != nil {
		return platformObjectResult{}, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	digest := platformObjectDigest(request.content)
	changed, err := e.exec(`UPDATE `+plugin.TableName+` SET label=?,media_type=?,content=?,content_sha256=?,size_bytes=?,revision=revision+1,updated_ms=? WHERE id=? AND installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND revision=?`,
		request.label, request.mediaType, request.content, digest, len(request.content), now, row.ID, installation, account.username, account.created, account.generation, row.Revision)
	if err := platformObjectCAS(changed, err); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?+?,revision=revision+1,updated_ms=? WHERE id=1`, row.SizeBytes, len(request.content), now); err != nil {
		return platformObjectResult{}, err
	}
	if err := updatePlatformPluginUsage(e, plugin, int64(len(request.content))-row.SizeBytes, 0, now); err != nil {
		return platformObjectResult{}, err
	}
	updated, err := readPlatformStoredObject(e, plugin, installation, account.username, account.created, account.generation, row.ID, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "update", Object: &platformObjectDetail{platformObjectSummaryOf(updated), append([]byte(nil), updated.Content...)}}, nil
}

func deletePlatformObject(e migExec, installation, appID string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return platformObjectResult{}, err
	}
	plugin, err := readPlatformPluginTable(e, appID, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, plugin, installation, account.username, account.created, account.generation, request.id, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	if err := checkPlatformObjectRevision(row.Revision, request.expectedRevision); err != nil {
		return platformObjectResult{}, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	changed, err := e.exec(`DELETE FROM `+plugin.TableName+` WHERE id=? AND installation_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND revision=?`,
		row.ID, installation, account.username, account.created, account.generation, row.Revision)
	if err := platformObjectCAS(changed, err); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?,used_objects=used_objects-1,revision=revision+1,updated_ms=? WHERE id=1`, row.SizeBytes, now); err != nil {
		return platformObjectResult{}, err
	}
	if err := updatePlatformPluginUsage(e, plugin, -row.SizeBytes, -1, now); err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "delete", DeletedID: row.ID}, nil
}

func platformManifestAllowsStorage(m appplatform.Manifest) bool {
	return slices.Contains(m.Capabilities.Required, "storage.files") || slices.Contains(m.Capabilities.Optional, "storage.files")
}

func (s *Server) executePlatformObjectRequest(ctx context.Context, session platformActorSession, installation string, binding platformObjectBinding, request platformObjectRequest) (platformObjectResult, error) {
	var result platformObjectResult
	_, err := s.withPlatformLaunchAccountReadContext(ctx, session, installation, func(e migExec, current platformLaunchAccess, manifest appplatform.Manifest, account platformActorAccount) error {
		if current.generation != binding.Generation || current.policyRevision != binding.PolicyRevision || current.releaseSHA != binding.ReleaseSHA256 || !platformManifestAllowsStorage(manifest) {
			return errPlatformObjectNotFound
		}
		var granted int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='storage.files' AND granted=1 AND policy_revision=?`, installation, current.policyRevision).Scan(&granted); err != nil || granted != 1 {
			return errPlatformObjectNotFound
		}
		var operationErr error
		switch request.action {
		case "list":
			result, operationErr = listPlatformObjects(e, installation, manifest.ID, account, request)
		case "get":
			result, operationErr = getPlatformObject(e, installation, manifest.ID, account, request.id)
		case "create":
			result, operationErr = createPlatformObject(e, installation, manifest.ID, account, request)
		case "update":
			result, operationErr = updatePlatformObject(e, installation, manifest.ID, account, request)
		case "delete":
			result, operationErr = deletePlatformObject(e, installation, manifest.ID, account, request)
		default:
			operationErr = errPlatformObjectInvalid
		}
		return operationErr
	})
	return result, err
}

func platformPluginUsage(e migExec, plugin platformPluginTableRow) (int64, int64, error) {
	var usedBytes, usedObjects int64
	err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM `+plugin.TableName).Scan(&usedBytes, &usedObjects)
	return usedBytes, usedObjects, err
}

func refreshPlatformPluginUsage(e migExec, plugin platformPluginTableRow, now int64) error {
	usedBytes, usedObjects, err := platformPluginUsage(e, plugin)
	if err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_plugin_tables SET used_bytes=?,used_objects=?,revision=revision+1,updated_ms=? WHERE id=?`, usedBytes, usedObjects, now, plugin.ID)
	return platformObjectCAS(changed, err)
}

func refreshPlatformObjectStorage(e migExec, now int64) error {
	var usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(used_bytes),0),COALESCE(SUM(used_objects),0) FROM app_platform_plugin_tables`).Scan(&usedBytes, &usedObjects); err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=?,used_objects=?,revision=revision+1,updated_ms=? WHERE id=1`, usedBytes, usedObjects, now)
	return platformObjectCAS(changed, err)
}

func cleanupPlatformInstallationObjects(e migExec, installation, appID string, now int64) error {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return err
	}
	plugin, err := readPlatformPluginTable(e, appID, true)
	if errors.Is(err, errPlatformObjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !e.tableExists(plugin.TableName) {
		return fmt.Errorf("plugin data table is missing")
	}
	var others int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations WHERE app_id=? AND id<>? AND desired_state<>'removed'`, appID, installation).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		if _, err := e.exec(`DELETE FROM ` + plugin.TableName); err != nil {
			return err
		}
		if err := dropPlatformPluginDataTable(e, plugin.TableName); err != nil {
			return err
		}
		changed, err := e.exec(`DELETE FROM app_platform_plugin_tables WHERE id=?`, plugin.ID)
		if err := platformObjectCAS(changed, err); err != nil {
			return err
		}
	} else {
		if _, err := e.exec(`DELETE FROM `+plugin.TableName+` WHERE installation_id=?`, installation); err != nil {
			return err
		}
		if err := refreshPlatformPluginUsage(e, plugin, now); err != nil {
			return err
		}
	}
	return refreshPlatformObjectStorage(e, now)
}

func cleanupPlatformAccountObjects(e migExec, username string, now int64) error {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return err
	}
	rows, err := e.query(`SELECT id,app_id,table_name,data_format,used_bytes,used_objects,revision,created_ms,updated_ms FROM app_platform_plugin_tables ORDER BY app_id`)
	if err != nil {
		return err
	}
	var plugins []platformPluginTableRow
	for rows.Next() {
		var row platformPluginTableRow
		if err := rows.Scan(&row.ID, &row.AppID, &row.TableName, &row.Format, &row.UsedBytes, &row.UsedObjects, &row.Revision, &row.CreatedMS, &row.UpdatedMS); err != nil {
			rows.Close()
			return err
		}
		if row.TableName != platformPluginDataTableName(row.AppID) || !platformPluginDataTablePattern.MatchString(row.TableName) {
			rows.Close()
			return fmt.Errorf("plugin data registry row is invalid")
		}
		plugins = append(plugins, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, plugin := range plugins {
		if !e.tableExists(plugin.TableName) {
			return fmt.Errorf("plugin data table is missing")
		}
		if _, err := e.exec(`DELETE FROM `+plugin.TableName+` WHERE owner_username=?`, username); err != nil {
			return err
		}
		if err := refreshPlatformPluginUsage(e, plugin, now); err != nil {
			return err
		}
	}
	return refreshPlatformObjectStorage(e, now)
}
