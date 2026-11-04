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

type ServiceTaskSnapshot struct {
	Binding               BackendRegistrationSnapshot
	BodySHA256            string
	ServiceIdentitySHA256 string
	Response              BackendResponse
	ObservedMS            int64
}

type ServiceTaskObservation struct {
	snapshot ServiceTaskSnapshot
	valid    bool
}

func (o ServiceTaskObservation) Snapshot() (ServiceTaskSnapshot, error) {
	if !o.valid {
		return ServiceTaskSnapshot{}, ErrContainerUncertain
	}
	s := o.snapshot
	s.Response.Body = bytes.Clone(s.Response.Body)
	return s, nil
}

func (r *CandidateRuntime) ForwardServiceTask(
	ctx context.Context,
	launch CandidateLaunch,
	binding BackendRegistrationSnapshot,
	body, serviceIdentity []byte,
) (ServiceTaskObservation, error) {
	var zero ServiceTaskObservation
	if r == nil || launch.state == nil || launch.state.owner != r ||
		!containerSHA.MatchString(binding.NativeID) ||
		!containerSHA.MatchString(binding.BindingSHA256) ||
		!containerSHA.MatchString(binding.DocumentSHA256) ||
		binding.Inode == 0 || len(body) == 0 ||
		len(body) > appplatform.MaxTaskBackendBody ||
		len(serviceIdentity) == 0 || len(serviceIdentity) > 8192 {
		return zero, ErrContainerUncertain
	}
	body = bytes.Clone(body)
	serviceIdentity = bytes.Clone(serviceIdentity)
	defer clear(body)
	defer clear(serviceIdentity)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r.mu.Lock()
	s := launch.state
	s.mu.Lock()
	if r.closed || s.closed || s.used || binding.Launch != s.snapshot {
		s.mu.Unlock()
		r.mu.Unlock()
		return zero, ErrContainerUncertain
	}
	r.mu.Unlock()
	defer s.mu.Unlock()
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		return zero, err
	}
	socket := filepath.Join(r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation, "http", "app.sock")
	response, err := forwardServiceTask(ctx, socket, 65532, 65532, body, serviceIdentity)
	if err != nil {
		return zero, err
	}
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		return zero, err
	}
	return ServiceTaskObservation{
		snapshot: ServiceTaskSnapshot{
			Binding: binding, BodySHA256: containerHash(body),
			ServiceIdentitySHA256: containerHash(serviceIdentity),
			Response:              response, ObservedMS: time.Now().UnixMilli(),
		},
		valid: true,
	}, nil
}

func forwardServiceTask(ctx context.Context, socket string, uid, gid uint32, body, serviceIdentity []byte) (BackendResponse, error) {
	var zero BackendResponse
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 4096,
		DialContext: readinessSocketDialer(socket, uid, gid),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, "http://portal-app.invalid"+appplatform.TaskBackendPath,
		bytes.NewReader(body))
	if err != nil {
		return zero, ErrContainerUncertain
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(appplatform.TaskServiceIdentityHeader, string(serviceIdentity))
	response, err := client.Do(request)
	if err != nil {
		return zero, ErrContainerUncertain
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" ||
		response.Header.Get("Content-Encoding") != "" {
		return zero, ErrContainerUncertain
	}
	raw, err := io.ReadAll(io.LimitReader(
		response.Body, appplatform.MaxTaskResultBytes+1))
	if err != nil || ctx.Err() != nil ||
		len(raw) == 0 || len(raw) > appplatform.MaxTaskResultBytes ||
		!appplatform.ValidBackendResponse(response.StatusCode, raw) {
		clear(raw)
		return zero, ErrContainerUncertain
	}
	return BackendResponse{Status: response.StatusCode, Body: raw}, nil
}
