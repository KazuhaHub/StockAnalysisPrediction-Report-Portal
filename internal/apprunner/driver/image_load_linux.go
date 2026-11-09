//go:build linux

package driver

import (
	"context"
	"net/http"
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
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: ImageLoadTimeout, DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: allowListTransport{next: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
