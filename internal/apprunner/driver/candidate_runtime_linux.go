//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

type CandidateRuntime struct {
	mu           sync.Mutex
	closed       bool
	socket       string
	roots        containerRoots
	root         *data.Root
	rootIdentity data.RootIdentity
	launches     map[*candidateLaunchState]struct{}
}
type candidateLaunchState struct {
	mu           sync.Mutex
	owner        *CandidateRuntime
	closed, used bool
	plan         containerPlan
	pins         *candidateMountPins
	snapshot     CandidateLaunchSnapshot
}

// OpenCandidateRuntime opens the explicitly provisioned private data root. It
// creates no directory, enrolls no controller and starts no service or process.
// The concrete root is borrowed from the shared provider; its owner must keep it
// open until runtime work settles. Runtime.Close never closes that shared root.
// Socket/data/sockets paths are infrastructure configuration, never app input.
func OpenCandidateRuntime(ctx context.Context, socket, dataPath, socketsPath string, root *data.Root, expected data.RootIdentity) (*CandidateRuntime, error) {
	if os.Geteuid() != 0 || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validContainerRoot(dataPath) || !validContainerRoot(socketsPath) || expected.Inode == 0 {
		return nil, ErrInvalidResponse
	}
	if _, err := socketInfo(socket, 0); err != nil {
		return nil, err
	}
	report, err := InspectDaemon(ctx, socket)
	if err != nil {
		return nil, err
	}
	if !report.MetadataCompatible {
		return nil, ErrUnsupported
	}
	dirs, err := openMountDirectory(dataPath, 0, 0, 0700)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, p := range dirs {
			p.file.Close()
		}
	}()
	leaf := dirs[len(dirs)-1]
	if leaf.stat.Ino != expected.Inode {
		return nil, ErrUnsafeSocket
	}
	if root == nil {
		return nil, ErrInvalidResponse
	}
	actual, err := root.Identity(ctx)
	if err != nil || actual != expected {
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafeSocket
	}
	return &CandidateRuntime{socket: socket, roots: containerRoots{Data: dataPath, Sockets: socketsPath}, root: root, rootIdentity: expected, launches: map[*candidateLaunchState]struct{}{}}, nil
}
func (r *CandidateRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var err error
	for state := range r.launches {
		state.mu.Lock()
		state.closed = true
		err = errors.Join(err, state.pins.Close())
		state.mu.Unlock()
		delete(r.launches, state)
	}
	return err
}
func (c CandidateLaunch) Close() error {
	if c.state == nil {
		return nil
	}
	r := c.state.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.closed {
		return nil
	}
	c.state.closed = true
	delete(r.launches, c.state)
	return c.state.pins.Close()
}
func (c CandidateLaunch) Snapshot() (CandidateLaunchSnapshot, error) {
	if c.state == nil {
		return CandidateLaunchSnapshot{}, ErrContainerUncertain
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.closed {
		return CandidateLaunchSnapshot{}, ErrContainerUncertain
	}
	return c.state.snapshot, nil
}
func (r *CandidateRuntime) checkData(ctx context.Context, identity CandidateIdentity, evidence CandidateDataEvidence) error {
	if evidence.Root != r.rootIdentity {
		return ErrUnsafeSocket
	}
	observed, err := r.root.Inspect(ctx, evidence.Root, identity.NamespaceID, evidence.ProjectID)
	if err != nil {
		return err
	}
	if observed.NamespaceID != identity.NamespaceID || observed.ProjectID != evidence.ProjectID || observed.Device != evidence.Device || observed.Inode != evidence.NamespaceInode || observed.ByteLimit != evidence.QuotaBytes || observed.InodeLimit != evidence.QuotaInodes || !observed.Inherited || observed.UsedBytes < 0 || observed.UsedBytes > observed.ByteLimit || observed.UsedInodes < 0 || observed.UsedInodes > observed.InodeLimit {
		return ErrUnsafeSocket
	}
	return nil
}
func (r *CandidateRuntime) client() (*http.Client, func()) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: probeTimeout, DialContext: dockerDialer(r.socket)}
	return daemonClient(transport), transport.CloseIdleConnections
}

// PrepareCandidate is read-only. It verifies the concrete quota/data root,
// immutable image and native mount identities but does not grant execution.
func (r *CandidateRuntime) PrepareCandidate(ctx context.Context, request CandidateLaunchRequest) (CandidateLaunch, error) {
	return r.prepareCandidateProfile(ctx, request, nil)
}
func (r *CandidateRuntime) prepareCandidateProfile(ctx context.Context, request CandidateLaunchRequest, writable *WritableCandidateBinding) (CandidateLaunch, error) {
	var zero CandidateLaunch
	if r == nil {
		return zero, ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.root == nil || r.launches == nil || len(r.launches) >= 32 || !candidateRequestValid(request) || request.Platform != "linux/"+runtime.GOARCH {
		return zero, ErrInvalidResponse
	}
	request.Descriptor = bytes.Clone(request.Descriptor)
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, writable != nil); err != nil {
		return zero, err
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil {
		return zero, err
	}
	var plan containerPlan
	if writable == nil {
		plan, err = planCandidateContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots)
	} else {
		if err := r.requireWritableCandidateExclusion(ctx, request, nil); err != nil {
			return zero, err
		}
		plan, err = planWritableCandidateContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots, *writable)
	}
	if err != nil {
		return zero, err
	}
	pins, err := pinCandidateMounts(ctx, plan, candidateDataPin{Device: request.Data.Device, Inode: request.Data.NamespaceInode})
	if err != nil {
		return zero, err
	}
	fail := func(err error) (CandidateLaunch, error) { pins.Close(); return zero, err }
	// A configured pathname must resolve to the same root that the native quota
	// provider holds, not merely to a directory with a matching child basename.
	type directory struct {
		Device, Inode  uint64
		UID, GID, Mode uint32
	}
	metadata := map[string]directory{}
	foundRoot := false
	for _, p := range pins.dirs {
		if p.path == r.roots.Data {
			if p.stat.Ino != r.rootIdentity.Inode || uint64(p.stat.Dev) != request.Data.Device {
				return fail(ErrUnsafeSocket)
			}
			foundRoot = true
		}
	}
	if !foundRoot {
		return fail(ErrUnsafeSocket)
	}
	var body containerCreate
	if json.Unmarshal(plan.body, &body) != nil {
		return fail(ErrInvalidResponse)
	}
	for _, mount := range body.HostConfig.Mounts {
		for _, p := range pins.dirs {
			if p.path == mount.Source {
				metadata[mount.Target] = directory{Device: uint64(p.stat.Dev), Inode: p.stat.Ino, UID: p.stat.Uid, GID: p.stat.Gid, Mode: p.stat.Mode & 07777}
			}
		}
	}
	if len(metadata) != 3 {
		return fail(ErrUnsafeSocket)
	}
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, writable != nil); err != nil {
		return fail(err)
	}
	state := &candidateLaunchState{owner: r, plan: plan, pins: pins, snapshot: CandidateLaunchSnapshot{Identity: request.Identity, ImageID: image.ImageID, Platform: image.Platform, PolicySHA256: plan.bodySHA, MountsSHA256: candidateSnapshotHash(metadata), Data: request.Data}}
	r.launches[state] = struct{}{}
	return CandidateLaunch{state: state}, nil
}

// StartCandidate consumes one live native preflight handle. Callers must persist
// its snapshot under current signed journal authority before this invocation.
// Any possibly effectful error remains uncertain; this function never retries.
func (r *CandidateRuntime) StartCandidate(ctx context.Context, launch CandidateLaunch) (CandidateRunningObservation, error) {
	return r.startCandidateProfile(ctx, launch, nil)
}
func (r *CandidateRuntime) startCandidateProfile(ctx context.Context, launch CandidateLaunch, writable *WritableCandidateBinding) (CandidateRunningObservation, error) {
	var zero CandidateRunningObservation
	if r == nil || launch.state == nil || launch.state.owner != r {
		return zero, ErrInvalidResponse
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := launch.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if r.closed || state.closed || state.used || (state.plan.writable == nil) != (writable == nil) || (writable != nil && *state.plan.writable != *writable) {
		return zero, ErrContainerUncertain
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	report, err := InspectDaemon(ctx, r.socket)
	if err != nil {
		return zero, err
	}
	if !report.MetadataCompatible {
		return zero, ErrUnsupported
	}
	if err := r.checkCandidateDataProfile(ctx, state.snapshot.Identity, state.snapshot.Data, writable != nil); err != nil {
		return zero, err
	}
	if err := state.pins.validate(ctx, state.plan); err != nil {
		return zero, err
	}
	if writable != nil {
		request := CandidateLaunchRequest{Identity: state.snapshot.Identity, Data: state.snapshot.Data}
		if err := r.requireWritableCandidateExclusion(ctx, request, state); err != nil {
			return zero, err
		}
	}
	state.used = true
	client, closeClient := r.client()
	defer closeClient()
	native, err := createPinnedCandidateHTTP(ctx, client, state.plan, state.pins)
	if err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	observed, err := startPinnedCandidateHTTP(ctx, client, native, state.plan, state.pins)
	if err != nil || observed != "running" {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := r.checkCandidateDataProfile(ctx, state.snapshot.Identity, state.snapshot.Data, writable != nil); err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	return CandidateRunningObservation{snapshot: CandidateRunningSnapshot{Launch: state.snapshot, NativeID: native, ObservedMS: time.Now().UTC().UnixMilli()}, valid: true}, nil
}

// cleanupCandidate is only the owned disposable test/support primitive. Current
// lifecycle authority is still required before exposing any stop/remove route.
func (r *CandidateRuntime) cleanupCandidate(ctx context.Context, launch CandidateLaunch, observed CandidateRunningObservation) error {
	if r == nil || launch.state == nil || launch.state.owner != r {
		return ErrInvalidResponse
	}
	snapshot, err := observed.Snapshot()
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	launch.state.mu.Lock()
	defer launch.state.mu.Unlock()
	if r.closed || launch.state.closed || snapshot.Launch != launch.state.snapshot {
		return ErrContainerConflict
	}
	client, closeClient := r.client()
	defer closeClient()
	if _, err := stopCandidateContainerHTTP(ctx, client, snapshot.NativeID, launch.state.plan); err != nil {
		return err
	}
	return removeCandidateContainerHTTP(ctx, client, snapshot.NativeID, launch.state.plan)
}

// CheckConfiguration checks the borrowed provider and daemon availability only.
// It grants no execution readiness, namespace access, writer lease or launch.
func (r *CandidateRuntime) CheckConfiguration(ctx context.Context) error {
	if r == nil {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkConfigurationLocked(ctx)
}

// CheckSharedRootConfiguration requires the exact borrowed provider object and
// root pin used at runtime opening. Matching pathnames/UUID text alone cannot
// join independently owned providers into one managed endpoint.
func (r *CandidateRuntime) CheckSharedRootConfiguration(ctx context.Context, root *data.Root, pin data.RootIdentity) error {
	_, err := r.ObserveSharedConfiguration(ctx, root, pin)
	return err
}
func (r *CandidateRuntime) ObserveSharedConfiguration(ctx context.Context, root *data.Root, pin data.RootIdentity) (RuntimeConfiguration, error) {
	if r == nil || root == nil {
		return RuntimeConfiguration{}, ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root != root || r.rootIdentity != pin {
		return RuntimeConfiguration{}, ErrUnsafeSocket
	}
	if err := r.checkConfigurationLocked(ctx); err != nil {
		return RuntimeConfiguration{}, err
	}
	return RuntimeConfiguration{Platform: "linux/" + runtime.GOARCH, SandboxProfile: Profile, Root: r.rootIdentity}, nil
}
func (r *CandidateRuntime) checkConfigurationLocked(ctx context.Context) error {
	if r.closed || r.root == nil || r.launches == nil {
		return ErrUnsupported
	}
	actual, err := r.root.Identity(ctx)
	if err != nil {
		return err
	}
	if actual != r.rootIdentity {
		return ErrUnsafeSocket
	}
	report, err := InspectDaemon(ctx, r.socket)
	if err != nil {
		return err
	}
	if !report.MetadataCompatible {
		return ErrUnsupported
	}
	return ctx.Err()
}
