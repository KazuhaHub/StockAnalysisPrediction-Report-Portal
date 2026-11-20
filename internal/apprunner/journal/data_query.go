package journal

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

const dataQueryReportFormat = "portal.runner.data-query-report/1-dev.1"

// DataQueryReport answers one signed data query. Only the fields of its operation are meaningful; the others are
// zero. It is never stored.
type DataQueryReport struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	EffectID         string `json:"effect_id"`
	PayloadSHA256    string `json:"payload_sha256"`
	Operation        string `json:"operation"`
	ArchiveFound     bool   `json:"archive_found"`
	ArchiveBytes     string `json:"archive_bytes"`
	ArchiveEntries   string `json:"archive_entries"`
	ContentBytes     string `json:"content_bytes"`
	SchemaMarker     string `json:"schema_marker"`
	CapacityFits     bool   `json:"capacity_fits"`
	CapacityDim      string `json:"capacity_dimension"`
	CapacityRequired string `json:"capacity_requested"`
	CapacityLeft     string `json:"capacity_remaining"`
	ObservedMS       string `json:"observed_ms"`
}

func validDataQueryReport(r DataQueryReport) bool {
	if r.Format != dataQueryReportFormat || !dataSHA(r.PayloadSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID} {
		if !enrollmentUUID.MatchString(id) {
			return false
		}
	}
	for _, value := range []string{r.ArchiveBytes, r.ArchiveEntries, r.ContentBytes, r.SchemaMarker, r.CapacityRequired, r.CapacityLeft, r.ObservedMS} {
		if !dataNumber(value, false) {
			return false
		}
	}
	switch r.Operation {
	case control.DataQueryInspectImport, control.DataQueryRemoveImport, control.DataQueryQuotaCapacity:
	default:
		return false
	}
	return r.CapacityDim == "" || validCapacityDimension(r.CapacityDim)
}

// ParseDataQueryReport accepts only the canonical encoding of a valid report.
func ParseDataQueryReport(raw []byte) (DataQueryReport, error) {
	var r DataQueryReport
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &r) || !validDataQueryReport(r) {
		return DataQueryReport{}, ErrFenceReconciliation
	}
	return r, nil
}

// DataQueryReportMatchesCommand reports whether the report answers exactly this signed query.
func DataQueryReportMatchesCommand(r DataQueryReport, c control.DataQueryCommand) bool {
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID && r.EffectID == c.EffectID && r.PayloadSHA256 == c.PayloadSHA256 && r.Operation == c.Payload.Operation
}

// DataQueryInbox is the part of the checkpoint root a query reads: the import inbox.
type DataQueryInbox interface {
	InspectImportInbox(ctx context.Context, expected data.RootIdentity, digest string, maxEntries int64) (data.ImportInboxArchive, error)
	RemoveImportArchive(ctx context.Context, expected data.RootIdentity, digest string) error
}

// AnswerDataQuery verifies and answers a data query. An inbox archive that is missing or not unique is a report with
// ArchiveFound false, never an error: the review states which.
func (s *Store) AnswerDataQuery(ctx context.Context, raw, signature []byte, inbox DataQueryInbox, pin data.RootIdentity) (DataQueryReport, error) {
	var zero DataQueryReport
	if s.closing.Load() {
		return zero, ErrDataCommandUnavailable
	}
	// The journal has one connection: it is held only to verify the query, and never across the archive work.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	authority, err := s.readAuthority(ctx, tx)
	if err != nil {
		return zero, err
	}
	verified, err := control.VerifyDataQuery(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := verified.Command()
	if c.Payload.Operation != control.DataQueryQuotaCapacity {
		if err := tx.Rollback(); err != nil {
			return zero, err
		}
	}
	report := DataQueryReport{
		Format: dataQueryReportFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID,
		OperationID: c.OperationID, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, Operation: c.Payload.Operation,
		ArchiveBytes: "0", ArchiveEntries: "0", ContentBytes: "0", SchemaMarker: "0", CapacityRequired: "0", CapacityLeft: "0",
	}
	switch c.Payload.Operation {
	case control.DataQueryQuotaCapacity:
		report.CapacityFits = true
		if plan, admit := s.namespacePlan(c.Payload.NamespaceID, c.InstallationID, mustDataInt(c.Payload.QuotaBytes), mustDataInt(c.Payload.QuotaInodes)); admit {
			refusal, err := s.admitCapacity(ctx, tx, plan, time.Now().UTC().UnixMilli())
			if err != nil {
				return zero, err
			}
			// The reservation is only measured: the transaction is never committed.
			if refusal != nil {
				report.CapacityFits, report.CapacityDim = false, refusal.dimension
				report.CapacityRequired = strconv.FormatInt(refusal.requested, 10)
				report.CapacityLeft = strconv.FormatInt(refusal.remaining, 10)
			}
		}
	case control.DataQueryInspectImport:
		if inbox == nil {
			return zero, ErrDataCommandUnavailable
		}
		found, err := inbox.InspectImportInbox(ctx, pin, c.Payload.ArchiveSHA256, mustDataInt(c.Payload.QuotaInodes))
		switch {
		case err == nil:
			report.ArchiveFound = true
			report.ArchiveBytes = strconv.FormatInt(found.Bytes, 10)
			report.ArchiveEntries = strconv.FormatInt(found.Manifest.Entries, 10)
			report.ContentBytes = strconv.FormatInt(found.Manifest.Bytes, 10)
			report.SchemaMarker = strconv.Itoa(found.SchemaMarker)
		case errors.Is(err, data.ErrImportArchiveMissing), errors.Is(err, data.ErrImportArchiveAmbiguous), errors.Is(err, data.ErrImportArchiveInvalid):
		default:
			return zero, err
		}
	case control.DataQueryRemoveImport:
		if inbox == nil {
			return zero, ErrDataCommandUnavailable
		}
		if err := s.acquireDataExecution(ctx); err != nil {
			return zero, err
		}
		defer s.releaseDataExecution()
		if err := inbox.RemoveImportArchive(ctx, pin, c.Payload.ArchiveSHA256); err != nil {
			return zero, err
		}
	}
	report.ObservedMS = strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	if !validDataQueryReport(report) {
		return zero, ErrFenceReconciliation
	}
	return report, nil
}
