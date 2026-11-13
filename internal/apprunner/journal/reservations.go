package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Reservation kinds and states of runner_reservations (doc 171).
const (
	reservationBackend   = "backend"
	reservationMigration = "migration"
	reservationNamespace = "namespace"
	reservationImage     = "image"

	reservationHeld     = "held"
	reservationReleased = "released"
)

// The descriptor maxima a backfilled reservation is held at: the largest memory
// and CPU a descriptor may declare, the 768 MiB a managed namespace reserves, and
// the largest declared image (4 GiB). An upgraded journal can therefore only
// over-count a live resource, never under-count it.
const (
	backfillBackendMemoryMiB = 4096
	backfillBackendCPUMillis = 4000
	backfillImageBytes       = int64(4) << 30
)

// Disk a managed namespace reserves: its 256 MiB quota plus two retained recovery
// checkpoints of at most 256 MiB each (doc 167 section 4).
const (
	NamespaceQuotaBytes       = int64(256) << 20
	RetainedCheckpoints       = 2
	NamespaceReservationBytes = NamespaceQuotaBytes * (1 + RetainedCheckpoints)
)

// imageResourceID is the reservation identity of a loaded image: one per config digest, however many
// installations use it.
func imageResourceID(configDigest string) string { return "image/" + configDigest }

type reservationBackfill struct {
	resource, installation, kind string
	memory, cpu, disk, image     int64
}

// upgradeReservationsV2ToV3 adds the additive reservation table and holds one
// reservation, at the descriptor maxima, for every live resource that already
// exists: a container or volume not proven absent, and every config digest an
// applied or unknown load left in the daemon. A command row whose digest cannot
// be read fails the upgrade rather than under-counting.
func upgradeReservationsV2ToV3(ctx context.Context, tx *sql.Tx) error {
	for _, product := range schemaProducts(3) {
		if strings.HasPrefix(product.name, "runner_reservations") || strings.HasPrefix(product.name, "idx_runner_reservations_") {
			if _, err := tx.ExecContext(ctx, product.sql); err != nil {
				return err
			}
		}
	}
	var backfill []reservationBackfill
	rows, err := tx.QueryContext(ctx, `SELECT id,installation_id,kind,native_id FROM runner_resources WHERE kind IN ('container','volume') AND observed_state<>'absent' ORDER BY rowid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, installation, kind, native string
		if err := rows.Scan(&id, &installation, &kind, &native); err != nil {
			rows.Close()
			return err
		}
		switch {
		case kind == "volume":
			backfill = append(backfill, reservationBackfill{resource: id, installation: installation, kind: reservationNamespace, disk: NamespaceReservationBytes})
		case strings.HasPrefix(native, "migration-job/"):
			backfill = append(backfill, reservationBackfill{resource: id, installation: installation, kind: reservationMigration, memory: backfillBackendMemoryMiB, cpu: backfillBackendCPUMillis})
		default:
			backfill = append(backfill, reservationBackfill{resource: id, installation: installation, kind: reservationBackend, memory: backfillBackendMemoryMiB, cpu: backfillBackendCPUMillis})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT installation_id,COALESCE(json_extract(receipt_json,'$.claim.config_digest'),json_extract(receipt_json,'$.config_digest')) FROM runner_commands WHERE kind='load-image' AND state IN ('applied','unknown') ORDER BY rowid`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var installation string
		var digest sql.NullString
		if err := rows.Scan(&installation, &digest); err != nil {
			rows.Close()
			return err
		}
		if !digest.Valid || !dataSHA(digest.String) {
			rows.Close()
			return fmt.Errorf("%w: a load-image row has no readable config digest", ErrCorrupt)
		}
		if seen[digest.String] {
			continue
		}
		seen[digest.String] = true
		backfill = append(backfill, reservationBackfill{resource: imageResourceID(digest.String), installation: installation, kind: reservationImage, image: backfillImageBytes})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	for _, b := range backfill {
		id, err := dataClaimID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,'held',?,?)`,
			id, b.resource, b.installation, b.kind, b.memory, b.cpu, b.disk, b.image, now, now); err != nil {
			return err
		}
	}
	return nil
}
