//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const candidateIPCBytes int64 = 16 << 20
const candidateIPCInodes uint64 = 1024
const candidateIPCMarker = "trusted.portal.ipc"

func candidateIPCRecord(identity CandidateIdentity, kind string) []byte {
	raw, _ := json.Marshal(struct {
		Format   string            `json:"format"`
		Profile  string            `json:"profile"`
		Kind     string            `json:"kind"`
		Identity CandidateIdentity `json:"identity"`
	}{"portal.runner.ipc-storage/1-dev.1", candidateIPCProfile, kind, identity})
	return raw
}
func candidateIPCMarkerMatches(fd int, identity CandidateIdentity, kind string) bool {
	value := make([]byte, 2048)
	n, err := unix.Fgetxattr(fd, candidateIPCMarker, value)
	return err == nil && bytes.Equal(value[:n], candidateIPCRecord(identity, kind))
}

// Observe a pinned directory, never an app-supplied path. Trusted xattrs cannot
// be changed by the app owning the tmpfs root. Kernel data-block and inode limits bound file content and entry counts,
// independently of data writer grants. Docker memory limits remain separately required.
func validateCandidateHTTPStorage(fd int, identity CandidateIdentity, strictMode bool) error {
	if !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsafeSocket
	}
	readable, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnsafeSocket
	}
	defer unix.Close(readable)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(readable, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 65532 || st.Gid != 65532 || (strictMode && st.Mode&07777 != 0700) || unix.Fstatfs(readable, &fs) != nil {
		return ErrUnsafeSocket
	}
	const required = unix.ST_NODEV | unix.ST_NOSUID | unix.ST_NOEXEC
	if fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || candidateIPCBytes%fs.Bsize != 0 || fs.Blocks != uint64(candidateIPCBytes/fs.Bsize) || fs.Files != candidateIPCInodes || fs.Flags&required != required || fs.Flags&unix.ST_RDONLY != 0 || !candidateIPCMarkerMatches(readable, identity, "mount") {
		return ErrUnsafeSocket
	}
	return nil
}

// Called after durable candidate claiming. Only a fresh directory is mounted;
// an existing one must already have the exact bounded mount and trusted identity.
// A partial or foreign directory is never repaired under an uncertain effect.
func provisionCandidateHTTPStorage(ctx context.Context, parent int, identity CandidateIdentity) error {
	if os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := unix.Mkdirat(parent, "http", 0700)
	if errors.Is(err, unix.EEXIST) {
		fd, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnsafeSocket
		}
		defer unix.Close(fd)
		return validateCandidateHTTPStorage(fd, identity, true)
	}
	if err != nil {
		return err
	}
	base, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	defer unix.Close(base)
	// Normalize only this freshly created underlay, never an existing directory.
	if unix.Fchown(base, 0, 0) != nil || unix.Fchmod(base, 0700) != nil {
		return ErrContainerUncertain
	}
	var st unix.Stat_t
	if unix.Fstat(base, &st) != nil || !safeMountDirectory(st, true, 0, 0, 0700) {
		return ErrUnsafeSocket
	}
	if err := unix.Fsetxattr(base, candidateIPCMarker, candidateIPCRecord(identity, "base"), unix.XATTR_CREATE); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if unix.Fsync(base) != nil || unix.Fsync(parent) != nil {
		return ErrContainerUncertain
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	// Kernel-owned proc FD aliases keep the effect attached to the protected,
	// already opened directory instead of resolving mutable caller paths.
	target := fmt.Sprintf("/proc/self/fd/%d", base)
	if err := unix.Mount("portal-app-ipc", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=16777216,nr_inodes=1024,mode=0700,uid=65532,gid=65532"); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	mounted, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	defer unix.Close(mounted)
	if err := unix.Mount("", fmt.Sprintf("/proc/self/fd/%d", mounted), "", unix.MS_PRIVATE, ""); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := unix.Fsetxattr(mounted, candidateIPCMarker, candidateIPCRecord(identity, "mount"), unix.XATTR_CREATE); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := validateCandidateHTTPStorage(mounted, identity, true); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return ctx.Err()
}

// Caller holds lifecycle exclusion, closes live pins and proves no Docker mount
// references remain. Never lazy-unmount or recursively remove unknown storage.
func releaseCandidateHTTPStorage(ctx context.Context, parent int, path string, identity CandidateIdentity) error {
	if os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return ErrUnsafeSocket
	}
	if !candidateIPCMarkerMatches(fd, identity, "base") {
		err = validateCandidateHTTPStorage(fd, identity, false)
		unix.Close(fd)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The complete root-owned ancestor chain is pinned by the caller. Close the
		// final mount FD first so ordinary unmount detects genuine outstanding users.
		if err := unix.Unmount(path, 0); err != nil {
			return errors.Join(ErrContainerUncertain, err)
		}
		fd, err = unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return errors.Join(ErrContainerUncertain, err)
		}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !safeMountDirectory(st, true, 0, 0, 0700) || !candidateIPCMarkerMatches(fd, identity, "base") {
		return ErrUnsafeSocket
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := unix.Unlinkat(parent, "http", unix.AT_REMOVEDIR); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := unix.Fsync(parent); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return nil
}

// Explicit quiescent cleanup, not a lifecycle authorization. Remove the owned
// container under its own journal authority and close launch pins first. Runtime
// Close alone deliberately keeps live app communication storage mounted.
func (r *CandidateRuntime) ReleaseCandidateHTTPStorage(ctx context.Context, identity CandidateIdentity) error {
	if r == nil || os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	for launch := range r.launches {
		if launch.snapshot.Identity.InstallationID == identity.InstallationID && launch.snapshot.Identity.Generation == identity.Generation {
			return ErrContainerConflict
		}
	}
	path := filepath.Join(r.roots.Sockets, identity.InstallationID, identity.Generation, "http")
	client, closeClient := r.client()
	defer closeClient()
	if err := requireNoContainerMountReferences(ctx, client, path); err != nil {
		return err
	}
	pins, err := openMountDirectory(filepath.Dir(path), 0, 0, 0700)
	if err != nil {
		return err
	}
	defer func() {
		for _, p := range pins {
			p.file.Close()
		}
	}()
	parent, err := unix.Openat(int(pins[len(pins)-1].file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return releaseCandidateHTTPStorage(ctx, parent, path, identity)
}

type candidateIPCContainer struct {
	ID     string          `json:"Id"`
	Mounts json.RawMessage `json:"Mounts"`
	// plan is the parsed mount plan of a remembered entry, whose Mounts bytes are not kept.
	plan *candidateIPCMountPlanResult
}

// The general daemon decoder deliberately accepts object projections only.
// Keep that contract intact and validate each object of this bounded array.
func readCandidateIPCInventory(ctx context.Context, client *http.Client) ([]candidateIPCContainer, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/json?all=1", nil)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: candidate IPC inventory request: %v",
			ErrUnavailable, err)
	}
	defer response.Body.Close()
	if err := requireDaemonSharesMountNamespace(client); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"%w: candidate IPC inventory status %d",
			ErrUnavailable, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	defer clear(raw)
	if err != nil || ctx.Err() != nil {
		return nil, fmt.Errorf(
			"%w: candidate IPC inventory read: %v context=%v",
			ErrUnavailable, err, ctx.Err())
	}
	if len(raw) > maxResponseBytes {
		return nil, ErrInvalidResponse
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, fmt.Errorf("%w: candidate IPC inventory is not a JSON array", ErrInvalidResponse)
	}
	defer func() {
		for _, entry := range entries {
			clear(entry)
		}
	}()
	result := make([]candidateIPCContainer, 0, len(entries))
	for _, entry := range entries {
		// An entry that passed the checks below passes them again byte for byte; the daemon's volatile
		// fields (a status like "Up 3 minutes") make it a different entry, which is checked from scratch.
		key := memoKeyOf(entry)
		if remembered, ok := inventoryEntryMemo.get(key); ok {
			// The same bytes were accepted before, so Mounts is present. Callers read it, so it is taken
			// from this pass's own bytes (a copy the result owns), not kept in the memory.
			var narrow struct {
				Mounts json.RawMessage `json:"Mounts"`
			}
			if json.Unmarshal(entry, &narrow) != nil || len(narrow.Mounts) == 0 {
				return nil, fmt.Errorf("%w: candidate IPC inventory entry %s: Mounts key missing", ErrInvalidResponse, inventoryEntryLabel(entry))
			}
			result = append(result, candidateIPCContainer{ID: remembered.id, Mounts: narrow.Mounts, plan: remembered.plan.clonePtr()})
			continue
		}
		var container candidateIPCContainer
		if !validProjection(entry, &container) || json.Unmarshal(entry, &container) != nil {
			return nil, fmt.Errorf("%w: candidate IPC inventory entry %s: projection rejected", ErrInvalidResponse, inventoryEntryLabel(entry))
		}
		if !containerSHA.MatchString(container.ID) {
			return nil, fmt.Errorf("%w: candidate IPC inventory entry %s: malformed Id", ErrInvalidResponse, inventoryEntryLabel(entry))
		}
		plan, err := candidateIPCMountPlan(container.Mounts)
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", shortContainerID(container.ID), err)
		}
		inventoryEntryMemo.put(key, candidateIPCInventoryEntry{id: container.ID, plan: plan.clone()})
		result = append(result, container)
	}
	return result, nil
}

// inventoryEntryMemo and mountPlanMemo remember accepted decodings of daemon bytes only. See decisionMemo.
var inventoryEntryMemo = newDecisionMemo[candidateIPCInventoryEntry](1024)

// candidateIPCInventoryEntry is what is kept of an accepted entry: its id and its parsed mount plan, never
// the daemon's bytes, which each pass clears.
type candidateIPCInventoryEntry struct {
	id   string
	plan candidateIPCMountPlanResult
}

var mountPlanMemo = newDecisionMemo[candidateIPCMountPlanResult](1024)

type candidateIPCMountPlanResult struct {
	sources []string
	pending []candidateIPCPendingVolume
	// volumes are the names of the local-driver volumes the container mounts. Each is inspected, because a
	// local volume created with type=none,o=bind is a bind mount of its device whatever path the list shows.
	volumes []string
}

func (r candidateIPCMountPlanResult) clonePtr() *candidateIPCMountPlanResult {
	c := r.clone()
	return &c
}

func (r candidateIPCMountPlanResult) clone() candidateIPCMountPlanResult {
	return candidateIPCMountPlanResult{sources: slices.Clone(r.sources), pending: slices.Clone(r.pending), volumes: slices.Clone(r.volumes)}
}

// shortContainerID names a container in an error without the full identifier.
func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// inventoryEntryLabel names an entry whose Id cannot be trusted: the first
// twelve characters of a string Id, or a fixed marker. It never echoes more.
func inventoryEntryLabel(entry json.RawMessage) string {
	var probe struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(entry, &probe) != nil || probe.ID == "" {
		return "(no Id)"
	}
	return fmt.Sprintf("%q", shortContainerID(probe.ID))
}

// candidateIPCPendingVolume is a volume mount the container list reports
// without a host path and that is not a local-driver volume: only the inspect
// endpoint can say where it lives.
type candidateIPCPendingVolume struct {
	index       int
	destination string
}

// candidateIPCMountSources is the closed, list-only projection: any mount whose
// host path cannot be read from the list alone is refused.
func candidateIPCMountSources(raw json.RawMessage) ([]string, error) {
	plan, err := candidateIPCMountPlan(raw)
	if err != nil {
		return nil, err
	}
	if len(plan.pending) != 0 {
		return nil, fmt.Errorf("%w: mount %d: volume without a source needs inspection", ErrInvalidResponse, plan.pending[0].index)
	}
	return plan.sources, nil
}

func candidateIPCMountPlan(raw json.RawMessage) (candidateIPCMountPlanResult, error) {
	if len(raw) == 0 {
		return candidateIPCMountPlanResult{}, fmt.Errorf("%w: Mounts key missing", ErrInvalidResponse)
	}
	// The plan is a function of the mount bytes alone. Callers append to the result, so they get copies.
	key := memoKeyOf(raw)
	if plan, ok := mountPlanMemo.get(key); ok {
		return plan.clone(), nil
	}
	plan, err := parseCandidateIPCMountPlan(raw)
	if err != nil {
		return candidateIPCMountPlanResult{}, err
	}
	mountPlanMemo.put(key, plan.clone())
	return plan, nil
}

func parseCandidateIPCMountPlan(raw json.RawMessage) (candidateIPCMountPlanResult, error) {
	var plan candidateIPCMountPlanResult
	fail := func(format string, args ...any) (candidateIPCMountPlanResult, error) {
		return candidateIPCMountPlanResult{}, fmt.Errorf(format, args...)
	}
	if len(raw) == 0 {
		return fail("%w: Mounts key missing", ErrInvalidResponse)
	}
	var mounts []json.RawMessage
	if json.Unmarshal(raw, &mounts) != nil {
		return fail("%w: Mounts is not an array", ErrInvalidResponse)
	}
	plan.sources = make([]string, 0, len(mounts))
	for i, entry := range mounts {
		var m struct {
			Type        string  `json:"Type"`
			Name        string  `json:"Name"`
			Source      *string `json:"Source"`
			Driver      *string `json:"Driver"`
			Destination string  `json:"Destination"`
		}
		if !validProjection(entry, &m) || json.Unmarshal(entry, &m) != nil {
			return fail("%w: mount %d: projection rejected", ErrInvalidResponse, i)
		}
		switch m.Type {
		case "volume":
			local := m.Driver != nil && *m.Driver == "local"
			if local {
				// A local volume created with type=none,o=bind is a bind mount of its device, whatever the list
				// shows as its source and whether or not a container is running. It is inspected by name.
				if !candidateIPCVolumeName.MatchString(m.Name) {
					return fail("%w: mount %d: local volume has the name %q", ErrInvalidResponse, i, boundedString(m.Name))
				}
				plan.volumes = append(plan.volumes, m.Name)
			}
			// The list endpoint reports a daemon-managed volume with an empty
			// Source. A local-driver volume lives under the Docker root, so the
			// empty Source names no path that could overlap the data root. Any
			// other driver (or none) is resolved through inspect instead.
			if m.Source != nil && *m.Source == "" {
				if local {
					continue
				}
				if !filepath.IsAbs(m.Destination) {
					return fail("%w: mount %d: volume without a source has destination %q, not an absolute path", ErrInvalidResponse, i, boundedString(m.Destination))
				}
				plan.pending = append(plan.pending, candidateIPCPendingVolume{index: i, destination: m.Destination})
				continue
			}
			fallthrough
		case "bind":
			if m.Source == nil || !filepath.IsAbs(*m.Source) {
				return fail("%w: mount %d: type %q source %q is not an absolute path", ErrInvalidResponse, i, m.Type, boundedSource(m.Source))
			}
			plan.sources = append(plan.sources, *m.Source)
		case "tmpfs":
			if m.Source != nil && *m.Source != "" {
				return fail("%w: mount %d: tmpfs with source %q", ErrInvalidResponse, i, boundedSource(m.Source))
			}
		default:
			return fail("%w: mount %d: unsupported type %q", ErrInvalidResponse, i, boundedString(m.Type))
		}
	}
	return plan, nil
}

// candidateIPCVolumeName is the name syntax the allow-list admits for a volume read.
var candidateIPCVolumeName = regexp.MustCompile(`^` + dockerVolumeName + `$`)

// candidateIPCVolumeOptionsRefusal refuses a local volume that is not a bind but whose options name a host
// path or a block device: an overlay's lowerdir, upperdir or workdir, a device other than the literal tmpfs
// one, or any value that looks like a path. The daemon mounts it on the host when a container uses it, so it
// can reach the namespace in ways the list never shows. A volume with no options, or with options that name
// no path (a size, a uid), is judged by the path the list shows, as before. nil means none of that.
func candidateIPCVolumeOptionsRefusal(name string, options map[string]string) error {
	refuse := func(key string) error {
		return fmt.Errorf("%w: local volume %q names a host path or device in its option %q", ErrContainerConflict, boundedString(name), boundedString(key))
	}
	for key, value := range options {
		switch {
		case key == "lowerdir" || key == "upperdir" || key == "workdir":
			return refuse(key)
		case key == "device" && value != "" && value != "tmpfs":
			return refuse(key)
		case strings.Contains(value, "/"):
			return refuse(key)
		}
	}
	return nil
}

// volumeDevice is candidateIPCBindVolumeDevice answered once per pass for a volume name.
func (p *candidateIPCPass) volumeDevice(ctx context.Context, client *http.Client, name string) (string, error) {
	if answer, ok := p.volumes[name]; ok {
		return answer.device, answer.err
	}
	device, err := candidateIPCBindVolumeDevice(ctx, client, name)
	if ctx.Err() == nil { // an answer cut short by the deadline is not remembered
		p.volumes[name] = candidateIPCVolumeAnswer{device, err}
	}
	return device, err
}

// candidateIPCBindVolumeDevice inspects one local volume and returns the host path it binds, or "" when it is
// not a bind-backed volume. A volume whose options are not a flat object of strings, that names another
// driver, or that binds without an absolute device or with a filesystem type other than none, is refused.
func candidateIPCBindVolumeDevice(ctx context.Context, client *http.Client, name string) (string, error) {
	var inspected struct {
		Driver  string          `json:"Driver"`
		Options json.RawMessage `json:"Options"`
	}
	if err := readJSON(ctx, client, "/v"+APIVersion+"/volumes/"+name, &inspected); err != nil {
		return "", fmt.Errorf("inspect volume %q: %w", boundedString(name), err)
	}
	if inspected.Driver != "local" {
		return "", fmt.Errorf("%w: volume %q is listed as local but inspects as driver %q", ErrInvalidResponse, boundedString(name), boundedString(inspected.Driver))
	}
	if len(inspected.Options) == 0 || string(inspected.Options) == "null" {
		return "", nil
	}
	var options map[string]string
	var probe struct{}
	if !validProjection(inspected.Options, &probe) || json.Unmarshal(inspected.Options, &options) != nil {
		return "", fmt.Errorf("%w: volume %q: options are not an object of strings", ErrInvalidResponse, boundedString(name))
	}
	bind := false
	for _, option := range strings.Split(options["o"], ",") {
		if option == "bind" || option == "rbind" {
			bind = true
		}
	}
	if !bind {
		return "", candidateIPCVolumeOptionsRefusal(name, options)
	}
	if fsType, ok := options["type"]; ok && fsType != "none" {
		return "", fmt.Errorf("%w: volume %q: a bind option with the filesystem type %q", ErrInvalidResponse, boundedString(name), boundedString(fsType))
	}
	device := options["device"]
	if !filepath.IsAbs(device) {
		return "", fmt.Errorf("%w: volume %q: bind device %q is not an absolute path", ErrInvalidResponse, boundedString(name), boundedString(device))
	}
	return device, nil
}

// candidateIPCContainerSources returns every host path a container mounts. A
// volume the list reports without a path (a non-local driver) is resolved with
// one inspect of that container; one that stays empty or relative is refused as an invalid response. A failed inspect (the container removed, a daemon error, a timeout) keeps the error type readJSON gives it: ErrUnavailable or the context error.
func candidateIPCContainerSources(ctx context.Context, client *http.Client, container candidateIPCContainer) ([]string, error) {
	return newCandidateIPCPass().containerSources(ctx, client, container)
}

// containerSources is candidateIPCContainerSources for a pass: a volume is inspected once per pass, however many
// containers mount it.
func (p *candidateIPCPass) containerSources(ctx context.Context, client *http.Client, container candidateIPCContainer) ([]string, error) {
	var plan candidateIPCMountPlanResult
	if container.plan != nil {
		plan = container.plan.clone()
	} else {
		var err error
		plan, err = candidateIPCMountPlan(container.Mounts)
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", shortContainerID(container.ID), err)
		}
	}
	sources, pending := plan.sources, plan.pending
	for _, name := range plan.volumes {
		device, err := p.volumeDevice(ctx, client, name)
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", shortContainerID(container.ID), err)
		}
		if device != "" {
			sources = append(sources, device)
		}
	}
	if len(pending) == 0 {
		return sources, nil
	}
	var inspected struct {
		Mounts json.RawMessage `json:"Mounts"`
	}
	if err := readJSON(ctx, client, "/v"+APIVersion+"/containers/"+container.ID+"/json", &inspected); err != nil {
		return nil, fmt.Errorf("container %s: inspect for a volume without a source: %w", shortContainerID(container.ID), err)
	}
	var mounts []json.RawMessage
	if json.Unmarshal(inspected.Mounts, &mounts) != nil {
		return nil, fmt.Errorf("%w: container %s: inspected Mounts is not an array", ErrInvalidResponse, shortContainerID(container.ID))
	}
	for _, want := range pending {
		resolved := ""
		matched := 0
		for _, entry := range mounts {
			var m struct {
				Type        string  `json:"Type"`
				Source      *string `json:"Source"`
				Destination string  `json:"Destination"`
			}
			if !validProjection(entry, &m) || json.Unmarshal(entry, &m) != nil {
				return nil, fmt.Errorf("%w: container %s: inspected mount projection rejected", ErrInvalidResponse, shortContainerID(container.ID))
			}
			if m.Type == "volume" && m.Destination == want.destination {
				matched++
				if m.Source != nil {
					resolved = *m.Source
				}
			}
		}
		if matched != 1 || !filepath.IsAbs(resolved) {
			return nil, fmt.Errorf("%w: container %s: mount %d: volume source %q unresolved after inspect", ErrInvalidResponse, shortContainerID(container.ID), want.index, boundedString(resolved))
		}
		sources = append(sources, resolved)
	}
	return sources, nil
}

func boundedSource(source *string) string {
	if source == nil {
		return "<absent>"
	}
	return boundedString(*source)
}

func boundedString(s string) string {
	if len(s) > 96 {
		return s[:96] + "..."
	}
	return s
}

// candidateIPCResolver canonicalises a mount source. A variable so a test can count and fail resolutions.
var candidateIPCResolver = candidateIPCCanonicalize

// A filesystem lookup may block on an unrelated host volume. Every lookup the
// overlap check makes, on the source side and the namespace side alike, runs
// here: at most one in flight process-wide, and the caller stops waiting at its
// deadline. A timed-out lookup retains the slot until it actually returns;
// retries cannot spawn an unbounded set of blocked native threads.
var candidateIPCResolverSlot = make(chan struct{}, 1)

func resolveCandidateIPCSource(ctx context.Context, source string, resolve func(string) (string, error)) (string, error) {
	return candidateIPCSlotted(ctx, func() (string, error) { return resolve(source) })
}

// candidateIPCSlotted runs one lookup under the process-wide slot, as described above.
func candidateIPCSlotted[T any](ctx context.Context, lookup func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	select {
	case candidateIPCResolverSlot <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-candidateIPCResolverSlot
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	ready := make(chan result, 1)
	go func() {
		value, err := lookup()
		<-candidateIPCResolverSlot
		ready <- result{value, err}
	}()
	select {
	case r := <-ready:
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
