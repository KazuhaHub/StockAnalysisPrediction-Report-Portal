package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Pruning recovery sets (doc 172 section 6).
//
// The retention policy keeps the latest N verified recovery sets of an installation. A set beyond that, and a set of a
// namespace the delete-data stage already removed, still owns a checkpoint archive on the runner. This driver asks the
// runner to delete that archive, one signed delete-checkpoint command per set, and only then lets the Portal forget
// the set.
//
// The effect. Kind delete_checkpoint, journal stage "remove_data", effect number 2 to 32 (number 1 of that stage is
// the delete-data effect), hosted by the latest finished operation of the installation, exactly as a remove-image
// effect is. There is no new stage and no new table: the effect row is the durable two-phase marker. The row is
// written, under the admission locks, before the command leaves the Portal, and from that commit on the set is
// treated as gone (platformRetainedRecoverySetsOf and the restore and import admissions read the row), because the
// runner's claim is irreversible.
//
// The id of the effect is derived from the set, its checkpoint and the attempt number only, never from the host
// operation, so every tick and every restart signs the same id for the same attempt and a lost receipt is read back
// under it. A refusal that may pass (the checkpoint is the newest of a live namespace, or is being restored) is tried
// again later under the next attempt number; a refusal that will not (the runner's row differs, the archive is
// unrecorded, the checkpoint is not verified) is reported to an operator and not tried again.

const platformDeleteCheckpointKind = "delete_checkpoint"

const (
	// platformPruneWindow bounds how many deletions one tick works on.
	platformPruneWindow = 4
	// platformPruneMaxAttempts bounds the tries of one set after refusals that may pass.
	platformPruneMaxAttempts = 8
	// platformPruneMaxNumber is the highest effect number a deletion may take (the journal bound).
	platformPruneMaxNumber = 32

	platformPruneSourceRetention        = "retention"
	platformPruneSourceDeletedNamespace = "deleted_namespace"
)

// platformPruneBackoffDefault is how long a refused deletion waits before its next attempt, per attempt already made,
// capped at platformReclaimBackoffCap. Tests set platformPruneBackoff to zero.
const platformPruneBackoffDefault = 10 * time.Minute

var platformPruneBackoff = platformPruneBackoffDefault

func platformPruneWait(attempts int) time.Duration {
	return min(time.Duration(attempts)*platformPruneBackoff, platformReclaimBackoffCap)
}

var (
	// errPlatformPruneUncertain is a deletion whose outcome is not known; the same effect is read again.
	errPlatformPruneUncertain = errors.New("the outcome of the checkpoint deletion is not known")
	// errPlatformPruneNotEligible is not a failure: under the admission locks the set no longer qualifies.
	errPlatformPruneNotEligible = errors.New("the recovery set no longer qualifies for deletion")
)

// platformPruneRetryable lists the refusals that may pass.
func platformPruneRetryable(outcome string) bool {
	switch outcome {
	case journal.DeleteOutcomeLatest, journal.DeleteOutcomeInUse:
		return true
	}
	return false
}

// platformPruneSuperseded is the outcome of an effect that a later attempt of the same set settled: the later verdict
// proves what the earlier claim did, and the earlier row says nothing more.
func platformPruneSuperseded(outcome string) bool { return strings.HasPrefix(outcome, "superseded_") }

// platformDeletionStates reads the delete_checkpoint effects in the given states (a SQL list) and returns the ids of the
// recovery sets their receipts name. The set id is carried by every receipt shape of the effect (the intent written
// before the command is sent, the runner's receipt and the Portal's own closures), always as the JSON member
// "recovery_set_id". The read is one indexed pass over the effects in those states, not a scan per set.
func platformDeletionStates(e migExec, states string) (map[string]bool, error) {
	rows, err := e.query(`SELECT receipt_json FROM app_platform_operation_effects WHERE kind=? AND state IN (`+states+`)`, platformDeleteCheckpointKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var named struct {
			RecoverySetID string `json:"recovery_set_id"`
		}
		if json.Unmarshal([]byte(raw), &named) == nil && platformValidUUID(named.RecoverySetID) {
			out[named.RecoverySetID] = true
		}
	}
	return out, rows.Err()
}

// platformUUIDList is the SQL list of validated ids. Only a string that passes platformValidUUID is written, so the
// list cannot carry anything but ids.
func platformUUIDList(ids []string) string {
	var list []string
	for _, id := range ids {
		if platformValidUUID(id) {
			list = append(list, `'`+id+`'`)
		}
	}
	return strings.Join(list, ",")
}

// platformRecoverySetDeletionDecided reports whether a deletion of the set has been decided: an effect that names it is
// prepared, unknown or applied. A refused (reverted) effect does not decide anything. Restore admission, import-grant
// issuance and restore-from-import refuse a decided set.
func platformRecoverySetDeletionDecided(e migExec, id string) (bool, error) {
	sets, err := platformDeletionStates(e, `'prepared','unknown','applied'`)
	return sets[id], err
}

type platformPruneIntent struct {
	RecoverySetID string `json:"recovery_set_id"`
	CheckpointID  string `json:"checkpoint_id"`
	// Source is why the set is pruned: platformPruneSourceRetention or platformPruneSourceDeletedNamespace.
	Source string `json:"source"`
	// Attempt numbers the tries after a refusal; it is part of the effect id, so a retry is a new effect.
	Attempt int `json:"attempt"`
	// Stranded marks an open effect whose host operation can no longer settle it (the installation's fence moved).
	// The effect stays open, so the set stays decided, and a new attempt on the installation's current host
	// settles the question (see strandDeletion).
	Stranded bool `json:"stranded,omitempty"`
}

func platformPruneEffectID(setID, checkpointID string, attempt int) string {
	return platformDerivedUUID("delete-checkpoint", setID+"/"+checkpointID+"/"+strconv.Itoa(attempt))
}

type platformPruneSet struct {
	id, installation, namespace, operation, release, manifest, checkpoint, state string
	bytes, dataFormat, writerFence, created                                      int64
}

func loadPlatformPruneSet(e migExec, id string) (platformPruneSet, error) {
	var s platformPruneSet
	var manifest sql.NullString
	err := e.queryRow(`SELECT id,installation_id,data_namespace_id,operation_id,release_sha256,manifest_sha256,checkpoint_id,state,bytes,data_format,writer_fence,created_ms
		FROM app_platform_recovery_sets WHERE id=?`, id).Scan(&s.id, &s.installation, &s.namespace, &s.operation, &s.release, &manifest, &s.checkpoint, &s.state,
		&s.bytes, &s.dataFormat, &s.writerFence, &s.created)
	if err != nil {
		return s, err
	}
	if !manifest.Valid || !platformValidSHA(manifest.String) || s.bytes <= 0 || s.bytes > control.MaxDeleteCheckpointArchiveBytes || s.dataFormat < 1 || s.writerFence < 1 {
		return s, errPlatformPruneNotEligible
	}
	s.manifest = manifest.String
	return s, nil
}

// platformPruneTarget is one deletion to carry out, new or already begun.
type platformPruneTarget struct {
	set           platformPruneSet
	intent        platformPruneIntent
	hostOperation string
	installation  string
	runtimeID     string
	effectID      string
	effectNumber  int64
	// state is "" for a deletion not begun, otherwise the Portal row's open state ("prepared" or "unknown").
	state string
	// retry marks a new attempt for a set whose earlier effects are all stranded: the decision to delete was already
	// made and cannot be undone, so it is not qualified again.
	retry bool
}

// platformPruneRow is one delete_checkpoint effect row as the history reads it.
type platformPruneRow struct {
	effectID, operation, runtimeID, state, setID, outcome string
	number, updated                                       int64
	intent                                                platformPruneIntent
	stranded                                              bool
}

// platformPruneHistory reads every delete_checkpoint row, grouped by the set it names. A row whose receipt names no
// set is returned in unparsable: it cannot be attributed, so it is never acted on.
func platformPruneHistory(e migExec) (bySet map[string][]platformPruneRow, open []platformPruneRow, err error) {
	rows, err := e.query(`SELECT f.effect_id,f.operation_id,COALESCE(f.runtime_id,''),f.state,f.effect_number,f.updated_ms,f.receipt_json
		FROM app_platform_operation_effects f WHERE f.kind='` + platformDeleteCheckpointKind + `' ORDER BY f.updated_ms,f.effect_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	bySet = map[string][]platformPruneRow{}
	for rows.Next() {
		var r platformPruneRow
		var raw string
		if err := rows.Scan(&r.effectID, &r.operation, &r.runtimeID, &r.state, &r.number, &r.updated, &raw); err != nil {
			return nil, nil, err
		}
		var parsed struct {
			RecoverySetID string `json:"recovery_set_id"`
			CheckpointID  string `json:"checkpoint_id"`
			Source        string `json:"source"`
			Attempt       int    `json:"attempt"`
			Outcome       string `json:"outcome"`
			Stranded      bool   `json:"stranded"`
		}
		if json.Unmarshal([]byte(raw), &parsed) != nil || !platformValidUUID(parsed.RecoverySetID) {
			continue
		}
		r.setID, r.outcome, r.stranded = parsed.RecoverySetID, parsed.Outcome, parsed.Stranded
		r.intent = platformPruneIntent{RecoverySetID: parsed.RecoverySetID, CheckpointID: parsed.CheckpointID, Source: parsed.Source, Attempt: parsed.Attempt, Stranded: parsed.Stranded}
		bySet[r.setID] = append(bySet[r.setID], r)
		if r.state == "prepared" || r.state == "unknown" {
			open = append(open, r)
		}
	}
	return bySet, open, rows.Err()
}

// platformPruneIdle reports whether the installation can host a deletion: no operation is running and none ended
// recovery_required, whose recovery reads the installation's checkpoints.
func platformPruneIdle(e migExec, installation string) (bool, error) {
	var n int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND (completed_ms IS NULL OR stage='recovery_required')`, installation).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// platformPruneInstallationRuntime is the runtime of the installation when it is an enabled binding.
func platformPruneInstallationRuntime(e migExec, installation string) (string, error) {
	var runtimeID sql.NullString
	err := e.queryRow(`SELECT i.runtime_id FROM app_platform_installations i WHERE i.id=? AND i.runtime_id IN (SELECT id FROM app_platform_runtime_bindings WHERE enabled=1)`, installation).Scan(&runtimeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return runtimeID.String, err
}

// platformPruneNumber is the next free effect number of the remove_data stage on the host operation, never below 2.
// ok is false when the stage has no free number.
func platformPruneNumber(e migExec, operation string) (number int64, ok bool, err error) {
	var highest int64
	if err := e.queryRow(`SELECT COALESCE(MAX(effect_number),1) FROM app_platform_operation_effects WHERE operation_id=? AND stage=?`, operation, control.DeleteCheckpointStage).Scan(&highest); err != nil {
		return 0, false, err
	}
	number = max(highest+1, control.MinDeleteCheckpointEffectNumber)
	return number, number <= platformPruneMaxNumber, nil
}

// platformPruneRetryReserve is how many effect numbers of a host operation a retry never takes: a refusal that keeps
// repeating must not use the numbers new deletions need, so a retry waits for the installation's next operation
// instead.
const platformPruneRetryReserve = 8

// platformPruneCandidates lists the sets to start deleting now, oldest first (the reverse of the runner's order, so the
// least recent checkpoint goes first), each with its host operation and the effect id of its next attempt. A set with a
// deletion that is open or applied, a refusal that is not retried, a backoff still running, a grant, a restore or an
// installation that is not idle is left out. A set whose earlier effects are all stranded is a retry: the decision was
// made, so it is not qualified again. exhausted names the hosts that had no free effect number.
func (s *Server) platformPruneCandidates(e migExec, keep int, history map[string][]platformPruneRow, now int64) (out []platformPruneTarget, exhausted []string, err error) {
	beyond, err := platformBeyondRetention(e, keep)
	if err != nil {
		return nil, nil, err
	}
	type candidate struct {
		id, source string
		set        platformPruneSet
	}
	chosen := map[string]*candidate{}
	add := func(id, source string) {
		if chosen[id] == nil {
			chosen[id] = &candidate{id: id, source: source}
		}
	}
	for _, b := range beyond {
		add(b.id, platformPruneSourceRetention)
	}
	leak, err := platformDeletedNamespaceSets(e)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range leak {
		add(b.id, platformPruneSourceDeletedNamespace)
	}
	for id, rows := range history {
		for _, r := range rows {
			if r.stranded && (r.state == "prepared" || r.state == "unknown") {
				add(id, r.intent.Source)
			}
		}
	}
	var all []*candidate
	for id, c := range chosen {
		set, err := loadPlatformPruneSet(e, id)
		if errors.Is(err, errPlatformPruneNotEligible) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		c.set = set
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].set.created != all[j].set.created {
			return all[i].set.created < all[j].set.created
		}
		return all[i].set.checkpoint > all[j].set.checkpoint
	})
	taken := map[string]int64{}
	for _, c := range all {
		set := c.set
		rows := history[c.id]
		blocked, retry := false, false
		var last int64
		lastOutcome := ""
		for _, r := range rows {
			switch {
			case r.state == "applied":
				blocked = true
			case r.state == "prepared" || r.state == "unknown":
				if r.stranded {
					retry = true
				} else {
					blocked = true
				}
			case !platformPruneSuperseded(r.outcome) && r.updated >= last:
				last, lastOutcome = r.updated, r.outcome
			}
		}
		attempts := len(rows)
		if blocked || attempts >= platformPruneMaxAttempts {
			continue
		}
		if !retry && attempts > 0 && (!platformPruneRetryable(lastOutcome) || attempts >= platformPruneMaxAttempts || now < last+platformPruneWait(attempts).Milliseconds()) {
			continue
		}
		idle, err := platformPruneIdle(e, set.installation)
		if err != nil || !idle {
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		host, ok, err := platformReclaimHost(e, set.installation)
		if err != nil {
			return nil, nil, err
		}
		runtimeID, err := platformPruneInstallationRuntime(e, set.installation)
		if err != nil {
			return nil, nil, err
		}
		if !ok || runtimeID == "" {
			continue
		}
		number, _, err := platformPruneNumber(e, host)
		if err != nil {
			return nil, nil, err
		}
		// Rows of this driver that were selected earlier in the same pass take their numbers too.
		if taken[host] != 0 {
			number = taken[host] + 1
		}
		limit := int64(platformPruneMaxNumber)
		if attempts > 0 {
			limit -= platformPruneRetryReserve
		}
		if number > limit {
			exhausted = append(exhausted, host)
			continue
		}
		attempt := attempts
		effect := platformPruneEffectID(set.id, set.checkpoint, attempt)
		for ; attempt < attempts+platformPruneMaxAttempts; attempt++ {
			effect = platformPruneEffectID(set.id, set.checkpoint, attempt)
			var exists int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE effect_id=?`, effect).Scan(&exists); err != nil {
				return nil, nil, err
			}
			if exists == 0 {
				break
			}
		}
		taken[host] = number
		out = append(out, platformPruneTarget{
			set: set, intent: platformPruneIntent{RecoverySetID: set.id, CheckpointID: set.checkpoint, Source: c.source, Attempt: attempt},
			hostOperation: host, installation: set.installation, runtimeID: runtimeID, effectID: effect, effectNumber: number, retry: retry,
		})
	}
	return out, exhausted, nil
}

// platformDeletedNamespaceSets lists the sets the delete-data stage marked deleted for a namespace that is deleted:
// the stage removes the namespace on the runner but never a checkpoint archive, so these sets still own one. A set
// held by an import grant that is issued or consumed is left out, as the retention selection leaves it out.
func platformDeletedNamespaceSets(e migExec) ([]platformBeyondSet, error) {
	rows, err := e.query(`SELECT r.id,r.installation_id,r.state,r.created_ms FROM app_platform_recovery_sets r
		JOIN app_platform_data_namespaces n ON n.id=r.data_namespace_id
		WHERE r.state='deleted' AND n.state='deleted' AND r.manifest_sha256 IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM app_platform_import_grants g WHERE g.recovery_set_id=r.id AND g.state IN ('issued','consumed'))
		ORDER BY r.created_ms,r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []platformBeyondSet
	for rows.Next() {
		var b platformBeyondSet
		if err := rows.Scan(&b.id, &b.installation, &b.state, &b.created); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// platformPruneCallFor is a signed delete-checkpoint command with its read-back signature.
type platformDeleteCheckpointCall struct {
	command                       control.DeleteCheckpointCommand
	raw, signature, readSignature []byte
	authority                     control.Authority
	descriptor                    []byte
}

// platformDeleteCheckpointReply is what the transport proves about a reply to a delete-checkpoint command or its
// read-back.
type platformDeleteCheckpointReply interface {
	platformLifecycleProof
	DeleteCheckpointReceipt() (journal.DeleteCheckpointReceipt, error)
	DeleteCheckpointStatus() (journal.DeleteCheckpointStatus, error)
}

// platformPruneRunner is the runner seam of the delete-checkpoint command. Every read returns errPlatformMigrationAbsent
// for the runner's typed absence and never for anything else.
type platformPruneRunner interface {
	DeleteCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error)
	ReadDeleteCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error)
}

func (s *Server) platformPruneRunnerSeam() platformPruneRunner {
	if s.platformPruneRunner != nil {
		return s.platformPruneRunner
	}
	return platformImageTransport{}
}

func (platformImageTransport) DeleteCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.DeleteCheckpointExecute, call.raw, call.signature, call.authority, call.descriptor)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformImageTransport) ReadDeleteCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.DeleteCheckpointRead, call.raw, call.readSignature, call.authority, call.descriptor)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

func (g *platformImageSigner) deleteCheckpointCall(t platformPruneTarget, descriptor []byte) (platformDeleteCheckpointCall, error) {
	var zero platformDeleteCheckpointCall
	now := time.Now().UnixMilli()
	expires := min(now+25000, g.lease.leaseUntilMS-1000)
	if expires <= now {
		return zero, errAppPlatformLeaseConflict
	}
	command := control.DeleteCheckpointCommand{
		CommandHeader: control.CommandHeader{
			Format: control.DeleteCheckpointFormat, DeploymentID: g.runtime.deploymentID, ControllerEpoch: g.lease.controllerEpoch,
			ControlRevision: strconv.FormatInt(g.runtime.controlRevision, 10), InstallationID: g.lease.installationID,
			OperationID: g.lease.id, TargetGeneration: strconv.FormatUint(g.lease.snapshot.Generation, 10),
			Stage: control.DeleteCheckpointStage, EffectNumber: strconv.FormatInt(t.effectNumber, 10),
			EffectID: t.effectID, Kind: control.DeleteCheckpointKind, InstallationFence: strconv.FormatInt(g.lease.fence, 10),
		},
		Payload: control.DeleteCheckpointPayload{
			RecoverySetID: t.set.id, CheckpointID: t.set.checkpoint, NamespaceID: t.set.namespace, SourceOperationID: t.set.operation,
			ReleaseSHA256: t.set.release, ManifestSHA256: t.set.manifest, ArchiveBytes: strconv.FormatInt(t.set.bytes, 10),
			DataFormat: strconv.FormatInt(t.set.dataFormat, 10), WriterFence: strconv.FormatInt(t.set.writerFence, 10),
		},
	}
	digest, err := control.DeleteCheckpointLogicalDigest(command)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(expires, 10)
	raw, signature, err := control.SignDeleteCheckpoint(command, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	read, err := control.SignDeleteCheckpointRead(raw, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	return platformDeleteCheckpointCall{command: command, raw: raw, signature: signature, readSignature: read, authority: g.authority, descriptor: bytes.Clone(descriptor)}, nil
}

// pruneTx runs fn in one transaction under the locks operation admission takes, in their order (the OU mutation lock,
// then the publisher trust lock). guard, when given, confirms that the host operation is still what the command was
// derived from. pause re-reads the restore pause and fails closed.
func (s *Server) pruneTx(ctx context.Context, lease *platformOperationLease, pause bool, fn func(e migExec, now int64) error) error {
	for attempt := 0; ; attempt++ {
		err := s.pruneTxOnce(ctx, lease, pause, fn)
		if err == nil || !isContentionErr(err) || attempt == 5 || ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
	}
}

func (s *Server) pruneTxOnce(ctx context.Context, lease *platformOperationLease, pause bool, fn func(e migExec, now int64) error) error {
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	if pause {
		paused, err := platformRecoveryPaused(e)
		if err != nil {
			return err
		}
		if paused {
			return errPlatformRetentionPaused
		}
	}
	var now int64
	if lease != nil {
		if now, err = platformLoadSettlementGuard(e, *lease); err != nil {
			return err
		}
	} else if now, err = platformDatabaseNow(e); err != nil {
		return err
	}
	if err := fn(e, now); err != nil {
		return err
	}
	return tx.Commit()
}

// prepareDeletion writes the effect row, the decision, before the first command is sent. Every condition that
// selected the set is read again under the locks, so a set an admission took first is not deleted, and a set whose
// deletion is decided first is refused by that admission. It reports the state an earlier attempt left.
func (s *Server) prepareDeletion(ctx context.Context, lease platformOperationLease, runtimeID string, t platformPruneTarget, c control.DeleteCheckpointCommand) (state string, err error) {
	raw, err := json.Marshal(t.intent)
	if err != nil {
		return "", err
	}
	// The retention setting is read before the transaction: the store's pool is not reachable from inside it.
	keep := s.platformRetentionConfigLoad().RecoverySets
	err = s.pruneTx(ctx, &lease, t.state == "", func(e migExec, now int64) error {
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var id, kind, digest string
		qerr := e.queryRow(`SELECT effect_id,kind,payload_sha256,state FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?`+lock,
			lease.id, control.DeleteCheckpointStage, t.effectNumber).Scan(&id, &kind, &digest, &state)
		if qerr == nil {
			if kind != platformDeleteCheckpointKind || id != c.EffectID || digest != c.PayloadSHA256 {
				return errAppPlatformStageEvidence
			}
			return nil
		}
		if !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		if t.state != "" {
			// A begun deletion whose row is gone cannot be sent: the decision is the row.
			return errAppPlatformStageEvidence
		}
		if err := s.checkPruneEligible(e, t, lease, keep); err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,0,'prepared',?,?,?)`, lease.id, control.DeleteCheckpointStage, t.effectNumber, c.EffectID, platformDeleteCheckpointKind, c.PayloadSHA256,
			runtimeID, lease.controllerEpoch, lease.fence, string(raw), now, now); err != nil {
			return err
		}
		state = "prepared"
		return nil
	})
	return state, err
}

// checkPruneEligible is the admission, run in the transaction that inserts the row.
func (s *Server) checkPruneEligible(e migExec, t platformPruneTarget, lease platformOperationLease, keep int) error {
	current, err := loadPlatformPruneSet(e, t.set.id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errPlatformPruneNotEligible
		}
		return err
	}
	if current != t.set {
		return errPlatformPruneNotEligible
	}
	if t.retry {
		// The decision was made when the first effect was written and cannot be undone, so a retry is not qualified
		// again. It needs every earlier effect of the set to be stranded and none applied, read under the locks.
		history, _, err := platformPruneHistory(e)
		if err != nil {
			return err
		}
		stranded := false
		for _, r := range history[current.id] {
			switch {
			case r.state == "applied":
				return errPlatformPruneNotEligible
			case r.state == "prepared" || r.state == "unknown":
				if !r.stranded {
					return errPlatformPruneNotEligible
				}
				stranded = true
			}
		}
		if !stranded || current.state == "restoring" {
			return errPlatformPruneNotEligible
		}
	} else {
		switch t.intent.Source {
		case platformPruneSourceRetention:
			if current.state != "verified" {
				return errPlatformPruneNotEligible
			}
			beyond, err := platformBeyondRetention(e, keep)
			if err != nil {
				return err
			}
			found := false
			for _, b := range beyond {
				found = found || b.id == current.id
			}
			if !found {
				return errPlatformPruneNotEligible
			}
		case platformPruneSourceDeletedNamespace:
			leak, err := platformDeletedNamespaceSets(e)
			if err != nil {
				return err
			}
			found := false
			for _, b := range leak {
				found = found || b.id == current.id
			}
			if !found {
				return errPlatformPruneNotEligible
			}
		default:
			return errPlatformPruneNotEligible
		}
		if decided, err := platformRecoverySetDeletionDecided(e, current.id); err != nil || decided {
			if err != nil {
				return err
			}
			return errPlatformPruneNotEligible
		}
	}
	idle, err := platformPruneIdle(e, current.installation)
	if err != nil || !idle {
		if err != nil {
			return err
		}
		return errPlatformPruneNotEligible
	}
	host, ok, err := platformReclaimHost(e, current.installation)
	if err != nil || !ok || host != lease.id {
		if err != nil {
			return err
		}
		return errPlatformPruneNotEligible
	}
	number, free, err := platformPruneNumber(e, host)
	if err != nil || !free || number > t.effectNumber || (t.intent.Attempt > 0 && t.effectNumber > platformPruneMaxNumber-platformPruneRetryReserve) {
		if err != nil {
			return err
		}
		return errPlatformPruneNotEligible
	}
	return nil
}

// settleDeletion records the runner's verdict. It runs under the same locks as the decision. An applied deletion
// also retires the set on the Portal and writes the audit row; a refusal leaves the set as it is.
func (s *Server) settleDeletion(ctx context.Context, lease platformOperationLease, t platformPruneTarget, receipt journal.DeleteCheckpointReceipt, raw []byte) error {
	if len(raw) > 16384 {
		return errAppPlatformStageEvidence
	}
	state := "reverted"
	if receipt.Applied() {
		state = "applied"
	}
	return s.pruneTx(ctx, nil, false, func(e migExec, now int64) error {
		res, err := e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
			state, lease.fence, string(raw), now, lease.id, control.DeleteCheckpointStage, t.effectNumber, t.effectID, platformDeleteCheckpointKind)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another settlement already recorded a verdict for this effect.
			return nil
		}
		// The verdict of one attempt proves what the claim of an earlier, stranded one did: a claim marks the runner's
		// checkpoint deleted before it touches the file, so after a refusal the earlier effect was never claimed, and
		// after an applied receipt the set is gone whichever effect removed it. Either way the earlier rows are closed.
		history, _, err := platformPruneHistory(e)
		if err != nil {
			return err
		}
		closure := "superseded_not_claimed"
		if state == "applied" {
			closure = "superseded_applied"
		}
		for _, r := range history[t.set.id] {
			if r.effectID == t.effectID || (r.state != "prepared" && r.state != "unknown") {
				continue
			}
			closed, _ := json.Marshal(struct {
				RecoverySetID string `json:"recovery_set_id"`
				CheckpointID  string `json:"checkpoint_id"`
				Source        string `json:"source"`
				Attempt       int    `json:"attempt"`
				Outcome       string `json:"outcome"`
			}{r.intent.RecoverySetID, r.intent.CheckpointID, r.intent.Source, r.intent.Attempt, closure})
			if _, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
				string(closed), now, r.effectID, platformDeleteCheckpointKind); err != nil {
				return err
			}
		}
		action := "app_platform.recovery_set_pruned"
		switch {
		case state == "applied":
			if _, err := e.exec(`UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=?,updated_ms=? WHERE id=? AND state<>'deleted'`, now, now, t.set.id); err != nil {
				return err
			}
		case !platformPruneRetryable(receipt.Outcome):
			action = "app_platform.recovery_set_prune_refused"
		default:
			return nil
		}
		detail, _ := json.Marshal(struct {
			EffectID string `json:"effect_id"`
			Source   string `json:"source"`
			Outcome  string `json:"outcome"`
		}{t.effectID, t.intent.Source, receipt.Outcome})
		_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_recovery_set',?,?,'')`,
			time.Now().UTC().Format(time.RFC3339), action, t.set.id, string(detail))
		return err
	})
}

// strandDeletion records that the host operation of an open deletion can no longer settle it: the installation's fence
// moved after the command was written. The row is never reverted, because the runner may have claimed it: it stays
// open (unknown), so the set stays decided and neither restore nor the recovery bundle offers it. The next pass signs a
// new attempt on the installation's current host operation, whose verdict closes this row (settleDeletion).
func (s *Server) strandDeletion(ctx context.Context, t platformPruneTarget) error {
	intent := t.intent
	intent.Stranded = true
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return s.pruneTx(ctx, nil, false, func(e migExec, now int64) error {
		_, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',receipt_json=? WHERE operation_id=? AND effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
			string(raw), t.hostOperation, t.effectID, platformDeleteCheckpointKind)
		return err
	})
}

// markDeletionUnknown marks a deletion that was sent and has no verdict. Best effort: a failure leaves the row
// prepared, which the settlement treats the same way.
func (s *Server) markDeletionUnknown(ctx context.Context, t platformPruneTarget) {
	if _, err := s.st.execContext(ctx, `UPDATE app_platform_operation_effects SET state='unknown',updated_ms=? WHERE operation_id=? AND effect_id=? AND kind=? AND state='prepared'`,
		time.Now().UnixMilli(), t.hostOperation, t.effectID, platformDeleteCheckpointKind); err != nil {
		log.Printf("app-platform recovery set prune %s: %v", t.effectID, err)
	}
}

// interpretPlatformDeleteReply turns a reply into a verified receipt or status. A reply is believed only when it
// answers exactly the command that was signed.
func (s *Server) interpretPlatformDeleteReply(reply platformDeleteCheckpointReply, call platformDeleteCheckpointCall, endpoint platformRuntimeEndpoint) (*journal.DeleteCheckpointReceipt, *journal.DeleteCheckpointStatus, error) {
	if r, rerr := reply.DeleteCheckpointReceipt(); rerr == nil {
		if !journal.DeleteCheckpointReceiptMatchesCommand(r, call.command) ||
			!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, r.ObservedMS, r.SettledMS, call.command.IssuedMS, false) {
			return nil, nil, errAppPlatformStageEvidence
		}
		return &r, nil, nil
	}
	status, serr := reply.DeleteCheckpointStatus()
	if serr != nil || !journal.DeleteCheckpointStatusMatchesCommand(status, call.command) ||
		!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, status.ObservedMS, "", call.command.IssuedMS, true) {
		return nil, nil, errAppPlatformStageEvidence
	}
	return nil, &status, nil
}

// pruneOutcome is how one deletion step ended.
type pruneOutcome int

const (
	pruneLeft pruneOutcome = iota
	pruneSettled
	pruneBusy
	pruneUnclaimed
	pruneExhausted
)

// pruneRecoverySet drives one deletion to a verdict, or leaves it for the next tick.
func (s *Server) pruneRecoverySet(ctx context.Context, endpoint platformRuntimeEndpoint, t platformPruneTarget) (pruneOutcome, error) {
	lease, err := platformLoadSettlementLease(migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}, t.hostOperation)
	if err != nil {
		if t.state != "" && errors.Is(err, errAppPlatformStageEvidence) {
			return pruneLeft, s.strandDeletion(ctx, t)
		}
		return pruneLeft, err
	}
	guard := func(e migExec) error { _, err := platformLoadSettlementGuard(e, lease); return err }
	signer, err := s.newPlatformImageSignerGuarded(ctx, lease, guard)
	if err != nil {
		if errors.Is(err, errAppPlatformLeaseConflict) || errors.Is(err, errAppPlatformStageEvidence) {
			if t.state != "" {
				// The installation's fence moved after the decision: this host can no longer settle it. The row stays
				// open, so the set stays decided; the next attempt is signed by the installation's new latest operation.
				log.Printf("app-platform recovery set prune %s: the installation's fence moved; the row is stranded and a new attempt will settle it", t.effectID)
				return pruneLeft, s.strandDeletion(ctx, t)
			}
			return pruneLeft, nil
		}
		return pruneLeft, err
	}
	defer signer.close()
	if t.runtimeID != "" && t.runtimeID != signer.runtime.id {
		// The set lives on another runtime than the one this endpoint serves.
		return pruneLeft, nil
	}
	var descriptor []byte
	if err := s.st.queryRowContext(ctx, `SELECT descriptor FROM app_platform_releases WHERE sha256=?`, t.set.release).Scan(&descriptor); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return pruneLeft, err
	}
	if len(descriptor) == 0 {
		// The transport refuses a lifecycle action with no release descriptor, so nothing can be sent for this set. A
		// deletion that is already open stays open (its claim may exist), which the storage status reports once it is
		// stale; an operator has to restore the release record.
		log.Printf("app-platform recovery set prune: recovery set %s names release %s, which has no stored descriptor; it is not deleted", t.set.id, t.set.release)
		return pruneLeft, nil
	}
	call, err := signer.deleteCheckpointCall(t, descriptor)
	if err != nil {
		return pruneLeft, err
	}
	state, err := s.prepareDeletion(ctx, lease, signer.runtime.id, t, call.command)
	if t.state != "" && errors.Is(err, errAppPlatformStageEvidence) {
		// The row was written under another fence than the host now carries, so its command can no longer be signed.
		return pruneLeft, s.strandDeletion(ctx, t)
	}
	if errors.Is(err, errPlatformPruneNotEligible) || errors.Is(err, errPlatformRetentionPaused) {
		return pruneLeft, nil
	}
	if err != nil {
		return pruneLeft, err
	}
	if state == "applied" || state == "reverted" {
		return pruneSettled, nil
	}
	t.state = state
	runner := s.platformPruneRunnerSeam()
	record := func(r *journal.DeleteCheckpointReceipt) (pruneOutcome, error) {
		raw, err := json.Marshal(r)
		if err != nil {
			return pruneLeft, err
		}
		return pruneSettled, s.settleDeletion(ctx, lease, t, *r, raw)
	}
	if state == "unknown" {
		// An unknown deletion is read back first: a receipt settles it, and only a runner that holds nothing running it
		// is asked to complete it, under its own claim.
		reply, readErr := runner.ReadDeleteCheckpoint(ctx, endpoint, call)
		if readErr == nil && reply != nil {
			receipt, status, ierr := s.interpretPlatformDeleteReply(reply, call, endpoint)
			if ierr != nil {
				return pruneLeft, ierr
			}
			if receipt != nil {
				return record(receipt)
			}
			if status.State == "pending" {
				return pruneBusy, nil
			}
		} else if readErr != nil && !errors.Is(readErr, errPlatformMigrationAbsent) {
			return pruneLeft, readErr
		}
	}
	reply, err := runner.DeleteCheckpoint(ctx, endpoint, call)
	if err != nil {
		// An uncertain execute is read back under its own identity. It is never sent again under a new one.
		reply, readErr := runner.ReadDeleteCheckpoint(ctx, endpoint, call)
		if errors.Is(readErr, errPlatformMigrationAbsent) {
			// The runner proved it never claimed the effect. The row stays prepared and the same id is sent again later.
			return pruneUnclaimed, nil
		}
		if readErr != nil || reply == nil {
			s.markDeletionUnknown(ctx, t)
			if ctx.Err() != nil {
				return pruneBusy, nil
			}
			return pruneLeft, fmt.Errorf("%w: %v", errPlatformPruneUncertain, err)
		}
		return s.interpretDeleteStep(reply, call, endpoint, t, record)
	}
	return s.interpretDeleteStep(reply, call, endpoint, t, record)
}

func (s *Server) interpretDeleteStep(reply platformDeleteCheckpointReply, call platformDeleteCheckpointCall, endpoint platformRuntimeEndpoint, t platformPruneTarget, record func(*journal.DeleteCheckpointReceipt) (pruneOutcome, error)) (pruneOutcome, error) {
	receipt, status, err := s.interpretPlatformDeleteReply(reply, call, endpoint)
	if err != nil {
		return pruneLeft, err
	}
	if receipt != nil {
		return record(receipt)
	}
	switch status.State {
	case "pending":
		s.markDeletionUnknown(context.Background(), t)
		return pruneBusy, nil
	case "absent":
		return pruneUnclaimed, nil
	}
	s.markDeletionUnknown(context.Background(), t)
	return pruneLeft, errPlatformPruneUncertain
}

// platformPruneResult is what one pass did.
type platformPruneResult struct {
	Settled, Busy, Unclaimed, Exhausted int
}

// prunePlatformRecoverySets deletes the checkpoint archives of the recovery sets the Portal no longer keeps, and
// settles the deletions it began. It takes one step for up to platformPruneWindow sets. It never fails an operation:
// whatever it cannot do is left for the next tick. With the restore pause on, or unreadable, nothing is selected.
func (s *Server) prunePlatformRecoverySets(ctx context.Context, endpoint platformRuntimeEndpoint) (platformPruneResult, error) {
	var result platformPruneResult
	if endpoint.path == "" {
		return result, nil
	}
	// One cheap question first: a Portal with no recovery set has nothing to prune, and the idle tick asks this often.
	var sets int
	if err := s.st.queryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM app_platform_recovery_sets WHERE state IN ('verified','deleted') LIMIT 1) x`).Scan(&sets); err != nil {
		return result, err
	}
	if sets == 0 {
		return result, nil
	}
	paused, err := platformRecoveryPaused(s.st.poolExec())
	if err != nil {
		// Fail closed: with the pause unreadable nothing is deleted.
		return result, err
	}
	if paused {
		return result, nil
	}
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	history, open, err := platformPruneHistory(pool)
	if err != nil {
		return result, err
	}
	var targets []platformPruneTarget
	for _, r := range open {
		if r.stranded {
			// A stranded row is never sent again; a new attempt on the current host settles it (platformPruneCandidates).
			continue
		}
		if r.runtimeID != "" {
			var enabled int
			if err := pool.queryRow(`SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, r.runtimeID).Scan(&enabled); err != nil || enabled == 0 {
				// A runtime that was retired cannot be read from. The row stays open, so the set stays decided, and the
				// storage status reports it (platformPruneRefusals); it takes no window slot.
				continue
			}
		}
		set, err := loadPlatformPruneSet(pool, r.setID)
		if err != nil {
			log.Printf("app-platform recovery set prune %s: %v", r.effectID, err)
			continue
		}
		var installation string
		if err := pool.queryRow(`SELECT installation_id FROM app_platform_operations WHERE id=?`, r.operation).Scan(&installation); err != nil {
			log.Printf("app-platform recovery set prune %s: %v", r.effectID, err)
			continue
		}
		targets = append(targets, platformPruneTarget{set: set, intent: r.intent, hostOperation: r.operation, installation: installation, runtimeID: r.runtimeID,
			effectID: r.effectID, effectNumber: r.number, state: r.state})
	}
	if len(targets) > platformPruneWindow {
		start := s.platformPruneCursor % len(targets)
		targets = append(append([]platformPruneTarget{}, targets[start:]...), targets[:start]...)[:platformPruneWindow]
		s.platformPruneCursor += platformPruneWindow
	}
	// New deletions have a window of their own: rows that cannot progress must not stop the others.
	{
		room := platformPruneWindow
		fresh, exhausted, err := s.platformPruneCandidates(pool, s.platformRetentionConfigLoad().RecoverySets, history, time.Now().UnixMilli())
		if err != nil {
			// Fail closed: with the selection unreadable nothing new is started.
			return result, err
		}
		for _, host := range exhausted {
			result.Exhausted++
			if _, seen := s.platformPruneNoted.LoadOrStore(host, true); !seen {
				log.Printf("app-platform recovery set prune: operation %s has no free remove_data effect number; the remaining sets wait for the installation's next operation", host)
			}
		}
		if len(fresh) > room {
			fresh = fresh[:room]
		}
		targets = append(targets, fresh...)
	}
	for _, t := range targets {
		outcome, err := s.pruneRecoverySet(ctx, endpoint, t)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return result, ctx.Err()
		default:
			log.Printf("app-platform recovery set prune %s: %v", t.effectID, err)
		}
		switch outcome {
		case pruneSettled:
			result.Settled++
		case pruneBusy:
			result.Busy++
		case pruneUnclaimed:
			result.Unclaimed++
		}
	}
	return result, nil
}

// platformPruneRefused is a set whose deletion the runner refused in a way that does not pass, or on every
// attempt, as the storage status reports it: an operator has to look at it.
type platformPruneRefused struct {
	RecoverySetID string `json:"recovery_set_id"`
	CheckpointID  string `json:"checkpoint_id"`
	Attempts      int    `json:"attempts"`
	// Reason is the last refusal, for example checkpoint_identity_mismatch or checkpoint_unrecorded.
	Reason string `json:"reason"`
}

// platformPruneStale is how long a deletion may stay open with no verdict before the storage status reports it.
const platformPruneStale = 24 * time.Hour

// platformPruneRefusals lists the sets whose deletion needs an operator: the last refusal does not pass, or the attempts
// ran out, with no deletion of the set applied or open; or a deletion has been open with no verdict for a day
// (outcome_unknown) or was issued to a runtime that was retired (runtime_retired). An open deletion is never closed
// without a runner verdict, so the status is the only way such a row leaves the operator's attention.
func (s *Server) platformPruneRefusals(ctx context.Context) ([]platformPruneRefused, error) {
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	history, _, err := platformPruneHistory(pool)
	if err != nil {
		return nil, err
	}
	retired := map[string]bool{}
	rows, err := pool.query(`SELECT id FROM app_platform_runtime_bindings WHERE enabled=0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		retired[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	out := []platformPruneRefused{}
	for id, set := range history {
		var last int64
		lastOutcome, checkpoint, openReason := "", "", ""
		applied := false
		for _, r := range set {
			checkpoint = r.intent.CheckpointID
			switch {
			case r.state == "applied":
				applied = true
			case r.state == "prepared" || r.state == "unknown":
				switch {
				case retired[r.runtimeID]:
					openReason = "runtime_retired"
				case !r.stranded && now-r.updated > platformPruneStale.Milliseconds() && openReason == "":
					openReason = "outcome_unknown"
				}
			case !platformPruneSuperseded(r.outcome) && r.updated >= last:
				last, lastOutcome = r.updated, r.outcome
			}
		}
		switch {
		case applied:
			continue
		case openReason != "":
			lastOutcome = openReason
		case platformPruneOpen(set) || (platformPruneRetryable(lastOutcome) && len(set) < platformPruneMaxAttempts):
			continue
		}
		out = append(out, platformPruneRefused{RecoverySetID: id, CheckpointID: checkpoint, Attempts: len(set), Reason: lastOutcome})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecoverySetID < out[j].RecoverySetID })
	return out, nil
}

func platformPruneOpen(rows []platformPruneRow) bool {
	for _, r := range rows {
		if r.state == "prepared" || r.state == "unknown" {
			return true
		}
	}
	return false
}

// platformPruneInterval is how often the pruning step runs at most, from the idle tick and from every operation ending
// together. platformPruneBusyInterval is how long it defers while any operation of any installation is in flight: the
// step is housekeeping, and under constant churn it must not add reads and lock waits to the operations. It still runs
// once that long has passed, so a Portal that is never idle is not left with its archives for ever.
const (
	platformPruneInterval     = time.Minute
	platformPruneBusyInterval = time.Hour
)

// pruneRecoverySetsOnTick runs prunePlatformRecoverySets from the scheduler. It never fails the tick. The selection
// reads take no lock; the admission locks are taken only by the short decision, settlement and strand transactions of
// a deletion that is really begun. A pass that left a deletion running on the runner lets the next tick read it back.
func (s *Server) pruneRecoverySetsOnTick(ctx context.Context, endpoint platformRuntimeEndpoint) {
	now := time.Now().UnixMilli()
	last := s.platformPruneLast.Load()
	if last != 0 && now-last < platformPruneInterval.Milliseconds() {
		return
	}
	if last != 0 && now-last < platformPruneBusyInterval.Milliseconds() {
		var running int
		if err := s.st.queryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM app_platform_operations WHERE completed_ms IS NULL LIMIT 1) x`).Scan(&running); err != nil || running != 0 {
			return
		}
	}
	s.platformPruneLast.Store(now)
	result, err := s.prunePlatformRecoverySets(ctx, endpoint)
	if err != nil && ctx.Err() == nil {
		log.Printf("app-platform recovery set prune: %v", err)
	}
	if result.Busy > 0 {
		s.platformPruneLast.Store(0)
	}
}
