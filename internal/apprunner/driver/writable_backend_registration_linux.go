//go:build linux

package driver

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"golang.org/x/sys/unix"
)

func (r *CandidateRuntime) DeliverWritableBackendRegistration(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID, bindingSHA string, document []byte) (WritableBackendRegistrationObservation, error) {
	return r.writableBackendRegistration(ctx, request, expected, nativeID, bindingSHA, document, false)
}
func (r *CandidateRuntime) ObserveWritableBackendRegistration(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID, bindingSHA string, document []byte) (WritableBackendRegistrationObservation, error) {
	return r.writableBackendRegistration(ctx, request, expected, nativeID, bindingSHA, document, true)
}

// No app-supplied action, path, mount or mode. Unknown recovery cannot enter the
// create branch. Both paths require full current native policy and readiness.
func (r *CandidateRuntime) writableBackendRegistration(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID, bindingSHA string, document []byte, observeOnly bool) (WritableBackendRegistrationObservation, error) {
	var zero WritableBackendRegistrationObservation
	if r == nil || !containerSHA.MatchString(nativeID) || !containerSHA.MatchString(bindingSHA) || len(document) == 0 || len(document) > 4096 || len(request.Descriptor) == 0 || len(request.Descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrContainerUncertain
	}
	document = bytes.Clone(document)
	request.Descriptor = bytes.Clone(request.Descriptor)
	defer clear(document)
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		return zero, ErrContainerUncertain
	}
	defer registration.Close()
	metadata, err := registration.Metadata()
	if err != nil {
		return zero, ErrContainerUncertain
	}
	descriptor, err := appplatform.ParseDescriptor(request.Descriptor)
	i, m := request.Identity, metadata.Identity
	if err != nil || descriptor.Manifest.Backend == nil || descriptor.SHA256 != i.ReleaseSHA256 || m.AppID != descriptor.Manifest.ID || m.DeploymentID != i.DeploymentID || m.ControllerEpoch != i.ControllerEpoch || m.InstallationID != i.InstallationID || m.Generation != i.Generation || m.Generation != "1" || m.PolicyRevision != "1" || m.ReleaseSHA256 != i.ReleaseSHA256 {
		return zero, ErrContainerUncertain
	}
	var file unix.Stat_t
	actual, err := r.withVerifiedWritableCandidate(ctx, request, expected, nativeID, func(ctx context.Context, pins *candidateMountPins) error {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		response, err := probeCandidateReadiness(bounded, filepath.Join(r.roots.Sockets, i.InstallationID, i.Generation, "http", "app.sock"), 65532, 65532)
		if err != nil || !response.Ready || response.DataFormat != strconv.Itoa(descriptor.Manifest.Data.Schema) {
			return ErrCandidateNotReady
		}
		path := filepath.Join(r.roots.Sockets, i.InstallationID, i.Generation, "broker")
		parent := -1
		for _, pin := range pins.dirs {
			if pin.path == path {
				parent = int(pin.file.Fd())
				break
			}
		}
		if parent < 0 {
			return ErrUnsafeSocket
		}
		dir, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnsafeSocket
		}
		defer unix.Close(dir)
		if !observeOnly {
			if err := createBackendRegistrationFile(ctx, dir, document, bindingSHA, 65532, 65532); err != nil {
				return ErrContainerUncertain
			}
		}
		file, err = observeBackendRegistrationFile(ctx, dir, document, bindingSHA, 65532, 65532)
		if err != nil {
			return ErrContainerUncertain
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	result := WritableBackendRegistrationObservation{valid: true, snapshot: WritableBackendRegistrationSnapshot{Launch: actual.Launch, NativeID: actual.NativeID, BindingSHA256: bindingSHA, DocumentSHA256: containerHash(document), Device: uint64(file.Dev), Inode: file.Ino, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}
