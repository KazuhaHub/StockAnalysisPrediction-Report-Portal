package app

import (
	"context"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// errPlatformMigrationAbsent is what a read-back returns when the runner states definitively that it holds
// no row for the effect. It is final only once every command signed for the effect has expired.
var errPlatformMigrationAbsent = errors.New("runner holds no such migration lifecycle effect")

// platformLifecycleProof is what the transport proves about a reply: who answered, on which endpoint, when
// and to which exact command bytes.
type platformLifecycleProof interface {
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

// platformMigrateObservation is a migrate-data reply: the receipt, or the typed pending or orphaned status.
type platformMigrateObservation interface {
	platformLifecycleProof
	MigrateDataReceipt() (journal.MigrateDataReceipt, error)
	MigrationStatus() (journal.MigrateDataStatus, error)
}

type platformDiscardObservation interface {
	platformLifecycleProof
	DiscardMigrationReceipt() (journal.DiscardMigrationReceipt, error)
	MigrationStatus() (journal.MigrateDataStatus, error)
}

// platformSettlingRestoreObservation is a restore reply to a read: the receipt, or the typed status.
type platformSettlingRestoreObservation interface {
	platformRestoreObservation
	MigrationStatus() (journal.MigrateDataStatus, error)
}

// platformMigrationRunner is the runner endpoint seam for the migration lifecycle: the migrate-data and
// discard-migration actions, the restore that settles them and the removal of a started target. Every read
// returns errPlatformMigrationAbsent for the runner's typed absence and never for anything else. Production
// uses the transport below; tests substitute a fake.
type platformMigrationRunner interface {
	// CheckRoot confirms the runner still reports the data root and profile the operation was reviewed under.
	CheckRoot(ctx context.Context, operation string, runtime platformRuntimeRegistration, authority control.Authority, endpoint platformRuntimeEndpoint) error
	ExecuteMigrate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformMigrateIntent) (platformMigrateObservation, error)
	ReadMigrate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformMigrateIntent) (platformMigrateObservation, error)
	ExecuteDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformDiscardIntent) (platformDiscardObservation, error)
	ReadDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformDiscardIntent) (platformDiscardObservation, error)
	ExecuteRestore(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (platformSettlingRestoreObservation, error)
	ReadRestore(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (platformSettlingRestoreObservation, error)
	RemoveTarget(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error)
}

func (s *Server) platformMigrationRunner() platformMigrationRunner {
	if s.platformMigration != nil {
		return s.platformMigration
	}
	return platformMigrationTransport{server: s}
}

type platformMigrationTransport struct{ server *Server }

func (t platformMigrationTransport) CheckRoot(ctx context.Context, operation string, runtime platformRuntimeRegistration, authority control.Authority, endpoint platformRuntimeEndpoint) error {
	return t.server.checkPlatformManagedOperationRoot(ctx, operation, runtime, authority, endpoint)
}

func migrationReadError(err error) error {
	if errors.Is(err, journal.ErrNoSuchEffect) {
		return errPlatformMigrationAbsent
	}
	return err
}

func (platformMigrationTransport) ExecuteMigrate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformMigrateIntent) (platformMigrateObservation, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.MigrateDataExecute, intent.raw, intent.signature, intent.authority, intent.descriptor)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformMigrationTransport) ReadMigrate(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformMigrateIntent) (platformMigrateObservation, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.MigrateDataRead, intent.raw, intent.readSignature, intent.authority, intent.descriptor)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

func (platformMigrationTransport) ExecuteDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformDiscardIntent) (platformDiscardObservation, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.DiscardMigrationExecute, intent.raw, intent.signature, intent.authority, intent.descriptor)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformMigrationTransport) ReadDiscard(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformDiscardIntent) (platformDiscardObservation, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.DiscardMigrationRead, intent.raw, intent.readSignature, intent.authority, intent.descriptor)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

func (platformMigrationTransport) ExecuteRestore(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (platformSettlingRestoreObservation, error) {
	o, err := platformExecuteRestore(ctx, endpoint, intent)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformMigrationTransport) ReadRestore(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (platformSettlingRestoreObservation, error) {
	o, err := platformReadRestoreReceipt(ctx, endpoint, intent)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

func (platformMigrationTransport) RemoveTarget(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
	action := transport.UpdateStartRemovalStop
	if intent.command.Stage == "remove_code" {
		action = transport.UpdateStartRemovalRemoveCode
	}
	o, err := platformRunUpdateTargetRemoval(ctx, endpoint, intent, action)
	if err != nil {
		return nil, err
	}
	return o, nil
}
