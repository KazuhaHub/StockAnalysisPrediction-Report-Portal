package journal

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func dataEffectStatus(h control.CommandHeader, state string, now int64) MigrateDataStatus {
	return MigrateDataStatus{
		Format: migrateStatusFormat, Kind: h.Kind, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch,
		InstallationID: h.InstallationID, OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, Stage: h.Stage,
		EffectNumber: h.EffectNumber, EffectID: h.EffectID, PayloadSHA256: h.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(now, 10),
	}
}

// DataEffectStatusMatchesHeader reports whether the status answers exactly this signed data command.
func DataEffectStatusMatchesHeader(s MigrateDataStatus, h control.CommandHeader) bool {
	return migrateStatusMatchesHeader(s, h)
}

// readDataEffectOrStatus is the runner's read-back answer for a data effect: the receipt, or a typed status. A
// status never waits for a running dispatch: "pending" while the data execution gate is held, "orphaned" for a
// claimed effect with the gate free, and "absent" only for no row at all, proven with the gate held by this read.
func readDataEffectOrStatus[R any](s *Store, read func() (R, control.CommandHeader, error)) (any, error) {
	receipt, header, err := read()
	if err == nil {
		return receipt, nil
	}
	now := time.Now().UTC().UnixMilli()
	switch {
	case errors.Is(err, ErrNoSuchEffect):
		if !s.tryAcquireDataExecution() {
			return dataEffectStatus(header, "pending", now), nil
		}
		defer s.releaseDataExecution()
		again, againHeader, againErr := read()
		switch {
		case againErr == nil:
			return again, nil
		case errors.Is(againErr, ErrNoSuchEffect):
			return dataEffectStatus(againHeader, "absent", time.Now().UTC().UnixMilli()), nil
		case errors.Is(againErr, ErrDataCommandUnavailable):
			return dataEffectStatus(againHeader, "orphaned", time.Now().UTC().UnixMilli()), nil
		}
		return nil, againErr
	case errors.Is(err, ErrDataCommandUnavailable):
		if !s.tryAcquireDataExecution() {
			return dataEffectStatus(header, "pending", now), nil
		}
		defer s.releaseDataExecution()
		again, againHeader, againErr := read()
		switch {
		case againErr == nil:
			return again, nil
		case errors.Is(againErr, ErrDataCommandUnavailable):
			return dataEffectStatus(againHeader, "orphaned", time.Now().UTC().UnixMilli()), nil
		}
		return nil, againErr
	}
	return nil, err
}

// ReadSetDataQuotaOrStatus answers a read-back of a raise with its receipt or a typed status.
func (s *Store) ReadSetDataQuotaOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	return readDataEffectOrStatus(s, func() (SetDataQuotaReceipt, control.CommandHeader, error) {
		receipt, err := s.ReadSetDataQuotaReceipt(ctx, raw, signature)
		if err == nil {
			return receipt, control.CommandHeader{}, nil
		}
		header, headerErr := s.setDataQuotaHeader(ctx, raw, signature)
		if headerErr != nil {
			return receipt, header, headerErr
		}
		return receipt, header, err
	})
}

// ReadImportDataOrStatus answers a read-back of an import with its receipt or a typed status.
func (s *Store) ReadImportDataOrStatus(ctx context.Context, raw, signature []byte) (any, error) {
	return readDataEffectOrStatus(s, func() (ImportDataReceipt, control.CommandHeader, error) {
		receipt, err := s.ReadImportDataReceipt(ctx, raw, signature)
		if err == nil {
			return receipt, control.CommandHeader{}, nil
		}
		header, headerErr := s.importDataHeader(ctx, raw, signature)
		if headerErr != nil {
			return receipt, header, headerErr
		}
		return receipt, header, err
	})
}

func (s *Store) setDataQuotaHeader(ctx context.Context, raw, signature []byte) (control.CommandHeader, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return control.CommandHeader{}, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return control.CommandHeader{}, err
	}
	verified, err := control.VerifySetDataQuotaRead(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return control.CommandHeader{}, err
	}
	return verified.Command().CommandHeader, nil
}

func (s *Store) importDataHeader(ctx context.Context, raw, signature []byte) (control.CommandHeader, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return control.CommandHeader{}, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return control.CommandHeader{}, err
	}
	verified, err := control.VerifyImportDataRead(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return control.CommandHeader{}, err
	}
	return verified.Command().CommandHeader, nil
}
