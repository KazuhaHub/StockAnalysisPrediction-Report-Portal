package journal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ReadControllerDeployment reads the enrolled controller identity through a read-only connection, without the
// owner lock and without creating, upgrading or writing anything. It exists for host-setup's plan, which must
// work while a runner is serving (doc 187 section 8.3). A missing or empty journal is ErrNotEnrolled; a journal
// that is not a private directory with private files, or that this build cannot read, is an error.
func ReadControllerDeployment(ctx context.Context, directory string) (Deployment, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return Deployment{}, err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return Deployment{}, ErrUnsafePath
	}
	database, err := os.Lstat(filepath.Join(directory, journalName))
	if errors.Is(err, os.ErrNotExist) {
		return Deployment{}, ErrNotEnrolled
	}
	if err != nil {
		return Deployment{}, err
	}
	if !privateRegular(database) {
		return Deployment{}, ErrUnsafePath
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := sql.Open(journalDriver, journalDSN(directory, "ro"))
	if err != nil {
		return Deployment{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	version, err := inspectAt(ctx, db, schemaVersion)
	if err != nil {
		return Deployment{}, err
	}
	if version == 0 {
		return Deployment{}, ErrNotEnrolled
	}
	return readControllerDeployment(ctx, db)
}
