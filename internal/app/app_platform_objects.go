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

type platformObjectNamespaceRow struct {
	ID            string
	Origin        string
	Owner         sql.NullString
	State         string
	Format        int64
	QuotaBytes    int64
	ObservedBytes int64
	ObjectCount   int64
	Revision      int64
	DeletedMS     sql.NullInt64
	CreatedMS     int64
	UpdatedMS     int64
}

type platformStoredObject struct {
	ID                  string
	NamespaceID         string
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

func readPlatformObjectNamespace(e migExec, installation string, lock bool) (platformObjectNamespaceRow, error) {
	var row platformObjectNamespaceRow
	query := `SELECT id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,object_count,revision,deleted_ms,created_ms,updated_ms FROM app_platform_object_namespaces WHERE owner_installation_id=?`
	if lock && e.s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	err := e.queryRow(query, installation).Scan(&row.ID, &row.Origin, &row.Owner, &row.State, &row.Format, &row.QuotaBytes, &row.ObservedBytes, &row.ObjectCount, &row.Revision, &row.DeletedMS, &row.CreatedMS, &row.UpdatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return row, errPlatformObjectNotFound
	}
	if err != nil {
		return row, err
	}
	if row.State != "active" || !row.Owner.Valid || row.Owner.String != installation || row.Format != 1 || row.QuotaBytes < 1 || row.DeletedMS.Valid {
		return row, errPlatformObjectNotFound
	}
	return row, nil
}

func ensurePlatformObjectNamespace(e migExec, installation string) (platformObjectNamespaceRow, error) {
	row, err := readPlatformObjectNamespace(e, installation, true)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, errPlatformObjectNotFound) {
		return row, err
	}
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_object_namespaces WHERE origin_installation_id=?`, installation).Scan(&count); err != nil {
		return row, err
	}
	if count != 0 {
		return row, errPlatformObjectNotFound
	}
	id, err := platformNewUUID()
	if err != nil {
		return row, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return row, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_object_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,object_count,revision,deleted_ms,created_ms,updated_ms) VALUES(?,?,?,'active',1,?,0,0,1,NULL,?,?)`, id, installation, installation, platformObjectNamespaceBytes, now, now); err != nil {
		return row, err
	}
	return readPlatformObjectNamespace(e, installation, true)
}

func platformObjectAccountUsage(e migExec, namespace, username, accountCreated string, generation int64) (int64, int64, error) {
	var usedBytes, usedObjects int64
	err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects WHERE namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=?`, namespace, username, accountCreated, generation).Scan(&usedBytes, &usedObjects)
	return usedBytes, usedObjects, err
}

func scanPlatformStoredObject(scan func(...any) error) (platformStoredObject, error) {
	var row platformStoredObject
	var content []byte
	var digest, idempotency string
	err := scan(&row.ID, &row.NamespaceID, &row.OwnerUsername, &row.OwnerAccountCreated, &row.OwnerGeneration,
		&row.Collection, &row.Label, &row.MediaType, &content, &digest, &row.SizeBytes, &row.Revision,
		&idempotency, &row.CreatedMS, &row.UpdatedMS)
	if content != nil {
		row.Content = append([]byte(nil), content...)
	}
	row.ContentSHA256, row.IdempotencyKey = digest, idempotency
	return row, err
}

const platformObjectSelect = `SELECT id,namespace_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms FROM app_platform_storage_objects`

func readPlatformStoredObject(e migExec, namespace, owner, accountCreated string, generation int64, id string, lock bool) (platformStoredObject, error) {
	query := platformObjectSelect + ` WHERE id=? AND namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=?`
	if lock && e.s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	row, err := scanPlatformStoredObject(e.queryRow(query, id, namespace, owner, accountCreated, generation).Scan)
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

func createPlatformObject(e migExec, installation string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	usedBytes, usedObjects, err := lockPlatformObjectStorage(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	namespace, err := ensurePlatformObjectNamespace(e, installation)
	if err != nil {
		return platformObjectResult{}, err
	}
	digest := platformObjectDigest(request.content)
	idempotency := platformObjectSelect + ` WHERE namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND idempotency_key=?`
	existing, err := scanPlatformStoredObject(e.queryRow(idempotency, namespace.ID, account.username, account.created, account.generation, request.requestID).Scan)
	if err == nil {
		if existing.Collection != request.collection || existing.Label != request.label || existing.MediaType != request.mediaType || existing.SizeBytes != int64(len(request.content)) || existing.ContentSHA256 != digest || !bytes.Equal(existing.Content, request.content) {
			return platformObjectResult{}, errPlatformObjectConflict
		}
		return platformObjectResult{Action: "create", Object: &platformObjectDetail{platformObjectSummaryOf(existing), append([]byte(nil), existing.Content...)}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return platformObjectResult{}, err
	}
	accountBytes, accountObjects, err := platformObjectAccountUsage(e, namespace.ID, account.username, account.created, account.generation)
	if err != nil {
		return platformObjectResult{}, err
	}
	quota := platformObjectQuota{
		global:    platformObjectUsage{usedBytes, usedObjects, platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects},
		namespace: platformObjectUsage{namespace.ObservedBytes, namespace.ObjectCount, namespace.QuotaBytes, platformObjectNamespaceObjects},
		account:   platformObjectUsage{accountBytes, accountObjects, platformObjectAccountBytes, platformObjectAccountObjects},
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
	if _, err := e.exec(`INSERT INTO app_platform_storage_objects(id,namespace_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)`, id, namespace.ID, account.username, account.created, account.generation, request.collection, request.label, request.mediaType, request.content, digest, len(request.content), request.requestID, now, now); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes+?,used_objects=used_objects+1,revision=revision+1,updated_ms=? WHERE id=1`, len(request.content), now); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_namespaces SET observed_bytes=observed_bytes+?,object_count=object_count+1,revision=revision+1,updated_ms=? WHERE id=? AND owner_installation_id=? AND state='active'`, len(request.content), now, namespace.ID, installation); err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, namespace.ID, account.username, account.created, account.generation, id, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "create", Object: &platformObjectDetail{platformObjectSummaryOf(row), append([]byte(nil), row.Content...)}}, nil
}

func listPlatformObjects(e migExec, installation string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	namespace, err := readPlatformObjectNamespace(e, installation, false)
	if errors.Is(err, errPlatformObjectNotFound) {
		return platformObjectResult{Action: "list", Items: []platformObjectSummary{}}, nil
	}
	if err != nil {
		return platformObjectResult{}, err
	}
	where := `namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND collection=?`
	args := []any{namespace.ID, account.username, account.created, account.generation, request.collection}
	if request.cursor != "" {
		millisRaw, id, _ := strings.Cut(request.cursor, ":")
		millis, _ := strconv.ParseInt(millisRaw, 10, 64)
		where += ` AND (updated_ms>? OR (updated_ms=? AND id>?))`
		args = append(args, millis, millis, id)
	}
	rows, err := e.query(platformObjectSelect+` WHERE `+where+` ORDER BY updated_ms,id LIMIT ?`, append(args, request.limit+1)...)
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

func getPlatformObject(e migExec, installation string, account platformActorAccount, id string) (platformObjectResult, error) {
	namespace, err := readPlatformObjectNamespace(e, installation, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, namespace.ID, account.username, account.created, account.generation, id, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "get", Object: &platformObjectDetail{platformObjectSummaryOf(row), append([]byte(nil), row.Content...)}}, nil
}

func updatePlatformObject(e migExec, installation string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	usedBytes, usedObjects, err := lockPlatformObjectStorage(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	namespace, err := readPlatformObjectNamespace(e, installation, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, namespace.ID, account.username, account.created, account.generation, request.id, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	if err := checkPlatformObjectRevision(row.Revision, request.expectedRevision); err != nil {
		return platformObjectResult{}, err
	}
	accountBytes, accountObjects, err := platformObjectAccountUsage(e, namespace.ID, account.username, account.created, account.generation)
	if err != nil {
		return platformObjectResult{}, err
	}
	quota := platformObjectQuota{
		global:    platformObjectUsage{usedBytes, usedObjects, platformObjectGlobalMaxBytes, platformObjectGlobalMaxObjects},
		namespace: platformObjectUsage{namespace.ObservedBytes, namespace.ObjectCount, namespace.QuotaBytes, platformObjectNamespaceObjects},
		account:   platformObjectUsage{accountBytes, accountObjects, platformObjectAccountBytes, platformObjectAccountObjects},
	}
	if err := admitPlatformObjectQuota(quota, int64(len(request.content)), 1, row.SizeBytes, 1); err != nil {
		return platformObjectResult{}, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return platformObjectResult{}, err
	}
	digest := platformObjectDigest(request.content)
	changed, err := e.exec(`UPDATE app_platform_storage_objects SET label=?,media_type=?,content=?,content_sha256=?,size_bytes=?,revision=revision+1,updated_ms=? WHERE id=? AND namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND revision=?`, request.label, request.mediaType, request.content, digest, len(request.content), now, row.ID, namespace.ID, account.username, account.created, account.generation, row.Revision)
	if err := platformObjectCAS(changed, err); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?+?,revision=revision+1,updated_ms=? WHERE id=1`, row.SizeBytes, len(request.content), now); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_namespaces SET observed_bytes=observed_bytes-?+?,revision=revision+1,updated_ms=? WHERE id=? AND owner_installation_id=? AND state='active'`, row.SizeBytes, len(request.content), now, namespace.ID, installation); err != nil {
		return platformObjectResult{}, err
	}
	updated, err := readPlatformStoredObject(e, namespace.ID, account.username, account.created, account.generation, row.ID, false)
	if err != nil {
		return platformObjectResult{}, err
	}
	return platformObjectResult{Action: "update", Object: &platformObjectDetail{platformObjectSummaryOf(updated), append([]byte(nil), updated.Content...)}}, nil
}

func deletePlatformObject(e migExec, installation string, account platformActorAccount, request platformObjectRequest) (platformObjectResult, error) {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return platformObjectResult{}, err
	}
	namespace, err := readPlatformObjectNamespace(e, installation, true)
	if err != nil {
		return platformObjectResult{}, err
	}
	row, err := readPlatformStoredObject(e, namespace.ID, account.username, account.created, account.generation, request.id, true)
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
	changed, err := e.exec(`DELETE FROM app_platform_storage_objects WHERE id=? AND namespace_id=? AND owner_username=? AND owner_account_created=? AND owner_account_generation=? AND revision=?`, row.ID, namespace.ID, account.username, account.created, account.generation, row.Revision)
	if err := platformObjectCAS(changed, err); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?,used_objects=used_objects-1,revision=revision+1,updated_ms=? WHERE id=1`, row.SizeBytes, now); err != nil {
		return platformObjectResult{}, err
	}
	if _, err := e.exec(`UPDATE app_platform_object_namespaces SET observed_bytes=observed_bytes-?,object_count=object_count-1,revision=revision+1,updated_ms=? WHERE id=? AND owner_installation_id=? AND state='active'`, row.SizeBytes, now, namespace.ID, installation); err != nil {
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
			result, operationErr = listPlatformObjects(e, installation, account, request)
		case "get":
			result, operationErr = getPlatformObject(e, installation, account, request.id)
		case "create":
			result, operationErr = createPlatformObject(e, installation, account, request)
		case "update":
			result, operationErr = updatePlatformObject(e, installation, account, request)
		case "delete":
			result, operationErr = deletePlatformObject(e, installation, account, request)
		default:
			operationErr = errPlatformObjectInvalid
		}
		return operationErr
	})
	return result, err
}

func cleanupPlatformInstallationObjects(e migExec, installation string, now int64) error {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var namespace, state string
	err := e.queryRow(`SELECT id,state FROM app_platform_object_namespaces WHERE owner_installation_id=?`+lock, installation).Scan(&namespace, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "deleted" {
		return nil
	}
	var usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects WHERE namespace_id=?`, namespace).Scan(&usedBytes, &usedObjects); err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM app_platform_storage_objects WHERE namespace_id=?`, namespace); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?,used_objects=used_objects-?,revision=revision+1,updated_ms=? WHERE id=1`, usedBytes, usedObjects, now); err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_object_namespaces SET owner_installation_id=NULL,state='deleted',observed_bytes=0,object_count=0,revision=revision+1,deleted_ms=?,updated_ms=? WHERE id=? AND state<>'deleted'`, now, now, namespace)
	return platformObjectCAS(changed, err)
}

func cleanupPlatformAccountObjects(e migExec, username string, now int64) error {
	if _, _, err := lockPlatformObjectStorage(e); err != nil {
		return err
	}
	rows, err := e.query(`SELECT DISTINCT namespace_id FROM app_platform_storage_objects WHERE owner_username=?`, username)
	if err != nil {
		return err
	}
	var namespaces []string
	for rows.Next() {
		var namespace string
		if err := rows.Scan(&namespace); err != nil {
			rows.Close()
			return err
		}
		namespaces = append(namespaces, namespace)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(namespaces) == 0 {
		return err
	}
	var usedBytes, usedObjects int64
	if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects WHERE owner_username=?`, username).Scan(&usedBytes, &usedObjects); err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM app_platform_storage_objects WHERE owner_username=?`, username); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_object_storage SET used_bytes=used_bytes-?,used_objects=used_objects-?,revision=revision+1,updated_ms=? WHERE id=1`, usedBytes, usedObjects, now); err != nil {
		return err
	}
	for _, namespace := range namespaces {
		var remainingBytes, remainingObjects int64
		if err := e.queryRow(`SELECT COALESCE(SUM(size_bytes),0),COUNT(*) FROM app_platform_storage_objects WHERE namespace_id=?`, namespace).Scan(&remainingBytes, &remainingObjects); err != nil {
			return err
		}
		if _, err := e.exec(`UPDATE app_platform_object_namespaces SET observed_bytes=?,object_count=?,revision=revision+1,updated_ms=? WHERE id=?`, remainingBytes, remainingObjects, now, namespace); err != nil {
			return err
		}
	}
	return nil
}
