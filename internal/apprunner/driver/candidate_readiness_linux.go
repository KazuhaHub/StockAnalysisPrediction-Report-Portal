//go:build linux

package driver

import (
	"context"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// CheckCandidateReadiness performs fresh native/policy and private application
// checks without changing the journal, starting a process or granting access.
func (r *CandidateRuntime) CheckCandidateReadiness(ctx context.Context, launch CandidateLaunch, nativeID, dataFormat string) (CandidateReadinessObservation, error) {
	var zero CandidateReadinessObservation
	if r == nil || launch.state == nil || launch.state.owner != r || !containerSHA.MatchString(nativeID) || !positiveContainerCounter(dataFormat) {
		return zero, ErrCandidateNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := launch.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.closed || s.closed || s.used {
		return zero, ErrCandidateNotReady
	}
	if err := s.pins.validate(ctx, s.plan); err != nil {
		return zero, err
	}
	if err := r.checkData(ctx, s.snapshot.Identity, s.snapshot.Data); err != nil {
		return zero, err
	}
	client, closeClient := r.client()
	defer closeClient()
	state, err := inspectCandidateContainerHTTP(ctx, client, nativeID, s.plan)
	if err != nil || state != "running" {
		return zero, ErrCandidateNotReady
	}
	path := filepath.Join(r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation, "http", "app.sock")
	response, err := probeCandidateReadiness(ctx, path, 65532, 65532)
	if err != nil || !response.Ready || response.DataFormat != dataFormat {
		return zero, ErrCandidateNotReady
	}
	if err := s.pins.validate(ctx, s.plan); err != nil {
		return zero, err
	}
	if err := r.checkData(ctx, s.snapshot.Identity, s.snapshot.Data); err != nil {
		return zero, err
	}
	state, err = inspectCandidateContainerHTTP(ctx, client, nativeID, s.plan)
	if err != nil || state != "running" || ctx.Err() != nil {
		return zero, ErrCandidateNotReady
	}
	return CandidateReadinessObservation{snapshot: CandidateReadinessSnapshot{Launch: s.snapshot, NativeID: nativeID, Protocol: response.Protocol, DataFormat: response.DataFormat, ObservedMS: time.Now().UnixMilli()}, valid: true}, nil
}

func readinessSocketInfo(path string, uid, gid uint32) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 107 {
		return nil, ErrUnsafeSocket
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, ErrUnsafeSocket
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return nil, ErrUnsafeSocket
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uid || st.Gid != gid || st.Nlink != 1 {
		return nil, ErrUnsafeSocket
	}
	return info, nil
}

func readinessSocketDialer(path string, uid, gid uint32) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		before, err := readinessSocketInfo(path, uid, gid)
		if err != nil {
			return nil, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			return nil, ErrCandidateNotReady
		}
		refuse := func() (net.Conn, error) { conn.Close(); return nil, ErrUnsafeSocket }
		u, ok := conn.(*net.UnixConn)
		if !ok {
			return refuse()
		}
		raw, err := u.SyscallConn()
		if err != nil {
			return refuse()
		}
		var cred *syscall.Ucred
		var credErr error
		if err := raw.Control(func(fd uintptr) {
			cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		}); err != nil || credErr != nil || cred == nil || cred.Uid != uid || cred.Gid != gid {
			return refuse()
		}
		after, err := readinessSocketInfo(path, uid, gid)
		if err != nil || !os.SameFile(before, after) {
			return refuse()
		}
		return conn, nil
	}
}

func probeCandidateReadiness(ctx context.Context, path string, uid, gid uint32) (appplatform.BackendReadiness, error) {
	var zero appplatform.BackendReadiness
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 4096, DialContext: readinessSocketDialer(path, uid, gid)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://portal-app.invalid/health/ready", nil)
	if err != nil {
		return zero, ErrCandidateNotReady
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return zero, ErrCandidateNotReady
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" {
		return zero, ErrCandidateNotReady
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || ctx.Err() != nil {
		return zero, ErrCandidateNotReady
	}
	return appplatform.ParseBackendReadiness(body)
}
