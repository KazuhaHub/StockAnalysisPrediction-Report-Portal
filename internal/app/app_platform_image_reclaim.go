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
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Reclaiming the images of releases nothing references any more (doc 171, PR 4c).
//
// The Portal owns "last referencing installation": only it knows which releases every non-removed installation
// still keeps. The runner enforces the native guard. After an operation ends (an uninstall or an update, as any
// operation does) and on every idle tick, the Portal computes the images it put on the runtime that no pinned
// release carries, and sends one signed remove-image command per config digest.
//
// Where it runs. The step is a sweep over the finished operations' own ledger, not a new operation stage: an
// operation stage would be a Portal schema change (the stage list is a CHECK), and a stage in the middle of an
// operation would sit in front of start-bearing work. Run after the operation is complete, the sweep cannot
// change any start-bearing gate; every installation-wide effect counter ignores remove_image rows exactly as it
// ignores load_image rows.
//
// The effect. Kind remove_image, journal stage "stage", effect number 2 or more (number 1 of that stage is the
// load), hosted by the latest finished operation of the installation that loaded the image, so its fence and
// generation are the installation's own. The id is derived from the host operation and the load effect it undoes, so
// every attempt, restart and tick signs the same effect. The row is written before the command leaves the Portal and
// carries the intent (what to remove) in its receipt until the runner's verdict replaces it.

const platformRemoveImageKind = "remove_image"

// platformReclaimWindow bounds how many removals one tick sends.
const platformReclaimWindow = 4

// platformRemoveImageMaxNumber is the highest effect number a removal may take (the journal bound).
const platformRemoveImageMaxNumber = 32

var (
	// errPlatformReclaimUncertain is a removal whose outcome is not known; the same effect is read again.
	errPlatformReclaimUncertain = errors.New("the outcome of the image removal is not known")
	// errPlatformStillReferenced is not a failure: the image is referenced again, or was loaded again, so the
	// removal is not started.
	errPlatformStillReferenced = errors.New("the image is referenced again")
)

// platformKnownLoadsIn is the watermark of an image on a runtime: every applied load effect of its config digest.
func platformKnownLoadsIn(e migExec, runtimeID, digest string) (string, error) {
	rows, err := e.query(`SELECT effect_id,receipt_json FROM app_platform_operation_effects WHERE kind='load_image' AND state='applied' AND COALESCE(runtime_id,'')=?`, runtimeID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, receipt string
		if err := rows.Scan(&id, &receipt); err != nil {
			return "", err
		}
		if parsed, err := journal.ParseLoadImageReceipt([]byte(receipt)); err == nil && parsed.Claim.ConfigDigest == digest {
			ids = append(ids, id)
		}
	}
	return control.FormatKnownLoads(ids), rows.Err()
}

// resolvePlatformRemovalsForLoad settles, before a load of an image is sent, every removal of that image that has no
// verdict, so a load never waits on a step that only an idle tick or an operation ending would run. Each removal is
// read back under its own identity:
//
//   - a receipt settles the row (applied: the image is gone and the load proceeds as a normal load that uploads
//     the blobs again; reverted: the image is still there and the load finds it present);
//   - pending (the runner holds the gate) is a bounded wait: errPlatformImageBusy;
//   - orphaned, a typed absence, a row that was never sent or one the host operation can no longer settle: the row is
//     closed abandoned (referenced_again) and the load proceeds. The runner's claim, if it holds one, stays unknown
//     and keeps its reservation, and the runner refuses to remove an image whose load it has seen applied.
func (s *Server) resolvePlatformRemovalsForLoad(ctx context.Context, endpoint platformRuntimeEndpoint, runtimeID, configDigest string) error {
	open, err := s.platformOpenRemovalsFor(ctx, runtimeID, configDigest)
	if err != nil {
		return err
	}
	for _, t := range open {
		if err := s.resolveOpenRemoval(ctx, endpoint, t); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) resolveOpenRemoval(ctx context.Context, endpoint platformRuntimeEndpoint, t platformReclaimTarget) error {
	if t.unparsable {
		return s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
	}
	lease, err := platformLoadSettlementLease(s.st.poolExec(), t.hostOperation)
	if err != nil {
		return s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
	}
	guard := func(e migExec) (int64, error) { return platformLoadSettlementGuard(e, lease) }
	signer, err := s.newPlatformImageSignerGuarded(ctx, lease, func(e migExec) error { _, err := guard(e); return err })
	if err != nil {
		if errors.Is(err, errAppPlatformLeaseConflict) || errors.Is(err, errAppPlatformStageEvidence) {
			return s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
		}
		return err
	}
	defer signer.close()
	var descriptor []byte
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), t.intent.ReleaseSHA256).Scan(&descriptor); err != nil {
		return s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
	}
	call, err := signer.removeCall(t, descriptor)
	if err != nil {
		return err
	}
	reply, readErr := s.platformImageRunnerSeam().ReadRemoveImage(ctx, endpoint, call)
	switch {
	case errors.Is(readErr, errPlatformMigrationAbsent):
		return s.abandonPlatformRemoval(ctx, t, "referenced_again")
	case readErr != nil:
		return readErr
	}
	receipt, status, err := s.interpretPlatformRemoveReply(reply, call, endpoint)
	if err != nil {
		return err
	}
	if receipt != nil {
		rows := platformImageRows{s: s, lease: lease, runtimeID: signer.runtime.id, guard: guard}
		return s.recordPlatformRemoval(ctx, signer, rows, call, receipt)
	}
	if status.State == "pending" {
		return fmt.Errorf("%w: a removal of the image is still running", errPlatformImageBusy)
	}
	return s.abandonPlatformRemoval(ctx, t, "referenced_again")
}

// platformRemoveImageReply is what the transport proves about a reply to a remove-image command or its read-back.
type platformRemoveImageReply interface {
	platformLifecycleProof
	RemoveImageReceipt() (journal.RemoveImageReceipt, error)
	RemoveImageStatus() (journal.RemoveImageStatus, error)
}

type platformRemoveImageCall struct {
	command                       control.RemoveImageCommand
	raw, signature, readSignature []byte
	authority                     control.Authority
	descriptor                    []byte
}

func (platformImageTransport) RemoveImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformRemoveImageCall) (platformRemoveImageReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.RemoveImageExecute, call.raw, call.signature, call.authority, call.descriptor)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformImageTransport) ReadRemoveImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformRemoveImageCall) (platformRemoveImageReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.RemoveImageRead, call.raw, call.readSignature, call.authority, call.descriptor)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

// platformReclaimIntent is what a removal row records until the runner's verdict: the image to remove, as the load
// effect it undoes named it.
type platformReclaimIntent struct {
	ReleaseSHA256  string `json:"release_sha256"`
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	Layers         string `json:"layers"`
	LoadEffectID   string `json:"load_effect_id"`
	// KnownLoads is the reference watermark the command carries: every applied load of this image on the runtime
	// the Portal saw when it computed the references, as control.FormatKnownLoads encodes it.
	KnownLoads string `json:"known_loads"`
	// Attempt numbers the tries after a refusal; it is part of the effect id, so a retry is a new effect.
	Attempt int `json:"attempt"`
}

type platformReclaimRow struct {
	Intent platformReclaimIntent `json:"intent"`
}

// platformReclaimTarget is one image to remove, new or already begun.
type platformReclaimTarget struct {
	runtimeID     string
	hostOperation string
	installation  string
	intent        platformReclaimIntent
	effectID      string
	effectNumber  int64
	// unparsable marks an open row whose intent cannot be read.
	unparsable bool
	// stale marks a begun removal whose watermark no longer names every load of the image.
	stale bool
	// state is "" for a removal not begun, otherwise the Portal row's open state ("prepared" or "unknown").
	state string
}

func platformRemoveImageEffectID(hostOperation, loadEffect string, attempt int) string {
	if attempt == 0 {
		return platformDerivedUUID("remove-image", hostOperation+"/"+loadEffect)
	}
	return platformDerivedUUID("remove-image", hostOperation+"/"+loadEffect+"/"+strconv.Itoa(attempt))
}

// platformReclaimBackoff is how long a refused removal waits before its next attempt, per attempt already made and
// capped at platformReclaimBackoffCap. Tests set it to zero.
const platformReclaimBackoffDefault = 10 * time.Minute

var platformReclaimBackoff = platformReclaimBackoffDefault

const platformReclaimBackoffCap = 6 * time.Hour

func platformReclaimWait(attempts int) time.Duration {
	return min(time.Duration(attempts)*platformReclaimBackoff, platformReclaimBackoffCap)
}

type platformLoadedImage struct {
	runtimeID, operation, installation, effectID string
	updatedMS                                    int64
	intent                                       platformReclaimIntent
	// knownLoads is every applied load effect of the image on the runtime.
	knownLoads []string
}

// platformReferencedConfigDigests is every config digest a release the Portal still needs carries. A release is
// needed when loadPlatformReleasePins pins it (the active release of every installation that still exists, every
// release a retained recovery checkpoint or bundle could restore, candidates, recovery sets, releases of
// unfinished operations, unsettled loads, unrecovered failures and open review plans) or when it is the previous
// release of an installation that still exists: the release its latest completed update moved away from, kept for
// rollback. Any failure to read a release returns an error and no set: removal must never run on a partial set.
func (s *Server) platformReferencedConfigDigests(ctx context.Context) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	referenced, err := platformReferencedConfigDigestsIn(migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}})
	if err != nil {
		return nil, err
	}
	return referenced, nil
}

// platformReferencedConfigDigestsIn is the same computation on any executor, so the removal row can be inserted in
// the transaction that computed it, under the lock operation admission also takes.
func platformReferencedConfigDigestsIn(e migExec) (map[string]bool, error) {
	pins, err := loadPlatformReleasePins(e, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	rows, err := e.query(`SELECT DISTINCT p.source_release_sha256 FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.action='update' AND o.stage='complete' AND o.completed_ms IS NOT NULL AND i.desired_state<>'removed' AND p.source_release_sha256 IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM app_platform_operations o2 WHERE o2.installation_id=o.installation_id AND o2.action='update' AND o2.stage='complete'
			AND o2.completed_ms IS NOT NULL AND (o2.completed_ms>o.completed_ms OR (o2.completed_ms=o.completed_ms AND o2.id>o.id)))`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, err
		}
		if pins[sha] == nil {
			pins[sha] = map[string]bool{}
		}
		pins[sha]["previous"] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	shas := make([]string, 0, len(pins))
	for sha := range pins {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	for _, sha := range shas {
		var descriptor []byte
		if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, sha).Scan(&descriptor); err != nil {
			return nil, fmt.Errorf("referenced release %s: %w", sha, err)
		}
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil {
			return nil, fmt.Errorf("referenced release %s: %w", sha, err)
		}
		if parsed.Manifest.Backend == nil {
			continue
		}
		for _, entry := range parsed.Manifest.Backend.Images {
			referenced[entry.ConfigDigest] = true
		}
	}
	return referenced, nil
}

// platformLoadedImages lists what the Portal has loaded on a runtime and not removed since: the latest applied load
// of each config digest, unless an applied removal is at least as recent. A runtime with a load that has no verdict
// is left out whole: that load may be introducing any image.
func (s *Server) platformLoadedImages(ctx context.Context) ([]platformLoadedImage, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT f.kind,f.state,COALESCE(f.runtime_id,''),f.operation_id,o.installation_id,f.effect_id,f.updated_ms,f.receipt_json
		FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.kind IN ('load_image','remove_image') ORDER BY f.updated_ms,f.effect_id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ runtime, digest string }
	latestLoad := map[key]platformLoadedImage{}
	known := map[key][]string{}
	removedAt := map[key]int64{}
	busy := map[string]bool{}
	for rows.Next() {
		var kind, state, runtimeID, operation, installation, effect, receipt string
		var updated int64
		if err := rows.Scan(&kind, &state, &runtimeID, &operation, &installation, &effect, &updated, &receipt); err != nil {
			return nil, err
		}
		switch {
		case kind == platformLoadImageKind && state == "applied":
			parsed, err := journal.ParseLoadImageReceipt([]byte(receipt))
			if err != nil {
				// A load whose receipt cannot be read cannot be named, so it is never removed.
				continue
			}
			k := key{runtimeID, parsed.Claim.ConfigDigest}
			known[k] = append(known[k], effect)
			latestLoad[k] = platformLoadedImage{runtimeID: runtimeID, operation: operation, installation: installation, effectID: effect, updatedMS: updated,
				intent: platformReclaimIntent{ReleaseSHA256: parsed.Claim.ReleaseSHA256, Platform: parsed.Claim.Platform, ManifestDigest: parsed.Claim.ManifestDigest,
					ConfigDigest: parsed.Claim.ConfigDigest, Layers: parsed.Claim.Layers, LoadEffectID: effect}}
		case kind == platformLoadImageKind && (state == "prepared" || state == "unknown"):
			busy[runtimeID] = true
		case kind == platformRemoveImageKind && state == "applied":
			var settled journal.RemoveImageReceipt
			if json.Unmarshal([]byte(receipt), &settled) == nil {
				removedAt[key{runtimeID, settled.Claim.ConfigDigest}] = max(removedAt[key{runtimeID, settled.Claim.ConfigDigest}], updated)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []platformLoadedImage
	for k, image := range latestLoad {
		if busy[k.runtime] {
			continue
		}
		if at, removed := removedAt[k]; removed && at >= image.updatedMS {
			continue
		}
		image.knownLoads = known[k]
		image.intent.KnownLoads = control.FormatKnownLoads(known[k])
		out = append(out, image)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].updatedMS != out[j].updatedMS {
			return out[i].updatedMS < out[j].updatedMS
		}
		return out[i].effectID < out[j].effectID
	})
	return out, nil
}

// platformOpenRemovalsFor lists the open removals of one image on a runtime, asked directly with no window. A row
// whose intent cannot be read counts as open for every image of its runtime, so the answer fails closed.
func (s *Server) platformOpenRemovalsFor(ctx context.Context, runtimeID, configDigest string) ([]platformReclaimTarget, error) {
	open, err := s.platformOpenRemovals(ctx)
	if err != nil {
		return nil, err
	}
	var out []platformReclaimTarget
	for _, t := range open {
		if t.runtimeID == runtimeID && (t.unparsable || t.intent.ConfigDigest == configDigest) {
			out = append(out, t)
		}
	}
	return out, nil
}

// platformOpenRemovals lists the removal rows that have no verdict, rebuilt from the intent each recorded.
func (s *Server) platformOpenRemovals(ctx context.Context) ([]platformReclaimTarget, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT f.operation_id,o.installation_id,COALESCE(f.runtime_id,''),f.effect_id,f.effect_number,f.state,f.receipt_json
		FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.kind='remove_image' AND f.state IN ('prepared','unknown') ORDER BY f.updated_ms,f.effect_id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []platformReclaimTarget
	for rows.Next() {
		var t platformReclaimTarget
		var receipt string
		if err := rows.Scan(&t.hostOperation, &t.installation, &t.runtimeID, &t.effectID, &t.effectNumber, &t.state, &receipt); err != nil {
			return nil, err
		}
		var row platformReclaimRow
		if json.Unmarshal([]byte(receipt), &row) != nil || row.Intent.ConfigDigest == "" {
			// An open row whose intent cannot be read is still open: it is kept in the list, so the checks that
			// ask whether a removal is open fail closed, and the step closes it.
			t.unparsable = true
		} else {
			t.intent = row.Intent
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type platformRemovalOutcome struct {
	Outcome      string `json:"outcome"`
	ConfigDigest string `json:"config_digest"`
	Claim        struct {
		ConfigDigest string `json:"config_digest"`
	} `json:"claim"`
	Intent platformReclaimIntent `json:"intent"`
}

func (s *Server) platformRemovalRows(ctx context.Context) (map[string][]platformRemovalRow, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT COALESCE(runtime_id,''),state,updated_ms,receipt_json FROM app_platform_operation_effects WHERE kind='remove_image'`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]platformRemovalRow{}
	for rows.Next() {
		var runtimeID, state, receipt string
		var updated int64
		if err := rows.Scan(&runtimeID, &state, &updated, &receipt); err != nil {
			return nil, err
		}
		var parsed platformRemovalOutcome
		if json.Unmarshal([]byte(receipt), &parsed) != nil {
			continue
		}
		digest := parsed.Claim.ConfigDigest
		if digest == "" {
			digest = parsed.ConfigDigest
		}
		if digest == "" {
			digest = parsed.Intent.ConfigDigest
		}
		if digest == "" {
			continue
		}
		out[runtimeID+"/"+digest] = append(out[runtimeID+"/"+digest], platformRemovalRow{state: state, updated: updated, outcome: parsed.Outcome})
	}
	return out, rows.Err()
}

type platformRemovalRow struct {
	state   string
	updated int64
	outcome string
}

// platformReclaimMaxAttempts bounds the tries of one image after refusals: the effect numbers of a host operation are
// limited, and a removal that keeps being refused is reported to the operator instead of retried for ever.
const platformReclaimMaxAttempts = 8

// platformReclaimRefused is an image whose removal was refused on every attempt, as the storage status reports it.
type platformReclaimRefused struct {
	ConfigDigest  string `json:"config_digest"`
	ReleaseSHA256 string `json:"release_sha256"`
	Attempts      int    `json:"attempts"`
	// Reason is the last refusal: image_in_use, image_conflict, referenced_again, abandoned_outcome_unknown or unknown.
	Reason string `json:"reason"`
}

// platformReclaimRefusals lists the unreferenced images whose removal stopped after platformReclaimMaxAttempts refusals.
func (s *Server) platformReclaimRefusals(ctx context.Context) ([]platformReclaimRefused, error) {
	referenced, err := s.platformReferencedConfigDigests(ctx)
	if err != nil {
		return nil, err
	}
	loaded, err := s.platformLoadedImages(ctx)
	if err != nil {
		return nil, err
	}
	history, err := s.platformRemovalRows(ctx)
	if err != nil {
		return nil, err
	}
	out := []platformReclaimRefused{}
	for _, image := range loaded {
		if referenced[image.intent.ConfigDigest] {
			continue
		}
		attempts, _, reason := platformRefusalHistory(history[image.runtimeID+"/"+image.intent.ConfigDigest], image.updatedMS)
		if attempts >= platformReclaimMaxAttempts {
			out = append(out, platformReclaimRefused{ConfigDigest: image.intent.ConfigDigest, ReleaseSHA256: image.intent.ReleaseSHA256, Attempts: attempts, Reason: reason})
		}
	}
	return out, nil
}

// platformRefusalHistory counts the refusals recorded after the image's latest load.
func platformRefusalHistory(rows []platformRemovalRow, loadedMS int64) (attempts int, last int64, reason string) {
	reason = "unknown"
	for _, row := range rows {
		if row.updated >= loadedMS && row.state == "reverted" {
			attempts++
			if row.updated >= last {
				last = row.updated
				switch row.outcome {
				case "image_in_use", "image_conflict", "referenced_again", "abandoned_outcome_unknown":
					reason = row.outcome
				default:
					reason = "unknown"
				}
			}
		}
	}
	return attempts, last, reason
}

// platformReclaimTargets returns the removals to carry out now: the begun ones that still have no verdict, and the
// new ones for images nothing references. A begun removal whose image is referenced again, or whose watermark no
// longer names every load of the image, is returned so the caller can abandon it. A refused removal is tried again
// on a later tick, as a new attempt with an effect id of its own, after a backoff that grows with the attempts; an
// applied one never is, and an open one is never started twice. Any failure to compute the references returns an
// error and no target: if in doubt, the image stays.
func (s *Server) platformReclaimTargets(ctx context.Context) (targets []platformReclaimTarget, referenced map[string]bool, err error) {
	referenced, err = s.platformReferencedConfigDigests(ctx)
	if err != nil {
		return nil, nil, err
	}
	open, err := s.platformOpenRemovals(ctx)
	if err != nil {
		return nil, nil, err
	}
	loaded, err := s.platformLoadedImages(ctx)
	if err != nil {
		return nil, nil, err
	}
	fresh := map[string]string{}
	for _, image := range loaded {
		fresh[image.runtimeID+"/"+image.intent.ConfigDigest] = image.intent.KnownLoads
	}
	begun := map[string]bool{}
	for _, t := range open {
		k := t.runtimeID + "/" + t.intent.ConfigDigest
		begun[k] = true
		if watermark, ok := fresh[k]; ok && watermark != t.intent.KnownLoads {
			// A load the command does not name has been applied since: the image may be referenced again.
			t.stale = true
		}
		targets = append(targets, t)
	}
	history, err := s.platformRemovalRows(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UnixMilli()
	pool := s.st.poolExec()
	for _, image := range loaded {
		k := image.runtimeID + "/" + image.intent.ConfigDigest
		if referenced[image.intent.ConfigDigest] || begun[k] {
			continue
		}
		if len(image.knownLoads) > control.MaxKnownLoads {
			log.Printf("app-platform image removal: %d loads of one image exceed the watermark bound; the image is kept", len(image.knownLoads))
			continue
		}
		attempts, last, _ := platformRefusalHistory(history[k], image.updatedMS)
		if attempts >= platformReclaimMaxAttempts {
			// Reported by the storage status, not retried and not logged on every tick.
			continue
		}
		if attempts > 0 && now < last+platformReclaimWait(attempts).Milliseconds() {
			continue
		}
		host, ok, err := platformReclaimHost(pool, image.installation)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		effect := platformRemoveImageEffectID(host, image.effectID, attempts)
		var exists int
		if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE effect_id=?`), effect).Scan(&exists); err != nil {
			return nil, nil, err
		}
		if exists != 0 {
			continue
		}
		intent := image.intent
		intent.Attempt = attempts
		targets = append(targets, platformReclaimTarget{runtimeID: image.runtimeID, hostOperation: host, installation: image.installation, intent: intent, effectID: effect})
	}
	return targets, referenced, nil
}

// platformReclaimHost picks the operation that hosts a removal for an installation: its latest finished operation,
// and only while no operation of the installation is running, so the fence and generation the command carries are
// the installation's own and nothing it sends can be below a floor a newer operation set.
func platformReclaimHost(e migExec, installation string) (string, bool, error) {
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&active); err != nil || active != 0 {
		return "", false, err
	}
	var id string
	err := e.queryRow(`SELECT id FROM app_platform_operations WHERE installation_id=? AND managed=1 AND completed_ms IS NOT NULL AND lease_owner IS NULL
		ORDER BY completed_ms DESC,id DESC LIMIT 1`, installation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// platformRemovalNumber is the effect number of a removal: the one its row already has, or the next free number of
// the stage for the host operation, never below 2.
func platformRemovalNumber(e migExec, operation, effect string) (int64, error) {
	var number int64
	err := e.queryRow(`SELECT effect_number FROM app_platform_operation_effects WHERE operation_id=? AND effect_id=?`, operation, effect).Scan(&number)
	if err == nil {
		return number, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var highest int64
	if err := e.queryRow(`SELECT COALESCE(MAX(effect_number),1) FROM app_platform_operation_effects WHERE operation_id=? AND stage=?`, operation, control.RemoveImageStage).Scan(&highest); err != nil {
		return 0, err
	}
	number = max(highest+1, control.MinRemoveImageEffectNumber)
	if number > platformRemoveImageMaxNumber {
		return 0, errAppPlatformStageEvidence
	}
	return number, nil
}

func (g *platformImageSigner) removeCall(t platformReclaimTarget, descriptor []byte) (platformRemoveImageCall, error) {
	var zero platformRemoveImageCall
	now := time.Now().UnixMilli()
	expires := min(now+25000, g.lease.leaseUntilMS-1000)
	if expires <= now {
		return zero, errAppPlatformLeaseConflict
	}
	command := control.RemoveImageCommand{
		CommandHeader: control.CommandHeader{
			Format: control.RemoveImageFormat, DeploymentID: g.runtime.deploymentID, ControllerEpoch: g.lease.controllerEpoch,
			ControlRevision: strconv.FormatInt(g.runtime.controlRevision, 10), InstallationID: g.lease.installationID,
			OperationID: g.lease.id, TargetGeneration: strconv.FormatUint(g.lease.snapshot.Generation, 10),
			Stage: control.RemoveImageStage, EffectNumber: strconv.FormatInt(t.effectNumber, 10),
			EffectID: t.effectID, Kind: control.RemoveImageKind, InstallationFence: strconv.FormatInt(g.lease.fence, 10),
		},
		Payload: control.RemoveImagePayload{
			ReleaseSHA256: t.intent.ReleaseSHA256, Platform: t.intent.Platform, ManifestDigest: t.intent.ManifestDigest,
			ConfigDigest: t.intent.ConfigDigest, Layers: t.intent.Layers, KnownLoads: t.intent.KnownLoads,
		},
	}
	digest, err := control.RemoveImageLogicalDigest(command)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(expires, 10)
	raw, signature, err := control.SignRemoveImage(command, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	read, err := control.SignRemoveImageRead(raw, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	return platformRemoveImageCall{command: command, raw: raw, signature: signature, readSignature: read, authority: g.authority, descriptor: bytes.Clone(descriptor)}, nil
}

// prepareRemoval writes the row before the first command is sent, with the intent in its receipt. It reports the
// state an earlier attempt left: applied or reverted rows are never sent again.
func (r platformImageRows) prepareRemoval(ctx context.Context, c control.RemoveImageCommand, intent platformReclaimIntent) (state string, err error) {
	raw, err := json.Marshal(platformReclaimRow{Intent: intent})
	if err != nil {
		return "", err
	}
	err = r.with(ctx, func(e migExec, now int64) error {
		var id, kind, digest string
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		qerr := e.queryRow(`SELECT effect_id,kind,payload_sha256,state FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?`+lock,
			r.lease.id, control.RemoveImageStage, mustInt64(c.EffectNumber)).Scan(&id, &kind, &digest, &state)
		if errors.Is(qerr, sql.ErrNoRows) {
			// The references are computed again here, in the transaction that inserts the row, under the lock
			// operation admission takes too: an operation admitted first is pinned by its unfinished-operation
			// pin and stops the removal here, and a removal inserted first is seen by any later load of the image,
			// which waits for it (platformRefuseLoadWhileRemoval).
			if err := lockPlatformPublisherTrust(e); err != nil {
				return err
			}
			// The pause is read again here: a recovery that began after the sweep started stops the removal.
			if paused, err := platformRecoveryPaused(e); err != nil || paused {
				if err != nil {
					return err
				}
				return errPlatformStillReferenced
			}
			referenced, err := platformReferencedConfigDigestsIn(e)
			if err != nil {
				return err
			}
			if referenced[intent.ConfigDigest] {
				return errPlatformStillReferenced
			}
			watermark, err := platformKnownLoadsIn(e, r.runtimeID, intent.ConfigDigest)
			if err != nil {
				return err
			}
			if watermark != intent.KnownLoads {
				return errPlatformStillReferenced
			}
			_, err = e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				VALUES(?,?,?,?,?,?,?,?,?,0,'prepared',?,?,?)`, r.lease.id, control.RemoveImageStage, mustInt64(c.EffectNumber), c.EffectID, platformRemoveImageKind, c.PayloadSHA256,
				r.runtimeID, r.lease.controllerEpoch, r.lease.fence, string(raw), now, now)
			state = "prepared"
			return err
		}
		if qerr != nil {
			return qerr
		}
		if kind != platformRemoveImageKind || id != c.EffectID || digest != c.PayloadSHA256 {
			return errAppPlatformStageEvidence
		}
		return nil
	})
	return state, err
}

func mustInt64(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

// settleRemoval records the runner's verdict: applied for a removed or absent image, reverted for a refusal.
func (r platformImageRows) settleRemoval(ctx context.Context, c control.RemoveImageCommand, state, receipt string) error {
	if len(receipt) > 16384 {
		return errAppPlatformStageEvidence
	}
	return r.with(ctx, func(e migExec, now int64) error {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state=?,observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND state IN ('prepared','unknown')`,
			state, r.lease.fence, receipt, now, r.lease.id, control.RemoveImageStage, mustInt64(c.EffectNumber), c.EffectID, platformRemoveImageKind)
		return platformLeaseCAS(changed, err)
	})
}

// markRemovalUnknown marks a removal that was sent and has no verdict. Best effort: a failure leaves the row prepared,
// which the settlement treats the same way.
func (r platformImageRows) markRemovalUnknown(ctx context.Context, c control.RemoveImageCommand) error {
	return r.with(ctx, func(e migExec, now int64) error {
		_, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=?
			WHERE operation_id=? AND stage=? AND effect_number=? AND effect_id=? AND kind=? AND state='prepared'`,
			now, r.lease.id, control.RemoveImageStage, mustInt64(c.EffectNumber), c.EffectID, platformRemoveImageKind)
		return err
	})
}

// abandonRemoval settles a removal row that can no longer be carried out as reverted with a typed outcome. It
// changes nothing on the runner: an unknown claim there keeps its reservation, which is the fail-closed direction.
func (s *Server) abandonPlatformRemoval(ctx context.Context, t platformReclaimTarget, outcome string) error {
	return s.st.withOUMutation(func(e migExec) error {
		now, err := platformDatabaseNow(e)
		if err != nil {
			return err
		}
		_, err = e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
			WHERE operation_id=? AND effect_id=? AND kind='remove_image' AND state IN ('prepared','unknown')`, `{"outcome":"`+outcome+`","config_digest":"`+t.intent.ConfigDigest+`"}`, now, t.hostOperation, t.effectID)
		return err
	})
}

// interpretPlatformRemoveReply turns a reply into a verified receipt or status. A reply is believed only when it
// answers exactly the command that was signed.
func (s *Server) interpretPlatformRemoveReply(reply platformRemoveImageReply, call platformRemoveImageCall, endpoint platformRuntimeEndpoint) (*journal.RemoveImageReceipt, *journal.RemoveImageStatus, error) {
	if r, rerr := reply.RemoveImageReceipt(); rerr == nil {
		if !journal.RemoveImageReceiptMatchesCommand(r, call.command) ||
			!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, r.ObservedMS, r.SettledMS, call.command.IssuedMS, false) {
			return nil, nil, errAppPlatformStageEvidence
		}
		return &r, nil, nil
	}
	status, serr := reply.RemoveImageStatus()
	if serr != nil || !journal.RemoveImageStatusMatchesCommand(status, call.command) ||
		!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, status.ObservedMS, "", call.command.IssuedMS, true) {
		return nil, nil, errAppPlatformStageEvidence
	}
	return nil, &status, nil
}

// reclaimPlatformImage drives one removal to a verdict, or leaves it for the next tick. It returns whether the
// removal settled.
func (s *Server) reclaimPlatformImage(ctx context.Context, endpoint platformRuntimeEndpoint, t platformReclaimTarget, stillUnreferenced bool) (bool, error) {
	if t.unparsable {
		return false, s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
	}
	if !stillUnreferenced {
		// The image is referenced again: it is kept, and the row is closed with the reason. Nothing is sent.
		return false, s.abandonPlatformRemoval(ctx, t, "referenced_again")
	}
	lease, err := platformLoadSettlementLease(s.st.poolExec(), t.hostOperation)
	if err != nil {
		if t.state != "" {
			log.Printf("app-platform image removal %s: the host operation cannot settle it; closing the row as abandoned_outcome_unknown", t.effectID)
			return false, s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
		}
		return false, err
	}
	guard := func(e migExec) (int64, error) { return platformLoadSettlementGuard(e, lease) }
	signer, err := s.newPlatformImageSignerGuarded(ctx, lease, func(e migExec) error { _, err := guard(e); return err })
	if err != nil {
		if t.state != "" && (errors.Is(err, errAppPlatformLeaseConflict) || errors.Is(err, errAppPlatformStageEvidence)) {
			log.Printf("app-platform image removal %s: the installation's fence moved; closing the row as abandoned_outcome_unknown", t.effectID)
			return false, s.abandonPlatformRemoval(ctx, t, "abandoned_outcome_unknown")
		}
		return false, err
	}
	defer signer.close()
	if t.runtimeID != "" && t.runtimeID != signer.runtime.id {
		// The image lives on another runtime than the one this endpoint serves.
		return false, nil
	}
	var descriptor []byte
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), t.intent.ReleaseSHA256).Scan(&descriptor); err != nil {
		return false, err
	}
	if t.effectNumber == 0 {
		if t.effectNumber, err = platformRemovalNumber(s.st.poolExec(), t.hostOperation, t.effectID); err != nil {
			return false, err
		}
	}
	call, err := signer.removeCall(t, descriptor)
	if err != nil {
		return false, err
	}
	rows := platformImageRows{s: s, lease: lease, runtimeID: signer.runtime.id, guard: guard}
	state, err := rows.prepareRemoval(ctx, call.command, t.intent)
	if errors.Is(err, errPlatformStillReferenced) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state == "applied" || state == "reverted" {
		return true, nil
	}
	runner := s.platformImageRunnerSeam()
	if state == "unknown" {
		// Read an unknown removal back first: a receipt settles it, and only a runner that holds nothing running it
		// is asked to complete it, under its own claim.
		reply, readErr := runner.ReadRemoveImage(ctx, endpoint, call)
		if readErr == nil && reply != nil {
			receipt, status, ierr := s.interpretPlatformRemoveReply(reply, call, endpoint)
			if ierr != nil {
				return false, ierr
			}
			if receipt != nil {
				return true, s.recordPlatformRemoval(ctx, signer, rows, call, receipt)
			}
			if status.State == "pending" {
				return false, errPlatformImageBusy
			}
		} else if readErr != nil && !errors.Is(readErr, errPlatformMigrationAbsent) {
			return false, readErr
		}
	}
	reply, err := runner.RemoveImage(ctx, endpoint, call)
	if err != nil {
		// An uncertain execute is read back under its own identity. It is never sent again blind.
		reply, readErr := runner.ReadRemoveImage(ctx, endpoint, call)
		if errors.Is(readErr, errPlatformMigrationAbsent) {
			// The runner proved it never claimed the effect (for example a load of the image has no verdict): the row stays
			// prepared and the removal is tried again on a later tick.
			return false, fmt.Errorf("%w: %v", errPlatformReclaimUncertain, err)
		}
		if readErr != nil || reply == nil {
			_ = rows.markRemovalUnknown(ctx, call.command)
			if ctx.Err() != nil {
				return false, fmt.Errorf("%w: %v", errPlatformImageBusy, err)
			}
			return false, fmt.Errorf("%w: %v", errPlatformReclaimUncertain, err)
		}
		receipt, status, ierr := s.interpretPlatformRemoveReply(reply, call, endpoint)
		if ierr != nil {
			return false, ierr
		}
		if receipt != nil {
			return true, s.recordPlatformRemoval(ctx, signer, rows, call, receipt)
		}
		_ = rows.markRemovalUnknown(ctx, call.command)
		if status.State == "pending" {
			return false, errPlatformImageBusy
		}
		return false, errPlatformReclaimUncertain
	}
	receipt, status, err := s.interpretPlatformRemoveReply(reply, call, endpoint)
	if err != nil {
		return false, err
	}
	if receipt != nil {
		return true, s.recordPlatformRemoval(ctx, signer, rows, call, receipt)
	}
	_ = rows.markRemovalUnknown(ctx, call.command)
	if status.State == "pending" {
		return false, errPlatformImageBusy
	}
	return false, errPlatformReclaimUncertain
}

// recordPlatformRemoval writes a verified receipt on the removal row: applied for a removed or absent image, reverted
// for the runner's refusal because a container still uses it.
func (s *Server) recordPlatformRemoval(ctx context.Context, signer *platformImageSigner, rows platformImageRows, call platformRemoveImageCall, receipt *journal.RemoveImageReceipt) error {
	state := "reverted"
	if receipt.State == "removed" || receipt.State == "absent" {
		state = "applied"
	}
	raw, _ := json.Marshal(receipt)
	if err := rows.settleRemoval(ctx, call.command, state, string(raw)); err != nil {
		return err
	}
	if state == "applied" {
		s.auditPlatformImageRemoval(ctx, signer.lease, call.command, receipt.State)
	}
	return nil
}

func (s *Server) auditPlatformImageRemoval(ctx context.Context, lease platformOperationLease, command control.RemoveImageCommand, state string) {
	detail := fmt.Sprintf(`{"effect_id":%q,"release_sha256":%q,"platform":%q,"state":%q}`, command.EffectID, command.Payload.ReleaseSHA256, command.Payload.Platform, state)
	if _, err := s.st.db.ExecContext(ctx, s.st.bind(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.image_removed','app_platform_operation',?,?,'')`),
		time.Now().UTC().Format(time.RFC3339), lease.id, detail); err != nil {
		log.Printf("app-platform image removal audit: %v", err)
	}
}

// reclaimPlatformImages sends the removals the references allow and settles the begun ones. It takes one step for up
// to platformReclaimWindow images and returns how many settled. It never fails an operation: a failure only leaves
// the images for the next tick.
func (s *Server) reclaimPlatformImages(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	// One cheap question first: a Portal that never loaded a managed image has nothing to reclaim, and the idle tick
	// asks this every time.
	var images int
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind IN ('load_image','remove_image')`)).Scan(&images); err != nil {
		return 0, err
	}
	if images == 0 {
		return 0, nil
	}
	// While the Portal is paused for recovery nothing else of the application platform runs, and an image removed
	// now could be one the recovery is about to restore: the step waits for the pause to end.
	paused, err := platformRecoveryPaused(s.st.poolExec())
	if err != nil {
		// Fail closed: with the pause unreadable nothing is removed.
		return 0, err
	}
	if paused {
		return 0, nil
	}
	targets, referenced, err := s.platformReclaimTargets(ctx)
	if err != nil {
		// Fail closed: with the references unknown nothing is removed.
		return 0, err
	}
	if len(targets) > platformReclaimWindow {
		start := s.platformReclaimCursor % len(targets)
		targets = append(append([]platformReclaimTarget{}, targets[start:]...), targets[:start]...)[:platformReclaimWindow]
		s.platformReclaimCursor += platformReclaimWindow
	}
	settled := 0
	for _, t := range targets {
		done, err := s.reclaimPlatformImage(ctx, endpoint, t, !referenced[t.intent.ConfigDigest] && !t.stale)
		switch {
		case err == nil && done:
			settled++
		case err == nil:
		case ctx.Err() != nil:
			return settled, ctx.Err()
		case errors.Is(err, errPlatformImageBusy):
			// The runner still holds it: the same effect is read again on the next tick.
		default:
			log.Printf("app-platform image removal %s: %v", t.effectID, err)
		}
	}
	return settled, nil
}
