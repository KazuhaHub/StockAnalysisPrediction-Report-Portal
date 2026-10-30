//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"golang.org/x/sys/unix"
)

// This is a bounded point-in-time inventory check under admitted lifecycle
// exclusion, not a global lock against a host administrator changing Docker or
// exposing private path aliases. Running AND stopped containers are inspected.
func requireNoContainerMountReferences(ctx context.Context, client *http.Client, path string) error {
	containers, err := readCandidateIPCInventory(ctx, client)
	if err != nil {
		return err
	}
	if containers == nil {
		return ErrInvalidResponse
	}
	for _, container := range containers {
		if !containerSHA.MatchString(container.ID) {
			return ErrInvalidResponse
		}
		sources, err := candidateIPCMountSources(container.Mounts)
		if err != nil {
			return err
		}
		for _, source := range sources {
			referenced, err := candidateIPCReferenced(ctx, path, source)
			if err != nil {
				return err
			}
			if referenced {
				return ErrContainerConflict
			}
		}
	}
	return ctx.Err()
}

func closeDataWriterPins(pins []mountDirectoryPin) {
	for _, p := range pins {
		p.file.Close()
	}
}
func validateDataWriterPins(ctx context.Context, pins []mountDirectoryPin, writable bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pins) == 0 {
		return ErrUnsafeSocket
	}
	gid, mode := uint32(0), uint32(0700)
	if writable {
		gid, mode = 65532, 02770
	}
	fresh, err := openMountDirectory(pins[len(pins)-1].path, 0, gid, mode)
	if err != nil {
		return err
	}
	defer closeDataWriterPins(fresh)
	if len(fresh) != len(pins) {
		return ErrUnsafeSocket
	}
	for i, pin := range pins {
		want := pin.stat
		if i == len(pins)-1 {
			want.Gid, want.Mode = gid, (want.Mode&^07777)|mode
		}
		var held unix.Stat_t
		if unix.Fstat(int(pin.file.Fd()), &held) != nil || !mountStatEqual(held, want) || !mountStatEqual(fresh[i].stat, want) {
			return ErrUnsafeSocket
		}
	}
	return ctx.Err()
}

// EnableDataWriter is a concrete initial permission effect, not a container
// start or logical grant. Caller must own the current one-shot journal claim and
// lifecycle exclusion. There is no application/wire caller of this method.
// Every error after the permission provider is invoked is uncertain; no retry,
// chmod repair, forced stop or deletion is performed here.
func (r *CandidateRuntime) EnableDataWriter(ctx context.Context, request DataWriterRequest) (DataWriterObservation, error) {
	var zero DataWriterObservation
	if r == nil || os.Geteuid() != 0 {
		return zero, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	base := CandidateLaunchRequest{Identity: request.Identity, Descriptor: request.Descriptor, Platform: request.Platform, Data: request.Data}
	if r.closed || r.root == nil || !validDataWriterBinding(request.Binding) || !candidateRequestValid(base) || request.Platform != "linux/"+runtime.GOARCH || request.Identity.Generation != "1" || request.Identity.WriterFence != "1" || candidateSeccompError != nil {
		return zero, ErrInvalidResponse
	}
	request.Descriptor = bytes.Clone(request.Descriptor)
	for launch := range r.launches {
		if launch.snapshot.Identity.NamespaceID == request.Identity.NamespaceID {
			return zero, ErrContainerConflict
		}
	}
	if err := r.checkData(ctx, request.Identity, request.Data); err != nil {
		return zero, err
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil || image.Platform != request.Platform {
		return zero, errors.Join(ErrInvalidResponse, err)
	}
	path := filepath.Join(r.roots.Data, request.Identity.NamespaceID)
	pins, err := openMountDirectory(path, 0, 0, 0700)
	if err != nil {
		return zero, err
	}
	defer closeDataWriterPins(pins)
	if len(pins) < 2 || pins[len(pins)-2].stat.Ino != r.rootIdentity.Inode || uint64(pins[len(pins)-2].stat.Dev) != request.Data.Device || pins[len(pins)-1].stat.Ino != request.Data.NamespaceInode || uint64(pins[len(pins)-1].stat.Dev) != request.Data.Device {
		return zero, ErrUnsafeSocket
	}
	client, closeClient := r.client()
	defer closeClient()
	if err := requireNoContainerMountReferences(ctx, client, path); err != nil {
		return zero, err
	}
	if err := validateDataWriterPins(ctx, pins, false); err != nil {
		return zero, err
	}
	if err := r.checkData(ctx, request.Identity, request.Data); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	allocation := data.Observation{NamespaceID: request.Identity.NamespaceID, ProjectID: request.Data.ProjectID, Device: request.Data.Device, Inode: request.Data.NamespaceInode, ByteLimit: request.Data.QuotaBytes, InodeLimit: request.Data.QuotaInodes, Inherited: true}
	physical, err := r.root.EnableWriteAccess(ctx, request.Data.Root, allocation)
	if err != nil {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	if err := validateDataWriterPins(ctx, pins, true); err != nil {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	if err := requireNoContainerMountReferences(ctx, client, path); err != nil {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	current, err := r.root.InspectWriteAccess(ctx, request.Data.Root, request.Identity.NamespaceID, request.Data.ProjectID)
	if err != nil || current.Namespace != physical.Namespace {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	if err := validateDataWriterPins(ctx, pins, true); err != nil {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrDataWriterUncertain, err)
	}
	result := DataWriterObservation{valid: true, snapshot: DataWriterSnapshot{Binding: request.Binding, Identity: request.Identity, Data: request.Data, ImageID: image.ImageID, Platform: request.Platform, Profile: DataWriterSandboxProfile, SeccompSHA256: containerHash([]byte(candidateSeccompOption)), UsedBytes: current.Namespace.UsedBytes, UsedInodes: current.Namespace.UsedInodes, ObservedMS: time.Now().UnixMilli()}}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}
