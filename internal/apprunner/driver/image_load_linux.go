//go:build linux

package driver

import (
	"context"
	"runtime"
)

// LoadImage loads one verified managed image into the daemon from runner-private scratch blobs. The caller holds
// the journal's load lock and the durable unknown claim, and passes a context that outlives its connection and
// is bounded by ImageLoadTimeout. No retry is made inside: an error is uncertain and the same claim is completed
// by running it again.
func (r *CandidateRuntime) LoadImage(ctx context.Context, request LoadImageRequest) (LoadImageSnapshot, error) {
	if r == nil {
		return LoadImageSnapshot{}, ErrUnsupported
	}
	r.mu.Lock()
	closed := r.closed
	socket := r.socket
	r.mu.Unlock()
	if closed || request.Platform != "linux/"+runtime.GOARCH {
		return LoadImageSnapshot{}, ErrInvalidResponse
	}
	// The load streams a large body, so the client has no overall timeout: the context bounds it.
	client, closeClient := newDockerClient(socket, ImageLoadTimeout, 0)
	defer closeClient()
	return loadImageHTTP(ctx, client, request, runtime.GOARCH)
}

// LoadedImagePresent reports whether the managed image a descriptor implies is already in the daemon and valid. It
// is read-only, so a read-back may ask it.
func (r *CandidateRuntime) LoadedImagePresent(ctx context.Context, descriptor []byte, releaseSHA256, platform string) (bool, error) {
	if r == nil {
		return false, ErrUnsupported
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return false, ErrInvalidResponse
	}
	client, closeClient := r.client()
	defer closeClient()
	return loadedImagePresentHTTP(ctx, client, descriptor, releaseSHA256, platform, runtime.GOARCH)
}

// CheckBackendImage is the read-only gate every start path runs before it claims anything: the image a descriptor
// binds must be present and valid in the daemon, or the start is refused with the typed ErrImageMissing (a managed
// image that was pruned, or never loaded on this host) or ErrReferenceImageRefused (a registry reference under the
// production profile). Nothing is pulled and nothing is changed.
func (r *CandidateRuntime) CheckBackendImage(ctx context.Context, descriptor []byte, releaseSHA256 string) error {
	if r == nil {
		return ErrUnsupported
	}
	r.mu.Lock()
	closed, socket, policy := r.closed, r.socket, r.imagePolicy
	r.mu.Unlock()
	if closed {
		return ErrInvalidResponse
	}
	_, err := InspectBackendImage(ctx, socket, descriptor, releaseSHA256, policy)
	return err
}
