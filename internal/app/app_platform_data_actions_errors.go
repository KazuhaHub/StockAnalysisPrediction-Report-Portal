package app

import (
	"errors"
	"net/http"
)

// The typed refusals of the data quota and data import actions (doc 185 section 8). Each reaches the console as its
// own error code, translated in every language.
var (
	// errPlatformDataQuotaLower is a requested quota below the current one. Version 1 only raises.
	errPlatformDataQuotaLower = errors.New("a data quota can only be raised")
	// errPlatformDataQuotaInvalid is a request outside the platform bounds, or one that changes nothing.
	errPlatformDataQuotaInvalid = errors.New("the requested data quota is not a valid raise")
	// errPlatformDataQuotaCapacity is a raise the runner has no room to reserve.
	errPlatformDataQuotaCapacity = errors.New("the runner cannot reserve the requested data quota")
	// errAppPlatformQuotaRaiseUnsettled refuses a recovery choice of a failed update whose quota raise has no verdict
	// yet: the runner may have raised the limits, and the choice depends on whether it did.
	errAppPlatformQuotaRaiseUnsettled = errors.New("the data quota raise of the update is not settled")
	// errPlatformDataImportRequiresDisabled is an import for an installation that is still running. The import
	// replaces the namespace while the backend is stopped, so the installation is disabled first.
	errPlatformDataImportRequiresDisabled = errors.New("an installation must be disabled before its data is imported")
	errPlatformDataImportArchiveMissing   = errors.New("no import archive has the reviewed digest")
	errPlatformDataImportArchiveInvalid   = errors.New("the import archive failed validation")
	errPlatformDataImportTooLarge         = errors.New("the import archive exceeds the data quota")
	errPlatformDataFormatMismatch         = errors.New("the import archive data format differs from the release")
)

// platformDataActionError maps a data action refusal to its HTTP status and error code.
func platformDataActionError(err error) (status int, code string, ok bool) {
	switch {
	case errors.Is(err, errPlatformDataQuotaLower):
		return http.StatusUnprocessableEntity, "data_quota_lower_refused", true
	case errors.Is(err, errPlatformDataQuotaInvalid):
		return http.StatusUnprocessableEntity, "data_quota_invalid", true
	case errors.Is(err, errPlatformDataQuotaCapacity):
		return http.StatusConflict, "data_quota_capacity", true
	case errors.Is(err, errPlatformDataImportRequiresDisabled):
		return http.StatusConflict, "data_import_requires_disabled", true
	case errors.Is(err, errPlatformDataImportArchiveMissing):
		return http.StatusUnprocessableEntity, "data_import_archive_missing", true
	case errors.Is(err, errPlatformDataImportArchiveInvalid):
		return http.StatusUnprocessableEntity, "data_import_archive_invalid", true
	case errors.Is(err, errPlatformDataImportTooLarge):
		return http.StatusUnprocessableEntity, "data_import_too_large", true
	case errors.Is(err, errPlatformDataFormatMismatch):
		return http.StatusUnprocessableEntity, "data_format_mismatch", true
	}
	return 0, "", false
}
