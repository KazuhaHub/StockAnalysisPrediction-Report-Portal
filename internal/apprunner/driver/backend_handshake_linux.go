//go:build linux

package driver

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// ExchangeCandidateHandshake is read-only and targets one fixed application
// method/path. It checks the exact delivered file and current native policy;
// it never receives a URL, key, cookie, viewer or app-selected native ID.
func (r *CandidateRuntime) ExchangeCandidateHandshake(ctx context.Context, launch CandidateLaunch, receipt BackendRegistrationSnapshot, challenge string) (BackendHandshakeObservation, error) {
	var zero BackendHandshakeObservation
	if r == nil || launch.state == nil || launch.state.owner != r || !containerSHA.MatchString(receipt.NativeID) || !containerSHA.MatchString(receipt.BindingSHA256) || !containerSHA.MatchString(receipt.DocumentSHA256) || receipt.Inode == 0 || !appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrContainerUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := launch.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.closed || s.closed || s.used || receipt.Launch != s.snapshot {
		return zero, ErrContainerUncertain
	}
	check := func() error { return r.checkBackendBindingLocked(ctx, s, receipt) }
	if err := check(); err != nil {
		return zero, err
	}
	path := filepath.Join(r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation, "http", "app.sock")
	answer, err := exchangeBackendHandshake(ctx, path, 65532, 65532, challenge)
	if err != nil {
		return zero, err
	}
	if err := check(); err != nil {
		return zero, err
	}
	return BackendHandshakeObservation{snapshot: BackendHandshakeSnapshot{Launch: s.snapshot, NativeID: receipt.NativeID, BindingSHA256: receipt.BindingSHA256, DocumentSHA256: receipt.DocumentSHA256, Device: receipt.Device, Inode: receipt.Inode, ChallengeSHA256: containerHash([]byte(challenge)), Answer: answer, ObservedMS: time.Now().UnixMilli()}, valid: true}, nil
}
func exchangeBackendHandshake(ctx context.Context, path string, uid, gid uint32, challenge string) (string, error) {
	body, err := appplatform.BackendHandshakeRequest(challenge)
	if err != nil {
		return "", ErrContainerUncertain
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096, DialContext: readinessSocketDialer(path, uid, gid)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://portal-app.invalid/v1/handshake", bytes.NewReader(body))
	if err != nil {
		return "", ErrContainerUncertain
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", ErrContainerUncertain
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" {
		return "", ErrContainerUncertain
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || ctx.Err() != nil {
		return "", ErrContainerUncertain
	}
	return appplatform.ParseBackendHandshakeAnswer(raw)
}

// Caller holds runtime and launch locks. Shared by read-only handshake and
// explicit request forwarding so both enforce the same physical/file boundary.
func (r *CandidateRuntime) checkBackendBindingLocked(ctx context.Context, s *candidateLaunchState, receipt BackendRegistrationSnapshot) error {
	if err := s.pins.validate(ctx, s.plan); err != nil {
		return err
	}
	if err := r.checkData(ctx, s.snapshot.Identity, s.snapshot.Data); err != nil {
		return err
	}
	client, closeClient := r.client()
	defer closeClient()
	state, err := inspectCandidateContainerHTTP(ctx, client, receipt.NativeID, s.plan)
	if err != nil || state != "running" {
		return ErrContainerUncertain
	}
	path := filepath.Join(r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation, "broker")
	parent := -1
	for _, pin := range s.pins.dirs {
		if pin.path == path {
			parent = int(pin.file.Fd())
			break
		}
	}
	if parent < 0 {
		return ErrUnsafeSocket
	}
	document, st, err := readBackendRegistrationFile(ctx, parent, receipt.BindingSHA256, 65532, 65532)
	defer clear(document)
	if err != nil || uint64(st.Dev) != receipt.Device || st.Ino != receipt.Inode || containerHash(document) != receipt.DocumentSHA256 {
		return ErrUnsafeSocket
	}
	return ctx.Err()
}
