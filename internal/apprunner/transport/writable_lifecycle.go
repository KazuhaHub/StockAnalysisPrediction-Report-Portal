package transport

import (
	"bytes"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// WritableLifecycleAction selects a closed infrastructure operation. It never
// accepts a native path, Docker option, callback or caller-provided success flag.
type WritableLifecycleAction byte

const (
	WritableWriterExecute WritableLifecycleAction = iota + 1
	WritableWriterRead
	WritableWriterReconcile
	WritableCandidateStart
	WritableCandidateRead
	WritableCandidateReconcile
	WritableCandidateProbe
	WritableRegistrationDeliver
	WritableRegistrationRead
	ManagedRemovalStop
	ManagedRemovalRemoveCode
	ManagedRemovalRead
	CheckpointCreate
	CheckpointRead
	RestoreExecute
	RestoreRead
	UpdateStartReserve
	UpdateStartRead
	UpdateStartExecute
	UpdateStartReceipt
	UpdateStartProbe
	UpdateStartRegistrationDeliver
	UpdateStartRegistrationRead
	UpdateStartRemovalStop
	UpdateStartRemovalRemoveCode
	UpdateStartRemovalRead
	ManagedDataDelete
	ManagedDataRead
	ImportCheckpointExecute
	ImportCheckpointRead
	MigrateDataExecute
	MigrateDataRead
	DiscardMigrationExecute
	DiscardMigrationRead
	SettleUpdateStartExecute
	SettleUpdateStartRead
	LoadImageExecute
	LoadImageRead
)

func (a WritableLifecycleAction) valid() bool {
	return a >= WritableWriterExecute && a <= LoadImageRead
}
func (a WritableLifecycleAction) mutating() bool {
	return a == WritableWriterExecute || a == WritableWriterReconcile || a == WritableCandidateStart || a == WritableCandidateReconcile || a == WritableRegistrationDeliver || a == ManagedRemovalStop || a == ManagedRemovalRemoveCode || a == CheckpointCreate || a == RestoreExecute || a == UpdateStartReserve || a == UpdateStartExecute || a == UpdateStartRegistrationDeliver || a == UpdateStartRemovalStop || a == UpdateStartRemovalRemoveCode || a == ManagedDataDelete || a == ImportCheckpointExecute || a == MigrateDataExecute || a == DiscardMigrationExecute || a == SettleUpdateStartExecute || a == LoadImageExecute
}
func (a WritableLifecycleAction) registrationDelivery() bool {
	return a == WritableRegistrationDeliver || a == UpdateStartRegistrationDeliver
}

// This opaque value records authenticated response provenance. Historical
// receipts are not health/boot/access grants. Typed accessors parse fresh copies
// so nested pointer fields cannot mutate the retained canonical evidence.
type WritableLifecycleObservation struct {
	action     WritableLifecycleAction
	body       string
	authority  control.Authority
	endpoint   CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (WritableLifecycleObservation) MarshalJSON() ([]byte, error) {
	return nil, driver.ErrContainerUncertain
}
func (o *WritableLifecycleObservation) UnmarshalJSON([]byte) error {
	*o = WritableLifecycleObservation{}
	return driver.ErrContainerUncertain
}
func (o WritableLifecycleObservation) Action() WritableLifecycleAction { return o.action }
func (o WritableLifecycleObservation) Controller() control.Authority {
	a := o.authority
	a.PublicKey = bytes.Clone(a.PublicKey)
	return a
}
func (o WritableLifecycleObservation) Endpoint() CandidateReceiptEndpoint { return o.endpoint }
func (o WritableLifecycleObservation) ReceivedAt() time.Time              { return o.receivedAt }
func (o WritableLifecycleObservation) CommandSHA256() string              { return o.commandSHA }
func (o WritableLifecycleObservation) WriterReceipt() (journal.DataWriterReceipt, error) {
	if o.action < WritableWriterExecute || o.action > WritableWriterReconcile {
		return journal.DataWriterReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseDataWriterReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) RunningReceipt() (journal.WritableCandidateRunningReceipt, error) {
	if o.action < WritableCandidateStart || o.action > WritableCandidateReconcile {
		return journal.WritableCandidateRunningReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableCandidateRunningReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) Readiness() (journal.WritableCandidateReadiness, error) {
	if o.action != WritableCandidateProbe {
		return journal.WritableCandidateReadiness{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableCandidateReadiness([]byte(o.body))
}
func (o WritableLifecycleObservation) RegistrationReceipt() (journal.WritableBackendRegistrationReceipt, error) {
	if o.action != WritableRegistrationDeliver && o.action != WritableRegistrationRead {
		return journal.WritableBackendRegistrationReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseWritableBackendRegistrationReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error) {
	if o.action < ManagedRemovalStop || o.action > ManagedRemovalRead {
		return journal.ManagedRemovalReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseManagedRemovalReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) CheckpointReceipt() (journal.CheckpointReceipt, error) {
	if o.action < CheckpointCreate || o.action > CheckpointRead {
		return journal.CheckpointReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseCheckpointReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) RestoreReceipt() (journal.RestoreReceipt, error) {
	if o.action < RestoreExecute || o.action > RestoreRead {
		return journal.RestoreReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseRestoreReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) ImportCheckpointReceipt() (journal.ImportCheckpointReceipt, error) {
	if o.action != ImportCheckpointExecute && o.action != ImportCheckpointRead {
		return journal.ImportCheckpointReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseImportCheckpointReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) MigrateDataReceipt() (journal.MigrateDataReceipt, error) {
	if o.action != MigrateDataExecute && o.action != MigrateDataRead {
		return journal.MigrateDataReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseMigrateDataReceipt([]byte(o.body))
}

// LoadImageReceipt returns the receipt of a load-image execute or read-back. A typed status is not a receipt: see
// LoadImageStatus.
func (o WritableLifecycleObservation) LoadImageReceipt() (journal.LoadImageReceipt, error) {
	if o.action != LoadImageExecute && o.action != LoadImageRead {
		return journal.LoadImageReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseLoadImageReceipt([]byte(o.body))
}

// LoadImageStatus returns the typed "pending", "orphaned" or "blobs_missing" answer to a load-image execute or
// read-back. A definite absence is not an observation: it is reported as journal.ErrNoSuchEffect.
func (o WritableLifecycleObservation) LoadImageStatus() (journal.LoadImageStatus, error) {
	if o.action != LoadImageExecute && o.action != LoadImageRead {
		return journal.LoadImageStatus{}, driver.ErrContainerUncertain
	}
	return journal.ParseLoadImageStatus([]byte(o.body))
}

func (o WritableLifecycleObservation) SettleUpdateStartReceipt() (journal.SettleUpdateStartReceipt, error) {
	if o.action != SettleUpdateStartExecute && o.action != SettleUpdateStartRead {
		return journal.SettleUpdateStartReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseSettleUpdateStartReceipt([]byte(o.body))
}

func (o WritableLifecycleObservation) DiscardMigrationReceipt() (journal.DiscardMigrationReceipt, error) {
	if o.action != DiscardMigrationExecute && o.action != DiscardMigrationRead {
		return journal.DiscardMigrationReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseDiscardMigrationReceipt([]byte(o.body))
}

// MigrationStatus returns the typed "pending" or "orphaned" answer to a migrate-data, discard-migration
// or restore read-back. A definite absence is not an observation: it is reported as journal.ErrNoSuchEffect.
func (o WritableLifecycleObservation) MigrationStatus() (journal.MigrateDataStatus, error) {
	if o.action != MigrateDataRead && o.action != DiscardMigrationRead && o.action != RestoreRead {
		return journal.MigrateDataStatus{}, driver.ErrContainerUncertain
	}
	return journal.ParseMigrateDataStatus([]byte(o.body))
}
func (o WritableLifecycleObservation) UpdateStartReservation() (journal.UpdateStartReservation, error) {
	if o.action < UpdateStartReserve || o.action > UpdateStartRead {
		return journal.UpdateStartReservation{}, driver.ErrContainerUncertain
	}
	return journal.ParseUpdateStartReservation([]byte(o.body))
}
func (o WritableLifecycleObservation) UpdateStartRunningReceipt() (journal.UpdateStartRunningReceipt, error) {
	if o.action < UpdateStartExecute || o.action > UpdateStartReceipt {
		return journal.UpdateStartRunningReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseUpdateStartRunningReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) UpdateStartReadiness() (journal.UpdateStartReadiness, error) {
	if o.action != UpdateStartProbe {
		return journal.UpdateStartReadiness{}, driver.ErrContainerUncertain
	}
	return journal.ParseUpdateStartReadiness([]byte(o.body))
}
func (o WritableLifecycleObservation) UpdateStartRegistrationReceipt() (journal.UpdateStartRegistrationReceipt, error) {
	if o.action != UpdateStartRegistrationDeliver && o.action != UpdateStartRegistrationRead {
		return journal.UpdateStartRegistrationReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseUpdateStartRegistrationReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) UpdateStartRemovalReceipt() (journal.ManagedRemovalReceipt, error) {
	if o.action < UpdateStartRemovalStop || o.action > UpdateStartRemovalRead {
		return journal.ManagedRemovalReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseManagedRemovalReceipt([]byte(o.body))
}
func (o WritableLifecycleObservation) DeleteDataReceipt() (journal.DeleteDataReceipt, error) {
	if o.action != ManagedDataDelete && o.action != ManagedDataRead {
		return journal.DeleteDataReceipt{}, driver.ErrContainerUncertain
	}
	return journal.ParseDeleteDataReceipt([]byte(o.body))
}

type writableLifecycleReference struct {
	writer                  control.DataWriterCommand
	candidate               control.WritableCandidateCommand
	registration            control.WritableBackendRegistrationCommand
	removal                 control.ManagedRemovalCommand
	checkpoint              control.CheckpointCommand
	restore                 control.RestoreCommand
	importCheckpoint        control.ImportCheckpointCommand
	migrate                 control.MigrateDataCommand
	discard                 control.DiscardMigrationCommand
	settleStart             control.SettleUpdateStartCommand
	loadImage               control.LoadImageCommand
	updateStart             control.UpdateStartCommand
	updateStartRegistration control.UpdateStartRegistrationCommand
	updateStartRemoval      control.UpdateStartRemovalCommand
	deleteData              control.DeleteDataCommand
	index                   control.Command
	release                 string
	// capacity is the identity a capacity refusal must bind: set for the start-bearing actions only.
	capacity *journal.CapacityIdentity
}

func verifyWritableLifecycle(action WritableLifecycleAction, raw, sig, descriptor []byte, a control.Authority, now int64) (writableLifecycleReference, error) {
	return verifyWritableLifecycleMode(
		action, raw, sig, descriptor, a, now, false)
}

func verifyWritableLifecycleSettlement(action WritableLifecycleAction, raw, sig, descriptor []byte, a control.Authority, now int64) (writableLifecycleReference, error) {
	return verifyWritableLifecycleMode(
		action, raw, sig, descriptor, a, now, true)
}

func verifyWritableLifecycleMode(action WritableLifecycleAction, raw, sig, descriptor []byte, a control.Authority, now int64, settling bool) (writableLifecycleReference, error) {
	var r writableLifecycleReference
	if !action.valid() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(sig) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return r, driver.ErrContainerUncertain
	}
	switch {
	case action <= WritableWriterReconcile:
		v, err := control.VerifyDataWriter(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.writer = v.Command()
		r.index = v.Data()
		r.release = r.index.Payload.ReleaseSHA256
	case action <= WritableCandidateProbe:
		v, err := control.VerifyWritableCandidate(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.candidate = v.Command()
		r.writer = v.Writer().Command()
		r.index = v.Writer().Data()
		r.release = v.Index().Payload.ReleaseSHA256
		id := journal.CapacityIdentityOfCandidate(v.Index())
		r.capacity = &id
	case action <= WritableRegistrationRead:
		v, err := control.VerifyWritableBackendRegistration(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.registration = v.Command()
		r.candidate = v.Candidate().Command()
		r.writer = v.Candidate().Writer().Command()
		r.index = v.Candidate().Writer().Data()
		r.release = v.Candidate().Index().Payload.ReleaseSHA256
	case action <= ManagedRemovalRead:
		v, err := control.VerifyManagedRemoval(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.removal = v.Command()
		r.release = r.removal.Payload.ReleaseSHA256
	case action <= CheckpointRead:
		v, err := control.VerifyCheckpoint(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.checkpoint = v.Command()
		r.release = r.checkpoint.Payload.ReleaseSHA256
	case action <= RestoreRead:
		var v control.VerifiedRestoreCommand
		var err error
		if action == RestoreRead {
			// A read-receipt is signed under its own purpose and never as an execute.
			v, err = control.VerifyRestoreRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifyRestoreSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifyRestore(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.restore = v.Command()
		r.release = r.restore.Payload.ReleaseSHA256
	case action == UpdateStartRegistrationDeliver || action == UpdateStartRegistrationRead:
		v, err := control.VerifyUpdateStartRegistration(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.updateStartRegistration = v.Command()
		r.updateStart = v.Start().Command()
		r.release = r.updateStart.Payload.TargetReleaseSHA256
	case action >= UpdateStartRemovalStop && action <= UpdateStartRemovalRead:
		v, err := control.VerifyUpdateStartRemoval(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.updateStartRemoval = v.Command()
		r.updateStart = v.Start().Command()
		r.release = r.updateStart.Payload.TargetReleaseSHA256
	case action == ManagedDataDelete || action == ManagedDataRead:
		v, err := control.VerifyDeleteData(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.deleteData = v.Command()
		r.release = r.deleteData.Payload.ReleaseSHA256
	case action == ImportCheckpointExecute || action == ImportCheckpointRead:
		var v control.VerifiedImportCheckpointCommand
		var err error
		if action == ImportCheckpointRead {
			// A read-receipt is signed under its own purpose and never as an execute.
			v, err = control.VerifyImportCheckpointRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifyImportCheckpointSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifyImportCheckpoint(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.importCheckpoint = v.Command()
		r.release = r.importCheckpoint.Payload.ReleaseSHA256
		id := journal.CapacityIdentityOfImport(r.importCheckpoint)
		r.capacity = &id
	case action == MigrateDataExecute || action == MigrateDataRead:
		var v control.VerifiedMigrateDataCommand
		var err error
		if action == MigrateDataRead {
			v, err = control.VerifyMigrateDataRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifyMigrateDataSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifyMigrateData(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.migrate = v.Command()
		r.release = r.migrate.Payload.TargetReleaseSHA256
		id := journal.CapacityIdentityOfHeader(r.migrate.CommandHeader)
		r.capacity = &id
	case action == LoadImageExecute || action == LoadImageRead:
		var v control.VerifiedLoadImageCommand
		var err error
		if action == LoadImageRead {
			v, err = control.VerifyLoadImageRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifyLoadImageSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifyLoadImage(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.loadImage = v.Command()
		r.release = r.loadImage.Payload.ReleaseSHA256
		id := journal.CapacityIdentityOfHeader(r.loadImage.CommandHeader)
		r.capacity = &id
	case action == SettleUpdateStartExecute || action == SettleUpdateStartRead:
		var v control.VerifiedSettleUpdateStartCommand
		var err error
		if action == SettleUpdateStartRead {
			v, err = control.VerifySettleUpdateStartRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifySettleUpdateStartSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifySettleUpdateStart(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.settleStart = v.Command()
		r.updateStart = v.Start().Command()
		r.release = r.updateStart.Payload.TargetReleaseSHA256
	case action == DiscardMigrationExecute || action == DiscardMigrationRead:
		var v control.VerifiedDiscardMigrationCommand
		var err error
		if action == DiscardMigrationRead {
			v, err = control.VerifyDiscardMigrationRead(raw, sig, a, now)
		} else if settling {
			v, err = control.VerifyDiscardMigrationSettlement(raw, sig, a, now)
		} else {
			v, err = control.VerifyDiscardMigration(raw, sig, a, now)
		}
		if err != nil {
			return r, err
		}
		r.discard = v.Command()
		r.release = r.discard.Payload.TargetReleaseSHA256
	default:
		verify := control.VerifyUpdateStart
		if action == UpdateStartReceipt {
			// A read-receipt is signed under its own purpose and never as an execute.
			verify = control.VerifyUpdateStartRead
		}
		v, err := verify(raw, sig, a, now)
		if err != nil {
			return r, err
		}
		r.updateStart = v.Command()
		r.release = r.updateStart.Payload.TargetReleaseSHA256
		id := journal.CapacityIdentityOfUpdateStart(r.updateStart)
		r.capacity = &id
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != r.release {
		return writableLifecycleReference{}, driver.ErrContainerUncertain
	}
	return r, nil
}

// capacityRefusalAction reports whether the runner may answer the action with a capacity refusal: the actions
// whose effect starts a backend, runs a migration job or loads an image.
func capacityRefusalAction(action WritableLifecycleAction) bool {
	switch action {
	case WritableCandidateStart, WritableCandidateRead, UpdateStartExecute, UpdateStartReceipt, MigrateDataExecute, MigrateDataRead, LoadImageExecute, LoadImageRead, ImportCheckpointExecute, ImportCheckpointRead:
		return true
	}
	return false
}

// Each independently parsed canonical reply must match its exact signed action.
// This check supplements native/journal validation; it cannot replace it.
func validWritableLifecycleReply(action WritableLifecycleAction, body []byte, r writableLifecycleReference, started, received time.Time) bool {
	if received.Before(started) {
		return false
	}
	if capacityRefusalAction(action) {
		if refusal, err := journal.ParseCapacityRefusal(body); err == nil {
			// A refusal is terminal and immutable: read back later it still carries the time it was settled, so
			// freshness against the command's signing does not apply. Its binding to the exact command does.
			settled, _ := strconv.ParseInt(refusal.SettledMS, 10, 64)
			return r.capacity != nil && refusal.Matches(*r.capacity) && settled <= received.UnixMilli()
		}
	}
	if status, ok := lifecycleStatusReply(action, body); ok {
		return validLifecycleStatus(action, status, r, received)
	}
	if action == LoadImageExecute || action == LoadImageRead {
		return validLoadImageReply(body, r.loadImage, received)
	}
	var settled string
	switch {
	case action >= WritableWriterExecute && action <= WritableWriterReconcile:
		v, e := journal.ParseDataWriterReceipt(body)
		if e != nil || v.Claim.EffectID != r.writer.EffectID || v.Claim.PayloadSHA256 != r.writer.PayloadSHA256 || v.Claim.DataReceiptSHA256 != r.writer.DataReceiptSHA256 || v.Claim.ResourceID != r.writer.ResourceID || v.Claim.AdmittedFence != r.index.InstallationFence || v.Native.Platform != r.writer.Platform || !lifecycleRevisionWithin(v.ControlRevision, r.index.ControlRevision) {
			return false
		}
		settled = v.SettledMS
	case action >= WritableCandidateStart && action <= WritableCandidateReconcile:
		v, e := journal.ParseWritableCandidateRunningReceipt(body)
		if e != nil || v.Claim.EffectID != r.candidate.EffectID || v.Claim.PayloadSHA256 != r.candidate.PayloadSHA256 || v.Claim.ResourceID != r.writer.ResourceID || v.Claim.WriterReceiptSHA256 != r.candidate.WriterReceiptSHA256 || v.Claim.DataReceiptSHA256 != r.writer.DataReceiptSHA256 || v.Claim.AdmittedFence != r.index.InstallationFence || !lifecycleRevisionWithin(v.Claim.ControlRevision, r.index.ControlRevision) || (v.RecoveryControlRevision != "" && !lifecycleRevisionWithin(v.RecoveryControlRevision, r.index.ControlRevision)) {
			return false
		}
		settled = v.SettledMS
	case action == WritableCandidateProbe:
		v, e := journal.ParseWritableCandidateReadiness(body)
		if e != nil || v.PayloadSHA256 != r.candidate.PayloadSHA256 || v.DataFormat != r.index.Payload.DataFormat {
			return false
		}
		observed, _ := strconv.ParseInt(v.ObservedMS, 10, 64)
		return observed >= started.UnixMilli() && observed <= received.UnixMilli()
	case action == WritableRegistrationDeliver || action == WritableRegistrationRead:
		v, e := journal.ParseWritableBackendRegistrationReceipt(body)
		if e != nil || v.Claim.EffectID != r.registration.EffectID || v.Claim.PayloadSHA256 != r.registration.PayloadSHA256 || v.Claim.SourceSHA256 != r.registration.RunningReceiptSHA256 || v.Claim.DocumentSHA256 != r.registration.DocumentSHA256 || !lifecycleRevisionWithin(v.Claim.ControlRevision, r.index.ControlRevision) || (v.RecoveryControlRevision != "" && !lifecycleRevisionWithin(v.RecoveryControlRevision, r.index.ControlRevision)) {
			return false
		}
		settled = v.SettledMS
	case action >= ManagedRemovalStop && action <= ManagedRemovalRead:
		v, e := journal.ParseManagedRemovalReceipt(body)
		expectedStage := r.removal.Stage
		if action == ManagedRemovalStop {
			expectedStage = "stop"
		} else if action == ManagedRemovalRemoveCode {
			expectedStage = "remove_code"
		}
		if e != nil || v.DeploymentID != r.removal.DeploymentID || v.ControllerEpoch != r.removal.ControllerEpoch ||
			v.InstallationID != r.removal.InstallationID || v.OperationID != r.removal.OperationID ||
			v.SourceOperationID != r.removal.Payload.SourceOperationID || v.SourceEffectID != r.removal.Payload.SourceEffectID ||
			v.TargetGeneration != r.removal.TargetGeneration || v.Stage != expectedStage ||
			v.EffectID != r.removal.EffectID || v.PayloadSHA256 != r.removal.PayloadSHA256 ||
			v.SourceReceiptSHA256 != r.removal.Payload.SourceReceiptSHA256 ||
			v.ResourceID != r.removal.Payload.ResourceID || v.DataNamespaceID != r.removal.Payload.DataNamespaceID ||
			v.ReleaseSHA256 != r.removal.Payload.ReleaseSHA256 {
			return false
		}
		observed, observedErr := strconv.ParseInt(v.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(r.removal.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil || observed < issued {
			return false
		}
		settled = v.SettledMS
	case action >= CheckpointCreate && action <= CheckpointRead:
		v, e := journal.ParseCheckpointReceipt(body)
		c := r.checkpoint
		if e != nil || v.Stage != "snapshot" || v.DeploymentID != c.DeploymentID ||
			v.ControllerEpoch != c.ControllerEpoch || v.InstallationID != c.InstallationID ||
			v.OperationID != c.OperationID || v.TargetGeneration != c.TargetGeneration ||
			v.EffectID != c.EffectID || v.PayloadSHA256 != c.PayloadSHA256 ||
			v.CheckpointID != c.Payload.CheckpointID || v.NamespaceID != c.Payload.NamespaceID ||
			v.ResourceID != c.Payload.ResourceID || v.ReleaseSHA256 != c.Payload.ReleaseSHA256 ||
			v.SourceOperationID != c.Payload.SourceOperationID ||
			v.SourceStartEffectID != c.Payload.SourceStartEffectID ||
			v.SourceReceiptSHA256 != c.Payload.SourceReceiptSHA256 ||
			v.SourceDataEffectID != c.Payload.SourceDataEffectID ||
			v.SourceDataReceiptSHA256 != c.Payload.SourceDataReceiptSHA256 ||
			v.ProjectID != c.Payload.ProjectID || v.NamespaceInode != c.Payload.NamespaceInode ||
			v.Device != c.Payload.Device || v.DataFormat != c.Payload.DataFormat ||
			v.WriterFence != c.Payload.WriterFence {
			return false
		}
		observed, observedErr := strconv.ParseInt(v.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil || observed < issued {
			return false
		}
		settled = v.SettledMS
	case action >= RestoreExecute && action <= RestoreRead:
		v, e := journal.ParseRestoreReceipt(body)
		c := r.restore
		if e != nil || v.Stage != "restore_data" || v.State != "restored" ||
			v.DeploymentID != c.DeploymentID || v.ControllerEpoch != c.ControllerEpoch ||
			v.InstallationID != c.InstallationID || v.OperationID != c.OperationID ||
			v.TargetGeneration != c.TargetGeneration || v.EffectID != c.EffectID ||
			v.PayloadSHA256 != c.PayloadSHA256 || v.RecoverySetID != c.Payload.RecoverySetID ||
			v.CheckpointID != c.Payload.CheckpointID || v.NamespaceID != c.Payload.NamespaceID ||
			v.ReleaseSHA256 != c.Payload.ReleaseSHA256 || v.ManifestSHA256 != c.Payload.ManifestSHA256 ||
			v.ArchiveBytes != c.Payload.ArchiveBytes || v.ProjectID != c.Payload.ProjectID ||
			v.NamespaceInode != c.Payload.NamespaceInode || v.Device != c.Payload.Device ||
			v.DataFormat != c.Payload.DataFormat || v.WriterFence != c.Payload.WriterFence ||
			v.SourceCheckpointOperationID != c.Payload.SourceCheckpointOperationID ||
			v.SourceCheckpointEffectID != c.Payload.SourceCheckpointEffectID ||
			v.SourceCheckpointReceiptSHA != c.Payload.SourceCheckpointReceiptSHA {
			return false
		}
		// A restored receipt is terminal and immutable: read back later it still carries the time it was
		// observed, so freshness against the command's signing does not apply. Its binding to the effect is above.
		settled = v.SettledMS
	case action == ImportCheckpointExecute || action == ImportCheckpointRead:
		if importAbsenceReply(action, body) {
			absence, _ := journal.ParseImportCheckpointAbsence(body)
			observed, observedErr := strconv.ParseInt(absence.ObservedMS, 10, 64)
			issued, issuedErr := strconv.ParseInt(r.importCheckpoint.IssuedMS, 10, 64)
			return journal.ImportAbsenceMatchesCommand(absence, r.importCheckpoint) &&
				observedErr == nil && issuedErr == nil && observed >= issued && observed <= received.UnixMilli()
		}
		v, e := journal.ParseImportCheckpointReceipt(body)
		c := r.importCheckpoint
		p := c.Payload
		if e != nil || v.Stage != "restore_data" || v.State != "imported" ||
			v.DeploymentID != c.DeploymentID || v.ControllerEpoch != c.ControllerEpoch ||
			v.InstallationID != c.InstallationID || v.OperationID != c.OperationID ||
			v.TargetGeneration != c.TargetGeneration || v.EffectID != c.EffectID ||
			v.PayloadSHA256 != c.PayloadSHA256 || v.GrantID != p.GrantID ||
			v.RecoverySetID != p.RecoverySetID || v.CheckpointID != p.CheckpointID ||
			v.NamespaceID != p.NamespaceID || v.ReleaseSHA256 != p.ReleaseSHA256 ||
			v.ManifestSHA256 != p.ManifestSHA256 || v.ArchiveBytes != p.ArchiveBytes ||
			v.DataFormat != p.DataFormat || v.WriterFence != p.WriterFence ||
			v.QuotaBytes != p.QuotaBytes || v.QuotaInodes != p.QuotaInodes ||
			v.Claim.EffectID != c.EffectID || v.Claim.GrantID != p.GrantID ||
			v.Claim.AdmittedFence != c.InstallationFence {
			return false
		}
		observed, observedErr := strconv.ParseInt(v.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil || observed < issued {
			return false
		}
		settled = v.SettledMS
	case action == MigrateDataExecute || action == MigrateDataRead:
		v, e := journal.ParseMigrateDataReceipt(body)
		if e != nil || !journal.MigrateDataReceiptMatchesCommand(v, r.migrate) {
			return false
		}
		// Terminal receipts are immutable, so a read-back is not held to the command's signing time.
		settled = v.SettledMS
	case action == SettleUpdateStartExecute || action == SettleUpdateStartRead:
		v, e := journal.ParseSettleUpdateStartReceipt(body)
		if e != nil || !journal.SettleUpdateStartReceiptMatchesCommand(v, r.settleStart, r.updateStart) {
			return false
		}
		// A settled receipt is terminal and immutable, like the other receipts.
		settled = v.SettledMS
	case action == DiscardMigrationExecute || action == DiscardMigrationRead:
		v, e := journal.ParseDiscardMigrationReceipt(body)
		if e != nil || !journal.DiscardMigrationReceiptMatchesCommand(v, r.discard) {
			return false
		}
		settled = v.SettledMS
	case action >= UpdateStartReserve && action <= UpdateStartRead:
		v, e := journal.ParseUpdateStartReservation(body)
		c := r.updateStart
		if e != nil || v.ResourceID != c.Payload.ResourceID ||
			v.InstallationID != c.InstallationID || v.NamespaceID != c.Payload.NamespaceID ||
			v.EffectID != c.EffectID || v.SourceGeneration != mustLifecycleInt(c.Payload.SourceGeneration) ||
			v.TargetGeneration != mustLifecycleInt(c.TargetGeneration) ||
			v.SourceReleaseSHA256 != c.Payload.SourceReleaseSHA256 ||
			v.TargetReleaseSHA256 != c.Payload.TargetReleaseSHA256 ||
			v.ControlFence != mustLifecycleInt(c.InstallationFence) ||
			v.SourceWriterFence != mustLifecycleInt(c.Payload.SourceWriterFence) ||
			v.TargetWriterFence != mustLifecycleInt(c.Payload.TargetWriterFence) {
			return false
		}
		return true
	case action >= UpdateStartExecute && action <= UpdateStartReceipt:
		if startAbsenceReply(action, body) {
			absence, _ := journal.ParseUpdateStartAbsence(body)
			observed, observedErr := strconv.ParseInt(absence.ObservedMS, 10, 64)
			issued, issuedErr := strconv.ParseInt(r.updateStart.IssuedMS, 10, 64)
			return journal.UpdateStartAbsenceMatchesCommand(absence, r.updateStart) &&
				observedErr == nil && issuedErr == nil && observed >= issued && observed <= received.UnixMilli()
		}
		v, e := journal.ParseUpdateStartRunningReceipt(body)
		c := r.updateStart
		if e != nil || v.Claim.Preflight == nil ||
			v.Claim.EffectID != c.EffectID || v.Claim.ResourceID != c.Payload.ResourceID ||
			v.Claim.PayloadSHA256 != c.PayloadSHA256 ||
			v.Claim.CheckpointReceiptSHA256 != c.Payload.SourceCheckpointReceiptSHA256 ||
			v.Claim.AdmittedFence != c.InstallationFence ||
			v.Claim.SandboxProfile != c.Payload.SandboxProfile ||
			!lifecycleRevisionWithin(v.Claim.ControlRevision, c.ControlRevision) ||
			v.DeploymentID != c.DeploymentID || v.ControllerEpoch != c.ControllerEpoch ||
			v.InstallationID != c.InstallationID || v.OperationID != c.OperationID ||
			v.NamespaceID != c.Payload.NamespaceID ||
			v.SourceReleaseSHA256 != c.Payload.SourceReleaseSHA256 ||
			v.TargetReleaseSHA256 != c.Payload.TargetReleaseSHA256 ||
			v.SourceGeneration != c.Payload.SourceGeneration ||
			v.TargetGeneration != c.TargetGeneration || v.ControlFence != c.InstallationFence ||
			v.SourceWriterFence != c.Payload.SourceWriterFence ||
			v.TargetWriterFence != c.Payload.TargetWriterFence ||
			v.DataFormat != c.Payload.DataFormat {
			return false
		}
		observed, observedErr := strconv.ParseInt(v.NativeObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil || observed < issued ||
			observed > received.UnixMilli() {
			return false
		}
		settled = v.SettledMS
	case action == UpdateStartProbe:
		v, e := journal.ParseUpdateStartReadiness(body)
		c := r.updateStart
		if e != nil || v.PayloadSHA256 != c.PayloadSHA256 ||
			v.DataFormat != c.Payload.DataFormat {
			return false
		}
		observed, observedErr := strconv.ParseInt(v.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil || observed < issued ||
			observed < started.UnixMilli() || observed > received.UnixMilli() {
			return false
		}
		return true
	case action == UpdateStartRegistrationDeliver || action == UpdateStartRegistrationRead:
		v, e := journal.ParseUpdateStartRegistrationReceipt(body)
		if e != nil ||
			v.Claim.EffectID != r.updateStartRegistration.EffectID ||
			v.Claim.PayloadSHA256 != r.updateStartRegistration.PayloadSHA256 ||
			v.Claim.SourceSHA256 != r.updateStartRegistration.RunningReceiptSHA256 ||
			v.Claim.DocumentSHA256 != r.updateStartRegistration.DocumentSHA256 ||
			!lifecycleRevisionWithin(v.Claim.ControlRevision, r.updateStart.ControlRevision) ||
			(v.RecoveryControlRevision != "" &&
				!lifecycleRevisionWithin(v.RecoveryControlRevision, r.updateStart.ControlRevision)) {
			return false
		}
		settled = v.SettledMS
	case action >= UpdateStartRemovalStop && action <= UpdateStartRemovalRead:
		v, e := journal.ParseManagedRemovalReceipt(body)
		c := r.updateStartRemoval
		if e != nil || v.DeploymentID != r.updateStart.DeploymentID ||
			v.ControllerEpoch != r.updateStart.ControllerEpoch ||
			v.InstallationID != r.updateStart.InstallationID ||
			v.OperationID != c.OperationID ||
			v.Stage != c.Stage || v.EffectID != c.EffectID ||
			v.PayloadSHA256 != c.PayloadSHA256 ||
			v.SourceReceiptSHA256 != c.RunningReceiptSHA256 ||
			v.SourceOperationID != r.updateStart.OperationID ||
			v.SourceEffectID != r.updateStart.EffectID ||
			v.TargetGeneration != r.updateStart.TargetGeneration ||
			v.ResourceID != r.updateStart.Payload.ResourceID ||
			v.DataNamespaceID != r.updateStart.Payload.NamespaceID ||
			v.ReleaseSHA256 != r.updateStart.Payload.TargetReleaseSHA256 {
			return false
		}
		settled = v.SettledMS
	case action == ManagedDataDelete || action == ManagedDataRead:
		v, e := journal.ParseDeleteDataReceipt(body)
		c := r.deleteData
		if e != nil || v.DeploymentID != c.DeploymentID ||
			v.ControllerEpoch != c.ControllerEpoch ||
			v.InstallationID != c.InstallationID ||
			v.OperationID != c.OperationID ||
			v.TargetGeneration != c.TargetGeneration ||
			v.EffectID != c.EffectID ||
			v.PayloadSHA256 != c.PayloadSHA256 ||
			v.NamespaceID != c.Payload.NamespaceID ||
			v.ReleaseSHA256 != c.Payload.ReleaseSHA256 ||
			v.ProjectID != c.Payload.ProjectID ||
			v.DataFormat != c.Payload.DataFormat ||
			v.QuotaBytes != c.Payload.QuotaBytes ||
			v.QuotaInodes != c.Payload.QuotaInodes ||
			v.ExpectedDevice != c.Payload.ExpectedDevice ||
			v.ExpectedNamespaceInode !=
				c.Payload.ExpectedNamespaceInode ||
			v.WriterFence != c.Payload.WriterFence {
			return false
		}
		observed, observedErr := strconv.ParseInt(
			v.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(
			c.IssuedMS, 10, 64)
		if observedErr != nil || issuedErr != nil ||
			observed < issued {
			return false
		}
		settled = v.SettledMS
	default:
		return false
	}
	n, e := strconv.ParseInt(settled, 10, 64)
	return e == nil && n <= received.UnixMilli()
}

// importAbsenceReply reports whether body is the runner's definite "never claimed" answer. Only a
// read-receipt can be answered that way; an execute that found no row would claim one instead.
func importAbsenceReply(action WritableLifecycleAction, body []byte) bool {
	if action != ImportCheckpointRead {
		return false
	}
	_, err := journal.ParseImportCheckpointAbsence(body)
	return err == nil
}

// lifecycleStatusReply decodes the typed pending, orphaned or absent answer a migrate-data, discard-migration
// or restore read-back may get instead of a receipt. Only those reads can be answered so; an execute that
// found no row would claim one instead.
func lifecycleStatusReply(action WritableLifecycleAction, body []byte) (journal.MigrateDataStatus, bool) {
	if action != MigrateDataRead && action != DiscardMigrationRead && action != RestoreRead {
		return journal.MigrateDataStatus{}, false
	}
	status, err := journal.ParseMigrateDataStatus(body)
	return status, err == nil
}

// migrateAbsenceReply reports whether body is the runner's definite "never claimed" answer. Pending and
// orphaned are not definite and stay observations.
func migrateAbsenceReply(action WritableLifecycleAction, body []byte) bool {
	status, ok := lifecycleStatusReply(action, body)
	return ok && status.State == "absent"
}

func validLifecycleStatus(action WritableLifecycleAction, status journal.MigrateDataStatus, r writableLifecycleReference, received time.Time) bool {
	var header control.CommandHeader
	var issuedMS string
	switch action {
	case MigrateDataRead:
		header, issuedMS = r.migrate.CommandHeader, r.migrate.IssuedMS
	case DiscardMigrationRead:
		header, issuedMS = r.discard.CommandHeader, r.discard.IssuedMS
	default:
		c := r.restore
		header = control.CommandHeader{
			DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
			TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID, Kind: "restore",
			PayloadSHA256: c.PayloadSHA256,
		}
		issuedMS = c.IssuedMS
	}
	observed, observedErr := strconv.ParseInt(status.ObservedMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(issuedMS, 10, 64)
	return status.Kind == header.Kind && status.DeploymentID == header.DeploymentID && status.ControllerEpoch == header.ControllerEpoch &&
		status.InstallationID == header.InstallationID && status.OperationID == header.OperationID &&
		status.TargetGeneration == header.TargetGeneration && status.Stage == header.Stage && status.EffectNumber == header.EffectNumber &&
		status.EffectID == header.EffectID && status.PayloadSHA256 == header.PayloadSHA256 &&
		observedErr == nil && issuedErr == nil && observed >= issued && observed <= received.UnixMilli()
}

// startAbsenceReply reports whether body is the runner's definite "never admitted" answer to a start
// read-receipt. Only that read can be answered so; an execute that found no row would claim one instead.
func startAbsenceReply(action WritableLifecycleAction, body []byte) bool {
	if action != UpdateStartReceipt {
		return false
	}
	_, err := journal.ParseUpdateStartAbsence(body)
	return err == nil
}

func mustLifecycleInt(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func lifecycleRevisionWithin(value, current string) bool {
	v, e := strconv.ParseInt(value, 10, 64)
	c, ce := strconv.ParseInt(current, 10, 64)
	return e == nil && ce == nil && v > 0 && v <= c
}

// validLoadImageReply accepts a typed status or a receipt, each bound to the exact signed command. A receipt is
// terminal and immutable, so a read-back is not held to the command's signing time.
func validLoadImageReply(body []byte, c control.LoadImageCommand, received time.Time) bool {
	if status, err := journal.ParseLoadImageStatus(body); err == nil {
		observed, observedErr := strconv.ParseInt(status.ObservedMS, 10, 64)
		issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
		return journal.LoadImageStatusMatchesCommand(status, c) && observedErr == nil && issuedErr == nil && observed >= issued && observed <= received.UnixMilli()
	}
	receipt, err := journal.ParseLoadImageReceipt(body)
	if err != nil || !journal.LoadImageReceiptMatchesCommand(receipt, c) {
		return false
	}
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	return settledErr == nil && settled <= received.UnixMilli()
}

// loadImageAbsenceReply reports whether body is the runner's definite "never claimed" answer. Only a read-back can
// be answered so; an execute that found no row claims one instead. Pending, orphaned and blobs_missing are not
// definite and stay observations.
func loadImageAbsenceReply(action WritableLifecycleAction, body []byte) bool {
	if action != LoadImageRead {
		return false
	}
	status, err := journal.ParseLoadImageStatus(body)
	return err == nil && status.State == "absent"
}
