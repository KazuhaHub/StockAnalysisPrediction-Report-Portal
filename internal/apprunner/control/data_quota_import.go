package control

import (
	"crypto/ed25519"
	"encoding/json"
	"math"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Two data-lifecycle commands of doc 185: a reviewed raise of a namespace's quota, and a reviewed import of an
// inbox archive into it. Both ride the journal's stages "prepare_data" and "restore_data", because the closed stage
// list of runner_commands has no stage of its own; schema v5 added only their kinds.

const SetDataQuotaFormat = "portal.runner.set-data-quota-command/1-dev.1"
const setDataQuotaSignaturePurpose = "portal.app/runner-set-data-quota-command/v1"
const setDataQuotaReadPurpose = "portal.app/runner-set-data-quota-read/v1"
const setDataQuotaEffectPurpose = "portal.app/runner-set-data-quota-effect/v1"

const (
	SetDataQuotaStage        = "prepare_data"
	SetDataQuotaKind         = "set-data-quota"
	SetDataQuotaEffectNumber = "1"
)

// SetDataQuotaPayload binds one raise to an existing namespace. The old limits are what Portal and the journal
// recorded; the runner compares them with its own evidence and with the live project quota, so a raise signed over
// a stale or hand-edited limit is refused. DataEffectID and DataReceiptSHA256 name the install's prepare-data
// receipt that created the namespace.
type SetDataQuotaPayload struct {
	NamespaceID       string `json:"namespace_id"`
	ReleaseSHA256     string `json:"release_sha256"`
	DataFormat        string `json:"data_format"`
	WriterFence       string `json:"writer_fence"`
	ProjectID         string `json:"project_id"`
	NamespaceInode    string `json:"namespace_inode"`
	Device            string `json:"device"`
	DataEffectID      string `json:"data_effect_id"`
	DataReceiptSHA256 string `json:"data_receipt_sha256"`
	OldBytes          string `json:"old_bytes"`
	OldInodes         string `json:"old_inodes"`
	NewBytes          string `json:"new_bytes"`
	NewInodes         string `json:"new_inodes"`
}

type SetDataQuotaCommand struct {
	CommandHeader
	Payload SetDataQuotaPayload `json:"payload"`
}

type VerifiedSetDataQuotaCommand struct{ command SetDataQuotaCommand }

func (v VerifiedSetDataQuotaCommand) Command() SetDataQuotaCommand { return v.command }

func quotaPair(bytes, inodes string) (int64, int64, bool) {
	b, ok1 := counter(bytes, true)
	i, ok2 := counter(inodes, true)
	return b, i, ok1 && ok2 && appplatform.WithinRunnerDataQuota(b, i) && b%512 == 0
}

func validSetDataQuotaLogical(c SetDataQuotaCommand) bool {
	if !validHeader(c.CommandHeader, SetDataQuotaFormat, SetDataQuotaStage, SetDataQuotaKind, SetDataQuotaEffectNumber) {
		return false
	}
	p := c.Payload
	if !candidateUUID(p.NamespaceID) || !candidateUUID(p.DataEffectID) ||
		!shaPattern.MatchString(p.ReleaseSHA256) || !shaPattern.MatchString(p.DataReceiptSHA256) {
		return false
	}
	for _, value := range []string{p.DataFormat, p.WriterFence} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	project, ok := counter(p.ProjectID, true)
	if !ok || project > math.MaxUint32 || !checkpointUint(p.NamespaceInode) || !checkpointUint(p.Device) {
		return false
	}
	oldBytes, oldInodes, ok1 := quotaPair(p.OldBytes, p.OldInodes)
	newBytes, newInodes, ok2 := quotaPair(p.NewBytes, p.NewInodes)
	// Only a raise: neither limit may fall and at least one must grow (doc 185 question 2).
	return ok1 && ok2 && newBytes >= oldBytes && newInodes >= oldInodes && (newBytes > oldBytes || newInodes > oldInodes)
}

func SetDataQuotaLogicalDigest(c SetDataQuotaCommand) (string, error) {
	if !validSetDataQuotaLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(setDataQuotaEffectPurpose, "portal.runner.set-data-quota-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func verifySetDataQuota(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedSetDataQuotaCommand, error) {
	c, err := parseCanonicalCommand[SetDataQuotaCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return SetDataQuotaLogicalDigest(c) }) {
		return VerifiedSetDataQuotaCommand{}, ErrInvalidCommand
	}
	return VerifiedSetDataQuotaCommand{command: c}, nil
}

func VerifySetDataQuota(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSetDataQuotaCommand, error) {
	return verifySetDataQuota(raw, signature, authority, nowMS, false, setDataQuotaSignaturePurpose)
}

// VerifySetDataQuotaSettlement checks the exact command already admitted for an in-flight raise. Expiry governs
// dispatch; a raise that finishes after the signed window must still be settled.
func VerifySetDataQuotaSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSetDataQuotaCommand, error) {
	return verifySetDataQuota(raw, signature, authority, nowMS, true, setDataQuotaSignaturePurpose)
}

// VerifySetDataQuotaRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifySetDataQuotaRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSetDataQuotaCommand, error) {
	return verifySetDataQuota(raw, signature, authority, nowMS, true, setDataQuotaReadPurpose)
}

func SignSetDataQuota(c SetDataQuotaCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(setDataQuotaSignaturePurpose, raw))
	if _, err := VerifySetDataQuota(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignSetDataQuotaRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(setDataQuotaReadPurpose, raw))
	if _, err := VerifySetDataQuotaRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}

const ImportDataFormat = "portal.runner.import-data-command/1-dev.1"
const importDataSignaturePurpose = "portal.app/runner-import-data-command/v1"
const importDataReadPurpose = "portal.app/runner-import-data-read/v1"
const importDataEffectPurpose = "portal.app/runner-import-data-effect/v1"

const (
	ImportDataStage        = "restore_data"
	ImportDataKind         = "import-data"
	ImportDataEffectNumber = "1"
)

// ImportDataPayload binds one import to the namespace it replaces and the inbox archive it reads. The archive is
// named only by its reviewed digest, size and counts, never by a path. The source checkpoint fields name the
// pre-import checkpoint taken earlier in the same operation, so the runner imports only over a namespace whose
// previous contents are kept.
type ImportDataPayload struct {
	NamespaceID                 string `json:"namespace_id"`
	ReleaseSHA256               string `json:"release_sha256"`
	DataFormat                  string `json:"data_format"`
	WriterFence                 string `json:"writer_fence"`
	ProjectID                   string `json:"project_id"`
	NamespaceInode              string `json:"namespace_inode"`
	Device                      string `json:"device"`
	QuotaBytes                  string `json:"quota_bytes"`
	QuotaInodes                 string `json:"quota_inodes"`
	ArchiveSHA256               string `json:"archive_sha256"`
	ArchiveBytes                string `json:"archive_bytes"`
	ArchiveEntries              string `json:"archive_entries"`
	ContentBytes                string `json:"content_bytes"`
	SourceCheckpointOperationID string `json:"source_checkpoint_operation_id"`
	SourceCheckpointEffectID    string `json:"source_checkpoint_effect_id"`
	SourceCheckpointReceiptSHA  string `json:"source_checkpoint_receipt_sha256"`
}

type ImportDataCommand struct {
	CommandHeader
	Payload ImportDataPayload `json:"payload"`
}

type VerifiedImportDataCommand struct{ command ImportDataCommand }

func (v VerifiedImportDataCommand) Command() ImportDataCommand { return v.command }

func validImportDataLogical(c ImportDataCommand) bool {
	if !validHeader(c.CommandHeader, ImportDataFormat, ImportDataStage, ImportDataKind, ImportDataEffectNumber) {
		return false
	}
	p := c.Payload
	for _, id := range []string{p.NamespaceID, p.SourceCheckpointOperationID, p.SourceCheckpointEffectID} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, sha := range []string{p.ReleaseSHA256, p.ArchiveSHA256, p.SourceCheckpointReceiptSHA} {
		if !shaPattern.MatchString(sha) {
			return false
		}
	}
	// A data format of 0 is the format of an archive without a marker; a release's own format is at least 1.
	if _, ok := counter(p.DataFormat, false); !ok {
		return false
	}
	if _, ok := counter(p.WriterFence, true); !ok {
		return false
	}
	project, ok := counter(p.ProjectID, true)
	if !ok || project > math.MaxUint32 || !checkpointUint(p.NamespaceInode) || !checkpointUint(p.Device) {
		return false
	}
	if _, _, ok := quotaPair(p.QuotaBytes, p.QuotaInodes); !ok {
		return false
	}
	archive, ok := counter(p.ArchiveBytes, true)
	if !ok || archive > MaxCheckpointArchiveBytes {
		return false
	}
	entries, ok1 := counter(p.ArchiveEntries, false)
	content, ok2 := counter(p.ContentBytes, false)
	return ok1 && ok2 && entries <= appplatform.DataQuotaMaxInodes && content <= appplatform.DataQuotaMaxBytes
}

func ImportDataLogicalDigest(c ImportDataCommand) (string, error) {
	if !validImportDataLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(importDataEffectPurpose, "portal.runner.import-data-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func verifyImportData(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedImportDataCommand, error) {
	c, err := parseCanonicalCommand[ImportDataCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return ImportDataLogicalDigest(c) }) {
		return VerifiedImportDataCommand{}, ErrInvalidCommand
	}
	return VerifiedImportDataCommand{command: c}, nil
}

func VerifyImportData(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImportDataCommand, error) {
	return verifyImportData(raw, signature, authority, nowMS, false, importDataSignaturePurpose)
}

// VerifyImportDataSettlement checks the exact command already admitted for an in-flight import.
func VerifyImportDataSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImportDataCommand, error) {
	return verifyImportData(raw, signature, authority, nowMS, true, importDataSignaturePurpose)
}

// VerifyImportDataRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifyImportDataRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImportDataCommand, error) {
	return verifyImportData(raw, signature, authority, nowMS, true, importDataReadPurpose)
}

func SignImportData(c ImportDataCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(importDataSignaturePurpose, raw))
	if _, err := VerifyImportData(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignImportDataRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(importDataReadPurpose, raw))
	if _, err := VerifyImportDataRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
