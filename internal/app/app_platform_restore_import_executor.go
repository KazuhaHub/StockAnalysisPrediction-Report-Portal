package app

import (
	"context"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The import-checkpoint wire contract (command, receipt, signing and effect identity) is the runner's
// own: control.ImportCheckpointCommand and journal.ImportCheckpointReceipt. This file holds only what
// is the Portal's: its independent check that a receipt settles exactly the command it issued, and the
// seam to the runner endpoint.

// platformImportReceiptFormat is the format the runner stamps on an import receipt.
const platformImportReceiptFormat = "portal.runner.import-checkpoint-receipt/1-dev.1"

// Inode quota reserved for an imported namespace, the same figure prepare-data reserves.
const platformImportQuotaInodes = "65536"

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
func platformImportReceiptMatchesCommand(r journal.ImportCheckpointReceipt, c control.ImportCheckpointCommand) bool {
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
	command        control.ImportCheckpointCommand
	descriptor     []byte
	raw, signature []byte
}

type platformImportObservation interface {
	ImportCheckpointReceipt() (journal.ImportCheckpointReceipt, error)
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
	observation, err := platformExecuteImportCheckpoint(ctx, endpoint, intent)
	if err != nil {
		return nil, err
	}
	return observation, nil
}

func (platformImportTransport) Read(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	observation, err := platformReadImportCheckpointReceipt(ctx, endpoint, intent)
	if err != nil {
		return nil, err
	}
	return observation, nil
}

func (s *Server) platformImportExecutor() platformImportCheckpointExecutor {
	if s.platformImportCheckpoint != nil {
		return s.platformImportCheckpoint
	}
	return platformImportTransport{}
}
