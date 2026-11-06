package app

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The import-checkpoint wire contract, Portal side (doc 168, step 11).
//
// These types mirror, field for field and byte for byte, the runner's import-checkpoint command and
// receipt (control.ImportCheckpointCommand and journal.ImportCheckpointReceipt on the runner branch).
// They are declared here, rather than imported, only because that code has not reached this branch;
// when it does, these declarations are replaced by the runner's and nothing else changes. The format
// strings, signature purposes and field names are the runner's, so a command signed here verifies
// there.
const (
	platformImportCommandFormat    = "portal.runner.data-import-command/1-dev.1"
	platformImportReceiptFormat    = "portal.runner.import-checkpoint-receipt/1-dev.1"
	platformImportSignaturePurpose = "portal.app/runner-data-import-command/v1"
	platformImportEffectPurpose    = "portal.app/runner-data-import-effect/v1"
	platformImportEffectFormat     = "portal.runner.data-import-effect/1-dev.1"
	// Inode quota reserved for an imported namespace, the same figure prepare-data reserves.
	platformImportQuotaInodes = "65536"
)

type platformImportCheckpointPayload struct {
	GrantID                  string `json:"grant_id"`
	RecoverySetID            string `json:"recovery_set_id"`
	CheckpointID             string `json:"checkpoint_id"`
	NamespaceID              string `json:"namespace_id"`
	ReleaseSHA256            string `json:"release_sha256"`
	ManifestSHA256           string `json:"manifest_sha256"`
	ArchiveBytes             string `json:"archive_bytes"`
	DataFormat               string `json:"data_format"`
	WriterFence              string `json:"writer_fence"`
	QuotaBytes               string `json:"quota_bytes"`
	QuotaInodes              string `json:"quota_inodes"`
	SourceProjectID          string `json:"source_project_id"`
	SourceNamespaceInode     string `json:"source_namespace_inode"`
	SourceDevice             string `json:"source_device"`
	SourceFilesystemUUID     string `json:"source_filesystem_uuid"`
	SourceRootInode          string `json:"source_root_inode"`
	CheckpointFilesystemUUID string `json:"checkpoint_filesystem_uuid"`
	CheckpointRootInode      string `json:"checkpoint_root_inode"`
}

type platformImportCheckpointCommand struct {
	Format            string                          `json:"format"`
	DeploymentID      string                          `json:"deployment_id"`
	ControllerEpoch   string                          `json:"controller_epoch"`
	ControlRevision   string                          `json:"control_revision"`
	InstallationID    string                          `json:"installation_id"`
	OperationID       string                          `json:"operation_id"`
	TargetGeneration  string                          `json:"target_generation"`
	Stage             string                          `json:"stage"`
	EffectNumber      string                          `json:"effect_number"`
	EffectID          string                          `json:"effect_id"`
	Kind              string                          `json:"kind"`
	InstallationFence string                          `json:"installation_fence"`
	PayloadSHA256     string                          `json:"payload_sha256"`
	IssuedMS          string                          `json:"issued_ms"`
	ExpiresMS         string                          `json:"expires_ms"`
	Payload           platformImportCheckpointPayload `json:"payload"`
}

type platformImportCheckpointClaim struct {
	Format             string `json:"format"`
	ClaimID            string `json:"claim_id"`
	EffectID           string `json:"effect_id"`
	ResourceID         string `json:"resource_id"`
	OwnershipSHA256    string `json:"ownership_sha256"`
	AdmittedFence      string `json:"admitted_fence"`
	ClaimedMS          string `json:"claimed_ms"`
	GrantID            string `json:"grant_id"`
	RecoverySetID      string `json:"recovery_set_id"`
	CheckpointID       string `json:"checkpoint_id"`
	NamespaceID        string `json:"namespace_id"`
	ProjectID          string `json:"project_id"`
	RootFilesystemUUID string `json:"root_filesystem_uuid"`
	RootInode          string `json:"root_inode"`
}

type platformImportCheckpointReceipt struct {
	Format           string                        `json:"format"`
	Claim            platformImportCheckpointClaim `json:"claim"`
	DeploymentID     string                        `json:"deployment_id"`
	ControllerEpoch  string                        `json:"controller_epoch"`
	InstallationID   string                        `json:"installation_id"`
	OperationID      string                        `json:"operation_id"`
	TargetGeneration string                        `json:"target_generation"`
	Stage            string                        `json:"stage"`
	EffectID         string                        `json:"effect_id"`
	PayloadSHA256    string                        `json:"payload_sha256"`
	RecoverySetID    string                        `json:"recovery_set_id"`
	GrantID          string                        `json:"grant_id"`
	CheckpointID     string                        `json:"checkpoint_id"`
	NamespaceID      string                        `json:"namespace_id"`
	ReleaseSHA256    string                        `json:"release_sha256"`
	ManifestSHA256   string                        `json:"manifest_sha256"`
	ArchiveBytes     string                        `json:"archive_bytes"`
	DataFormat       string                        `json:"data_format"`
	WriterFence      string                        `json:"writer_fence"`
	ProjectID        string                        `json:"project_id"`
	NamespaceInode   string                        `json:"namespace_inode"`
	Device           string                        `json:"device"`
	QuotaBytes       string                        `json:"quota_bytes"`
	QuotaInodes      string                        `json:"quota_inodes"`
	Entries          string                        `json:"entries"`
	UsedBytes        string                        `json:"used_bytes"`
	UsedInodes       string                        `json:"used_inodes"`
	State            string                        `json:"state"`
	ObservedMS       string                        `json:"observed_ms"`
	SettledMS        string                        `json:"settled_ms"`
}

// platformImportFrame is the runner's length-framed signing input: purpose, a zero byte, the
// big-endian length of the message, then the message.
func platformImportFrame(purpose string, raw []byte) []byte {
	result := append([]byte(purpose), 0)
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(len(raw)))
	return append(append(result, size...), raw...)
}

func platformImportSigningInput(raw []byte) []byte {
	return platformImportFrame(platformImportSignaturePurpose, raw)
}

// platformImportLogicalDigest is the effect identity: the digest of everything that makes this the
// same import, without the per-attempt fields (control revision, fence, issue and expiry times).
func platformImportLogicalDigest(c platformImportCheckpointCommand) (string, error) {
	logical := struct {
		Format           string                          `json:"format"`
		DeploymentID     string                          `json:"deployment_id"`
		ControllerEpoch  string                          `json:"controller_epoch"`
		InstallationID   string                          `json:"installation_id"`
		OperationID      string                          `json:"operation_id"`
		TargetGeneration string                          `json:"target_generation"`
		Stage            string                          `json:"stage"`
		EffectNumber     string                          `json:"effect_number"`
		Kind             string                          `json:"kind"`
		Payload          platformImportCheckpointPayload `json:"payload"`
	}{platformImportEffectFormat, c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID,
		c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(platformImportFrame(platformImportEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func platformSignImportCheckpoint(c platformImportCheckpointCommand, key ed25519.PrivateKey) ([]byte, []byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, err
	}
	return raw, ed25519.Sign(key, platformImportSigningInput(raw)), nil
}

func platformDecimal(value string, positive bool) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || (positive && n == 0) || strconv.FormatInt(n, 10) != value {
		return 0, false
	}
	return n, true
}

// platformImportReceiptMatchesCommand is the Portal's own check that a receipt settles exactly the
// command it issued, whatever the transport claims. The new root's identity (project, inode, device,
// claim root) is the receiving host's and is deliberately not compared with the old host's provenance.
func platformImportReceiptMatchesCommand(r platformImportCheckpointReceipt, c platformImportCheckpointCommand) bool {
	p := c.Payload
	if r.Format != platformImportReceiptFormat || r.Stage != "restore_data" || r.State != "imported" ||
		r.DeploymentID != c.DeploymentID || r.ControllerEpoch != c.ControllerEpoch || r.InstallationID != c.InstallationID ||
		r.OperationID != c.OperationID || r.TargetGeneration != c.TargetGeneration || r.EffectID != c.EffectID ||
		r.PayloadSHA256 != c.PayloadSHA256 || r.RecoverySetID != p.RecoverySetID || r.GrantID != p.GrantID ||
		r.CheckpointID != p.CheckpointID || r.NamespaceID != p.NamespaceID || r.ReleaseSHA256 != p.ReleaseSHA256 ||
		r.ManifestSHA256 != p.ManifestSHA256 || r.ArchiveBytes != p.ArchiveBytes || r.DataFormat != p.DataFormat ||
		r.WriterFence != p.WriterFence || r.QuotaBytes != p.QuotaBytes || r.QuotaInodes != p.QuotaInodes {
		return false
	}
	k := r.Claim
	if k.EffectID != r.EffectID || k.GrantID != r.GrantID || k.RecoverySetID != r.RecoverySetID ||
		k.CheckpointID != r.CheckpointID || k.NamespaceID != r.NamespaceID || k.ResourceID != r.NamespaceID ||
		k.ProjectID != r.ProjectID || !platformValidUUID(k.RootFilesystemUUID) {
		return false
	}
	for _, value := range []string{r.ProjectID, r.NamespaceInode, r.Device, k.RootInode, k.ClaimedMS, r.ObservedMS, r.SettledMS} {
		if _, ok := platformDecimal(value, true); !ok {
			return false
		}
	}
	quotaBytes, ok1 := platformDecimal(r.QuotaBytes, true)
	quotaInodes, ok2 := platformDecimal(r.QuotaInodes, true)
	usedBytes, ok3 := platformDecimal(r.UsedBytes, false)
	usedInodes, ok4 := platformDecimal(r.UsedInodes, false)
	_, ok5 := platformDecimal(r.Entries, false)
	claimed, _ := platformDecimal(k.ClaimedMS, true)
	observed, _ := platformDecimal(r.ObservedMS, true)
	settled, _ := platformDecimal(r.SettledMS, true)
	return ok1 && ok2 && ok3 && ok4 && ok5 && usedBytes <= quotaBytes && usedInodes <= quotaInodes &&
		observed >= claimed && settled >= observed &&
		// The import landed on a different physical root than the one the checkpoint came from.
		(k.RootFilesystemUUID != p.SourceFilesystemUUID || k.RootInode != p.SourceRootInode)
}

type platformImportIntent struct {
	effectID       string
	runtime        platformRuntimeRegistration
	authority      control.Authority
	command        platformImportCheckpointCommand
	raw, signature []byte
}

type platformImportObservation interface {
	ImportReceipt() (platformImportCheckpointReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

// platformImportCheckpointExecutor is the runner endpoint seam for the two import-checkpoint actions
// (execute and read-receipt). Production uses the transport pair below; tests substitute a fake.
type platformImportCheckpointExecutor interface {
	Execute(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error)
	Read(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error)
}

type platformImportTransport struct{}

func (platformImportTransport) Execute(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	return platformExecuteImportCheckpoint(ctx, endpoint, intent)
}

func (platformImportTransport) Read(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	return platformReadImportCheckpointReceipt(ctx, endpoint, intent)
}

func (s *Server) platformImportExecutor() platformImportCheckpointExecutor {
	if s.platformImportCheckpoint != nil {
		return s.platformImportCheckpoint
	}
	return platformImportTransport{}
}
