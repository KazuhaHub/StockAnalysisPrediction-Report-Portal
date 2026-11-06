//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"golang.org/x/sys/unix"
)

func (r *CandidateRuntime) DeliverUpdateStartRegistration(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, nativeID, bindingSHA string, document []byte) (UpdateStartRegistrationObservation, error) {
	return r.updateStartRegistration(ctx, request, expected, nativeID, bindingSHA, document, false)
}

func (r *CandidateRuntime) ObserveUpdateStartRegistration(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, nativeID, bindingSHA string, document []byte) (UpdateStartRegistrationObservation, error) {
	return r.updateStartRegistration(ctx, request, expected, nativeID, bindingSHA, document, true)
}

// Unknown recovery cannot enter the create branch. Both paths require the
// exact current container, native policy, writable data profile and readiness.
func (r *CandidateRuntime) updateStartRegistration(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, nativeID, bindingSHA string, document []byte, observeOnly bool) (UpdateStartRegistrationObservation, error) {
	var zero UpdateStartRegistrationObservation
	if r == nil || !containerSHA.MatchString(nativeID) ||
		!containerSHA.MatchString(bindingSHA) || len(document) == 0 ||
		len(document) > 4096 || len(request.Descriptor) == 0 ||
		len(request.Descriptor) > appplatform.MaxDescriptorBytes {
		return zero, fmt.Errorf(
			"update-start registration arguments: %w",
			ErrContainerUncertain)
	}
	document = bytes.Clone(document)
	request.Descriptor = bytes.Clone(request.Descriptor)
	defer clear(document)
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration document: %w",
			ErrContainerUncertain)
	}
	defer registration.Close()
	metadata, metadataErr := registration.Metadata()
	descriptor, err := appplatform.ParseDescriptor(request.Descriptor)
	i, m := request.Identity, metadata.Identity
	if metadataErr != nil || err != nil ||
		descriptor.Manifest.Backend == nil ||
		descriptor.SHA256 != i.ReleaseSHA256 ||
		m.AppID != descriptor.Manifest.ID ||
		m.DeploymentID != i.DeploymentID ||
		m.ControllerEpoch != i.ControllerEpoch ||
		m.InstallationID != i.InstallationID ||
		m.Generation != i.Generation || m.Generation == "1" ||
		m.PolicyRevision != request.Binding.PolicyRevision ||
		m.ReleaseSHA256 != i.ReleaseSHA256 {
		return zero, fmt.Errorf(
			"update-start registration identity: %w",
			ErrContainerUncertain)
	}
	var file unix.Stat_t
	actual, err := r.withVerifiedUpdateStartCandidate(ctx, request, expected, nativeID, func(ctx context.Context, pins *candidateMountPins) error {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		readinessPath := filepath.Join(r.roots.Sockets, i.InstallationID, i.Generation, "http", "app.sock")
		response, err := probeCandidateReadiness(bounded, readinessPath, 65532, 65532)
		if err != nil || !response.Ready ||
			response.DataFormat != strconv.Itoa(descriptor.Manifest.Data.Schema) {
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
				return fmt.Errorf(
					"update-start registration file create: %w",
					errors.Join(ErrContainerUncertain, err))
			}
		}
		file, err = observeBackendRegistrationFile(ctx, dir, document, bindingSHA, 65532, 65532)
		if err != nil {
			return fmt.Errorf(
				"update-start registration file observe: %w",
				errors.Join(ErrContainerUncertain, err))
		}
		return nil
	})
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration native check: %w", err)
	}
	result := UpdateStartRegistrationObservation{
		valid: true,
		snapshot: UpdateStartRegistrationSnapshot{
			Launch: actual.Launch, NativeID: actual.NativeID,
			BindingSHA256: bindingSHA, DocumentSHA256: containerHash(document),
			Device: uint64(file.Dev), Inode: file.Ino, ObservedMS: actual.ObservedMS,
		},
	}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}
