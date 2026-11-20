package control

import (
	"crypto/ed25519"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// DataQuery is the signed question Portal asks the runner while it writes or revalidates a review of doc 185: does
// exactly one inbox archive carry a digest, would a larger quota fit the runner's capacity, and the removal of an
// inbox archive once its import has completed. It claims no journal row and mutates nothing but the inbox archive
// of a completed import, which a repeat finds already gone.

const DataQueryFormat = "portal.runner.data-query-command/1-dev.1"
const dataQuerySignaturePurpose = "portal.app/runner-data-query-command/v1"
const dataQueryEffectPurpose = "portal.app/runner-data-query-effect/v1"

const (
	DataQueryStage        = "validate"
	DataQueryKind         = "data-query"
	DataQueryEffectNumber = "1"

	DataQueryInspectImport = "inspect_import"
	DataQueryRemoveImport  = "remove_import"
	DataQueryQuotaCapacity = "quota_capacity"
)

// DataQueryPayload names the operation and what it concerns. ArchiveSHA256 is the import archive's manifest digest;
// NamespaceID, QuotaBytes and QuotaInodes are the namespace and the larger quota a raise would apply.
type DataQueryPayload struct {
	// ReleaseSHA256 is the installation's release: the lifecycle transport pairs every action with its descriptor.
	ReleaseSHA256 string `json:"release_sha256"`
	Operation     string `json:"operation"`
	ArchiveSHA256 string `json:"archive_sha256"`
	NamespaceID   string `json:"namespace_id"`
	QuotaBytes    string `json:"quota_bytes"`
	QuotaInodes   string `json:"quota_inodes"`
}

type DataQueryCommand struct {
	CommandHeader
	Payload DataQueryPayload `json:"payload"`
}

type VerifiedDataQueryCommand struct{ command DataQueryCommand }

func (v VerifiedDataQueryCommand) Command() DataQueryCommand { return v.command }

func validDataQueryLogical(c DataQueryCommand) bool {
	if !validHeader(c.CommandHeader, DataQueryFormat, DataQueryStage, DataQueryKind, DataQueryEffectNumber) {
		return false
	}
	p := c.Payload
	if !shaPattern.MatchString(p.ReleaseSHA256) {
		return false
	}
	switch p.Operation {
	case DataQueryInspectImport:
		inodes, ok := counter(p.QuotaInodes, true)
		return shaPattern.MatchString(p.ArchiveSHA256) && p.NamespaceID == "" && p.QuotaBytes == "0" && ok &&
			inodes <= appplatform.DataQuotaMaxInodes
	case DataQueryRemoveImport:
		return shaPattern.MatchString(p.ArchiveSHA256) && p.NamespaceID == "" && p.QuotaBytes == "0" && p.QuotaInodes == "0"
	case DataQueryQuotaCapacity:
		_, _, ok := quotaPair(p.QuotaBytes, p.QuotaInodes)
		return p.ArchiveSHA256 == "" && candidateUUID(p.NamespaceID) && ok
	}
	return false
}

func DataQueryLogicalDigest(c DataQueryCommand) (string, error) {
	if !validDataQueryLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(dataQueryEffectPurpose, "portal.runner.data-query-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func VerifyDataQuery(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDataQueryCommand, error) {
	c, err := parseCanonicalCommand[DataQueryCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, false, dataQuerySignaturePurpose, func() (string, error) { return DataQueryLogicalDigest(c) }) {
		return VerifiedDataQueryCommand{}, ErrInvalidCommand
	}
	return VerifiedDataQueryCommand{command: c}, nil
}

// VerifyDataQuerySettlement checks the command after its signed window, for a reply that finishes late.
func VerifyDataQuerySettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDataQueryCommand, error) {
	c, err := parseCanonicalCommand[DataQueryCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, true, dataQuerySignaturePurpose, func() (string, error) { return DataQueryLogicalDigest(c) }) {
		return VerifiedDataQueryCommand{}, ErrInvalidCommand
	}
	return VerifiedDataQueryCommand{command: c}, nil
}

func SignDataQuery(c DataQueryCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(dataQuerySignaturePurpose, raw))
	if _, err := VerifyDataQuery(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
