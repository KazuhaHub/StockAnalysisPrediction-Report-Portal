//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ObserveDataWriter never mutates permissions, starts/stops a container or
// reconstructs an execution handle. The journal must independently authorize a
// fresh observation of one exact expired unknown claim.
func (r *CandidateRuntime) ObserveDataWriter(ctx context.Context, request DataWriterRequest) (DataWriterRecoveryObservation, error) {
	var zero DataWriterRecoveryObservation
	if r == nil || os.Geteuid() != 0 {
		return zero, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	base := CandidateLaunchRequest{Identity: request.Identity, Descriptor: request.Descriptor, Platform: request.Platform, Data: request.Data}
	if r.closed || r.root == nil || request.Data.Root != r.rootIdentity || !validDataWriterBinding(request.Binding) || !candidateRequestValid(base) || request.Platform != "linux/"+runtime.GOARCH || request.Identity.Generation != "1" || request.Identity.WriterFence != "1" || candidateSeccompError != nil {
		return zero, ErrInvalidResponse
	}
	request.Descriptor = bytes.Clone(request.Descriptor)
	for launch := range r.launches {
		if launch.snapshot.Identity.NamespaceID == request.Identity.NamespaceID {
			return zero, ErrContainerConflict
		}
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil || image.Platform != request.Platform {
		return zero, errors.Join(ErrInvalidResponse, err)
	}
	path := filepath.Join(r.roots.Data, request.Identity.NamespaceID)
	pins, err := openMountDirectory(path, 0, 65532, 02770)
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
	before, err := r.root.InspectWriteAccess(ctx, request.Data.Root, request.Identity.NamespaceID, request.Data.ProjectID)
	if err != nil {
		return zero, err
	}
	n, d := before.Namespace, request.Data
	if n.NamespaceID != request.Identity.NamespaceID || n.ProjectID != d.ProjectID || n.Device != d.Device || n.Inode != d.NamespaceInode || n.ByteLimit != d.QuotaBytes || n.InodeLimit != d.QuotaInodes || !n.Inherited || n.UsedBytes < 0 || n.UsedBytes > n.ByteLimit || n.UsedInodes < 0 || n.UsedInodes > n.InodeLimit {
		return zero, ErrUnsafeSocket
	}
	if err := validateDataWriterPins(ctx, pins, true); err != nil {
		return zero, err
	}
	if err := requireNoContainerMountReferences(ctx, client, path); err != nil {
		return zero, err
	}
	after, err := r.root.InspectWriteAccess(ctx, d.Root, request.Identity.NamespaceID, d.ProjectID)
	if err != nil || after != before {
		return zero, errors.Join(ErrUnsafeSocket, err)
	}
	if err := validateDataWriterPins(ctx, pins, true); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	observed := DataWriterRecoveryObservation{valid: true, snapshot: DataWriterSnapshot{Binding: request.Binding, Identity: request.Identity, Data: d, ImageID: image.ImageID, Platform: request.Platform, Profile: DataWriterSandboxProfile, SeccompSHA256: DataWriterPolicySHA256(), UsedBytes: n.UsedBytes, UsedInodes: n.UsedInodes, ObservedMS: time.Now().UnixMilli()}}
	if _, err := observed.Snapshot(); err != nil {
		return zero, err
	}
	return observed, nil
}
