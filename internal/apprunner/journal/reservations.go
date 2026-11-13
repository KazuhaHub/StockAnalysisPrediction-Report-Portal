package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
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
	rows, err = tx.QueryContext(ctx, `SELECT installation_id,COALESCE(json_extract(receipt_json,'$.claim.config_digest'),json_extract(receipt_json,'$.config_digest')),COALESCE(json_extract(receipt_json,'$.claim.layers'),json_extract(receipt_json,'$.layers')) FROM runner_commands WHERE kind='load-image' AND state IN ('applied','unknown') ORDER BY rowid`)
	if err != nil {
		return err
	}
	imageAt := map[string]int{}
	for rows.Next() {
		var installation string
		var digest, layers sql.NullString
		if err := rows.Scan(&installation, &digest, &layers); err != nil {
			rows.Close()
			return err
		}
		if !digest.Valid || !dataSHA(digest.String) {
			rows.Close()
			return fmt.Errorf("%w: a load-image row has no readable config digest", ErrCorrupt)
		}
		// The declared size is in the claim; only an unreadable one falls back to the largest declared image.
		bytes := backfillImageBytes
		if parsed, ok := control.ParseLoadImageLayers(layers.String); ok && layers.Valid {
			bytes = 0
			for _, layer := range parsed {
				bytes += int64(layer.UncompressedBytes)
			}
		}
		if at, seen := imageAt[digest.String]; seen {
			// One image, counted once at the largest size any of its loads declared: the count never drops.
			backfill[at].image = max(backfill[at].image, bytes)
			continue
		}
		imageAt[digest.String] = len(backfill)
		backfill = append(backfill, reservationBackfill{resource: imageResourceID(digest.String), installation: installation, kind: reservationImage, image: bytes})
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

// LimitsObserver reads the limits a live container was created with. The runner's native layer is the only
// implementation; tests supply their own.
type LimitsObserver interface {
	ContainerLimits(ctx context.Context, nativeID string) (driver.ContainerLimits, error)
}

const estimatedRows = `state='held' AND kind IN ('backend','migration') AND memory_mib=? AND cpu_millis=?`

// EstimatedReservations counts the held backend and migration reservations still at the descriptor maxima: the
// rows an upgrade could only estimate and no live container has refined. A declared amount equal to the maxima
// is counted too, so the figure is an upper bound.
func (s *Store) EstimatedReservations(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_reservations WHERE `+estimatedRows, backfillBackendMemoryMiB, backfillBackendCPUMillis).Scan(&n)
	return n, err
}

// RefineEstimatedReservations replaces the maxima of a backfilled backend reservation with the limits of its
// live container, read from the daemon by native id. It runs once at startup, before the runner admits anything.
// A container the daemon does not have, a pending resource with no container, and a container with no limits
// keep their estimate: the amount cannot be observed, so the row stays fail-closed. A daemon that cannot answer
// is an error, and the runner is not ready. It returns the number of rows refined.
func (s *Store) RefineEstimatedReservations(ctx context.Context, observer LimitsObserver) (int, error) {
	if observer == nil {
		return 0, ErrCapacityBudgets
	}
	type candidate struct{ resource, native string }
	var targets []candidate
	rows, err := s.db.QueryContext(ctx, `SELECT r.resource_id,res.native_id FROM runner_reservations r JOIN runner_resources res ON res.id=r.resource_id
		WHERE r.kind='backend' AND r.state='held' AND r.memory_mib=? AND r.cpu_millis=? ORDER BY r.rowid`, backfillBackendMemoryMiB, backfillBackendCPUMillis)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.resource, &c.native); err != nil {
			rows.Close()
			return 0, err
		}
		if dataSHA(c.native) {
			targets = append(targets, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	refined := 0
	for _, c := range targets {
		limits, err := observer.ContainerLimits(ctx, c.native)
		if err != nil {
			return refined, err
		}
		if !limits.Found || limits.MemoryBytes <= 0 || limits.NanoCPUs <= 0 {
			continue
		}
		memory := (limits.MemoryBytes + (1<<20 - 1)) >> 20
		cpu := (limits.NanoCPUs + 999_999) / 1_000_000
		changed, err := s.db.ExecContext(ctx, `UPDATE runner_reservations SET memory_mib=?,cpu_millis=?,updated_ms=? WHERE resource_id=? AND `+estimatedRows,
			memory, cpu, time.Now().UTC().UnixMilli(), c.resource, backfillBackendMemoryMiB, backfillBackendCPUMillis)
		if err != nil {
			return refined, err
		}
		if n, _ := changed.RowsAffected(); n == 1 {
			refined++
		}
	}
	return refined, nil
}
