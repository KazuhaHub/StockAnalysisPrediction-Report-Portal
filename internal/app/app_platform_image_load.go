package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Ensuring the image of a release before a start (doc 170 resolutions 9-11).
//
// Every operation that is about to send a start first runs one load-image effect at stage "stage", effect number
// 1. The effect has no Portal row. Its identity is derived from the operation and the release, so every attempt,
// restart and lease takeover signs the same effect id, and an unknown effect is only ever settled under that id,
// never sent again under a new identity. The runner's journal is the record of the effect; Portal keeps an audit
// entry. The reasoning for no row is in the implementation notes of doc 170.

var (
	// errPlatformImageBusy is not a failure: the runner still holds the load, or the wait ran out while it did.
	errPlatformImageBusy = errors.New("the runner is still loading the image")
	// errPlatformImageUncertain is a failure whose outcome is not known; the same effect is read again.
	errPlatformImageUncertain = errors.New("the outcome of the image load is not known")
	// errPlatformImageRejected is the runner's definite refusal of the image.
	errPlatformImageRejected = errors.New("the runner did not accept the image")
	// errPlatformImageBlobs is a release whose blobs this Portal no longer holds in good order. No runner effect
	// was sent for it.
	errPlatformImageBlobs = errors.New("the release image blobs are not available in good order")
	// errPlatformImagePlatform is a release that carries no image for the host platform.
	errPlatformImagePlatform = errors.New("the release has no image for the host platform")
)

// platformLoadImageEffectNumber is the next effect number free in stage "stage" for every operation action:
// no effect has been issued in that stage before (doc 170 resolution 9).
const platformLoadImageEffectNumber = "1"

// platformImageReply is what the transport proves about a reply to a load-image command or its read-back.
type platformImageReply interface {
	platformLifecycleProof
	LoadImageReceipt() (journal.LoadImageReceipt, error)
	LoadImageStatus() (journal.LoadImageStatus, error)
}

type platformLoadImageCall struct {
	command                       control.LoadImageCommand
	raw, signature, readSignature []byte
	authority                     control.Authority
	descriptor                    []byte
}

type platformImageBlobCall struct {
	raw, signature []byte
	authority      control.Authority
	prove          transport.ImageBlobProver
	body           io.Reader
}

// platformImageRunner is the runner seam of image delivery: the blob channel and the load-image command. Every
// read returns errPlatformMigrationAbsent for the runner's typed absence and never for anything else.
type platformImageRunner interface {
	SendBlob(ctx context.Context, endpoint platformRuntimeEndpoint, call platformImageBlobCall) (transport.ImageBlobResult, error)
	LoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error)
	ReadLoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error)
}

func (s *Server) platformImageRunnerSeam() platformImageRunner {
	if s.platformImageRunner != nil {
		return s.platformImageRunner
	}
	return platformImageTransport{}
}

type platformImageTransport struct{}

func (platformImageTransport) SendBlob(ctx context.Context, endpoint platformRuntimeEndpoint, call platformImageBlobCall) (transport.ImageBlobResult, error) {
	return platformSendImageBlob(ctx, endpoint, call)
}

func (platformImageTransport) LoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.LoadImageExecute, call.raw, call.signature, call.authority, call.descriptor)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (platformImageTransport) ReadLoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	o, err := platformRunMigrationLifecycle(ctx, endpoint, transport.LoadImageRead, call.raw, call.readSignature, call.authority, call.descriptor)
	if err != nil {
		return nil, migrationReadError(err)
	}
	return o, nil
}

// platformImageTarget is the release an operation is about to start and the image the host loads for it.
type platformImageTarget struct {
	releaseSHA string
	descriptor []byte
	entry      appplatform.BackendImage
	plan       appplatform.ImagePlan
}

// loadPlatformImageTarget finds the release the operation targets and the image for the host platform. It
// returns nil for a release with no managed image to load: a static release, or the development form that names
// a registry reference, which the runner's inspection handles.
func (s *Server) loadPlatformImageTarget(ctx context.Context, lease platformOperationLease) (*platformImageTarget, error) {
	var sha string
	var descriptor []byte
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT r.sha256,r.descriptor FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=?`), lease.id).Scan(&sha, &descriptor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errAppPlatformStageEvidence
		}
		return nil, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != sha {
		return nil, errAppPlatformStageEvidence
	}
	if parsed.Manifest.Backend == nil || len(parsed.Manifest.Backend.Images) == 0 {
		return nil, nil
	}
	pool := migExec{s: s.st, ex: platformContextPool{DB: s.st.db, ctx: ctx}}
	host, err := platformManagedTargetForOperation(pool, lease.id)
	if err != nil {
		return nil, err
	}
	var entry *appplatform.BackendImage
	for i := range parsed.Manifest.Backend.Images {
		if parsed.Manifest.Backend.Images[i].Platform == host.Profile.Platform {
			entry = &parsed.Manifest.Backend.Images[i]
		}
	}
	if entry == nil {
		return nil, errPlatformImagePlatform
	}
	store, err := s.platformImages()
	if err != nil {
		return nil, err
	}
	plan, err := store.planFor(*entry)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPlatformImageBlobs, err)
	}
	// Every blob is hashed again before anything is sent to the runner: a release whose blobs fail the check
	// causes no runner effect at all, and the operation fails definitely.
	for _, blob := range plan.Blobs() {
		file, err := store.openBlob(blob)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errPlatformImageBlobs, err)
		}
		file.Close()
	}
	return &platformImageTarget{releaseSHA: sha, descriptor: bytes.Clone(descriptor), entry: *entry, plan: plan}, nil
}

// platformImageSigner signs the commands of one ensure. It holds the control key for the length of the call and
// is closed by it.
type platformImageSigner struct {
	key       ed25519.PrivateKey
	lease     platformOperationLease
	runtime   platformRuntimeRegistration
	authority control.Authority
}

func (g *platformImageSigner) close() { clear(g.key) }

// newPlatformImageSigner checks that the lease still holds and takes the runtime's control key.
func (s *Server) newPlatformImageSigner(ctx context.Context, expected platformOperationLease) (*platformImageSigner, error) {
	dek, err := s.dek()
	if err != nil {
		return nil, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return nil, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return nil, err
	}
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return nil, fmt.Errorf("image runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return nil, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &platformImageSigner{key: ed25519.NewKeyFromSeed(seed), lease: expected, runtime: runtime, authority: platformImportAuthority(runtime)}, nil
}

// platformLoadImageEffectID is the identity of the load: one per operation and release, the same on every
// attempt.
func platformLoadImageEffectID(operation, release string) string {
	return platformDerivedUUID("load-image", operation+"/"+release)
}

func (g *platformImageSigner) loadCall(t *platformImageTarget) (platformLoadImageCall, error) {
	var zero platformLoadImageCall
	now := time.Now().UnixMilli()
	expires := min(now+25000, g.lease.leaseUntilMS-1000)
	if expires <= now {
		return zero, errAppPlatformLeaseConflict
	}
	command := control.LoadImageCommand{
		CommandHeader: control.CommandHeader{
			Format: control.LoadImageFormat, DeploymentID: g.runtime.deploymentID, ControllerEpoch: g.lease.controllerEpoch,
			ControlRevision: strconv.FormatInt(g.runtime.controlRevision, 10), InstallationID: g.lease.installationID,
			OperationID: g.lease.id, TargetGeneration: strconv.FormatUint(g.lease.snapshot.Generation, 10),
			Stage: control.LoadImageStage, EffectNumber: platformLoadImageEffectNumber,
			EffectID: platformLoadImageEffectID(g.lease.id, t.releaseSHA), Kind: control.LoadImageKind,
			InstallationFence: strconv.FormatInt(g.lease.fence, 10),
		},
		Payload: control.LoadImagePayload{
			ReleaseSHA256: t.releaseSHA, Platform: t.entry.Platform, ManifestDigest: t.entry.ManifestDigest,
			ConfigDigest: t.entry.ConfigDigest, Layers: control.FormatLoadImageLayers(t.entry.Layers),
		},
	}
	digest, err := control.LoadImageLogicalDigest(command)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(expires, 10)
	raw, signature, err := control.SignLoadImage(command, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	read, err := control.SignLoadImageRead(raw, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	return platformLoadImageCall{command: command, raw: raw, signature: signature, readSignature: read, authority: g.authority, descriptor: bytes.Clone(t.descriptor)}, nil
}

func (g *platformImageSigner) blobCall(blob appplatform.ImageBlob, offset int64, body io.Reader) (platformImageBlobCall, error) {
	var zero platformImageBlobCall
	now := time.Now().UnixMilli()
	command := control.ImageBlobCommand{
		Format: control.ImageBlobFormat, DeploymentID: g.runtime.deploymentID, ControllerEpoch: g.lease.controllerEpoch,
		ControlRevision: strconv.FormatInt(g.runtime.controlRevision, 10), InstallationID: g.lease.installationID,
		OperationID: g.lease.id, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		Payload: control.ImageBlobPayload{
			Digest: blob.Digest, Kind: string(blob.Kind), MediaType: blob.MediaType, Size: strconv.FormatInt(blob.Size, 10),
			ResumeOffset: strconv.FormatInt(offset, 10), DiffID: blob.DiffID,
		},
	}
	if blob.Kind == appplatform.ImageBlobLayer {
		command.Payload.UncompressedBytes = strconv.FormatInt(blob.UncompressedBytes, 10)
	}
	raw, signature, err := control.SignImageBlob(command, g.key, g.authority, now)
	if err != nil {
		return zero, err
	}
	return platformImageBlobCall{raw: raw, signature: signature, authority: g.authority, prove: transport.ProveImageBlobWithKey(g.key), body: body}, nil
}

// ensurePlatformReleaseImage makes the image of the operation's target release present on the host before the
// operation sends a start. It returns nil when the image is loaded (or was present), and when there is no managed
// image to load.
func (s *Server) ensurePlatformReleaseImage(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) error {
	target, err := s.loadPlatformImageTarget(ctx, expected)
	if err != nil || target == nil {
		return err
	}
	store, err := s.platformImages()
	if err != nil {
		return err
	}
	names := make([]string, 0, 2+len(target.plan.Layers))
	for _, blob := range target.plan.Blobs() {
		names = append(names, blob.Digest)
	}
	release := store.hold(names...)
	defer release()
	signer, err := s.newPlatformImageSigner(ctx, expected)
	if err != nil {
		return err
	}
	defer signer.close()
	runner := s.platformImageRunnerSeam()
	for round := 0; round < 4; round++ {
		call, err := signer.loadCall(target)
		if err != nil {
			return err
		}
		reply, err := runner.LoadImage(ctx, endpoint, call)
		if err != nil {
			// An uncertain execute is read back under its own identity. It is never sent again under another.
			var readErr error
			if reply, readErr = runner.ReadLoadImage(ctx, endpoint, call); readErr != nil || reply == nil {
				if ctx.Err() != nil {
					return fmt.Errorf("%w: %v", errPlatformImageBusy, err)
				}
				return fmt.Errorf("%w: %v", errPlatformImageUncertain, err)
			}
		}
		done, missing, err := s.interpretPlatformImageReply(reply, call, endpoint)
		if err != nil || done {
			if err == nil {
				s.auditPlatformImageLoad(ctx, expected, call.command)
			}
			return err
		}
		if len(missing) > 0 {
			if err := s.uploadPlatformImageBlobs(ctx, signer, runner, endpoint, store, target, missing); err != nil {
				return err
			}
		}
	}
	return errPlatformImageUncertain
}

// interpretPlatformImageReply turns a reply into done, the digests to upload, or an error. A reply is believed
// only when it answers exactly the command that was signed.
func (s *Server) interpretPlatformImageReply(reply platformImageReply, call platformLoadImageCall, endpoint platformRuntimeEndpoint) (done bool, missing []string, err error) {
	if receipt, rerr := reply.LoadImageReceipt(); rerr == nil {
		if !journal.LoadImageReceiptMatchesCommand(receipt, call.command) ||
			!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, receipt.ObservedMS, receipt.SettledMS, call.command.IssuedMS, false) {
			return false, nil, errAppPlatformStageEvidence
		}
		if receipt.State == "loaded" || receipt.State == "present" {
			return true, nil, nil
		}
		return false, nil, fmt.Errorf("%w: %s", errPlatformImageRejected, receipt.Outcome)
	}
	status, serr := reply.LoadImageStatus()
	if serr != nil || !journal.LoadImageStatusMatchesCommand(status, call.command) ||
		!validPlatformLifecycleProof(reply, call.raw, call.authority, endpoint, status.ObservedMS, "", call.command.IssuedMS, true) {
		return false, nil, errAppPlatformStageEvidence
	}
	switch status.State {
	case "pending":
		return false, nil, errPlatformImageBusy
	case "orphaned", "blob_refused":
		// Nothing is working on it: the same effect is sent again and the runner completes it under its claim.
		return false, nil, nil
	case "blobs_missing":
		return false, status.Missing, nil
	}
	return false, nil, errPlatformImageUncertain
}

// uploadPlatformImageBlobs sends the blobs the runner named, one at a time, each re-hashed on open.
func (s *Server) uploadPlatformImageBlobs(ctx context.Context, signer *platformImageSigner, runner platformImageRunner, endpoint platformRuntimeEndpoint, store *platformImageStore, target *platformImageTarget, digests []string) error {
	byDigest := map[string]appplatform.ImageBlob{}
	for _, blob := range target.plan.Blobs() {
		byDigest[blob.Digest] = blob
	}
	for _, digest := range digests {
		blob, ok := byDigest[digest]
		if !ok {
			// The runner asked for a blob this release does not have.
			return errAppPlatformStageEvidence
		}
		if err := s.uploadPlatformImageBlob(ctx, signer, runner, endpoint, store, blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) uploadPlatformImageBlob(ctx context.Context, signer *platformImageSigner, runner platformImageRunner, endpoint platformRuntimeEndpoint, store *platformImageStore, blob appplatform.ImageBlob) error {
	var offset int64
	for attempt := 0; attempt < 6; attempt++ {
		file, err := store.openBlob(blob)
		if err != nil {
			return fmt.Errorf("%w: %v", errPlatformImageBlobs, err)
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		call, err := signer.blobCall(blob, offset, file)
		if err != nil {
			file.Close()
			return err
		}
		result, err := runner.SendBlob(ctx, endpoint, call)
		file.Close()
		switch {
		case errors.Is(err, transport.ErrImageBlobRefused):
			switch result.Refusal {
			case imagestore.RefusalScratchFull, imagestore.RefusalBusy:
				return fmt.Errorf("%w: %s", errPlatformImageBusy, result.Refusal)
			}
			return fmt.Errorf("%w: %s", errPlatformImageRejected, result.Refusal)
		case err != nil:
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %v", errPlatformImageBusy, err)
			}
			// The runner holds a prefix; the next session asks where it stands.
			continue
		case result.Stored:
			return nil
		case result.ResumeAt > 0:
			offset = result.ResumeAt
		}
	}
	return errPlatformImageUncertain
}

func (s *Server) auditPlatformImageLoad(ctx context.Context, lease platformOperationLease, command control.LoadImageCommand) {
	detail := fmt.Sprintf(`{"effect_id":%q,"release_sha256":%q,"platform":%q}`, command.EffectID, command.Payload.ReleaseSHA256, command.Payload.Platform)
	if _, err := s.st.db.ExecContext(ctx, s.st.bind(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.image_loaded','app_platform_operation',?,?,'')`),
		time.Now().UTC().Format(time.RFC3339), lease.id, detail); err != nil {
		log.Printf("app-platform image audit: %v", err)
	}
}
