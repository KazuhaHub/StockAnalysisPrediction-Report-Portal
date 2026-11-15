//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Mount overlap is decided by where a path really lives as well as by its name. A name comparison cannot see a
// bind mount: a local volume created with type=none,o=bind is listed with a /var/lib/docker/volumes/<id>/_data
// source, and a bind mount of a directory under another path is a different name for the same directory.
//
// Both a mount source and the namespace are resolved to a place: the device (major:minor) of the filesystem
// they are on and their path within that filesystem. The place comes from /proc/self/mountinfo, where field 3 is
// the device and field 4 is the mount's root within the filesystem, so the cost depends on the mount table and
// never on the data under the namespace. Two paths overlap when they are on the same device and one place's
// path is a prefix of the other's: the same directory, an ancestor, or a descendant.
//
// This assumes the runner and dockerd share the host mount namespace and that the runner is not chrooted; the
// daemon's namespace is compared with the runner's when the daemon connection is made (see daemonPeer), and a
// chrooted runner is refused here.

// candidateIPCMaxLinkHops is how many symbolic links one path may traverse, as the kernel allows.
const candidateIPCMaxLinkHops = 40

// candidateIPCMountInfoPath names the mount table. A variable so a test can supply one.
var candidateIPCMountInfoPath = "/proc/self/mountinfo"

// candidateIPCMountInfoReader reads the mount table. A variable so a test can stall it.
var candidateIPCMountInfoReader = func() ([]byte, error) {
	if root, err := os.Readlink("/proc/self/root"); err != nil || root != "/" {
		return nil, fmt.Errorf("%w: the runner's root is %q (%v), not the host root, so its mount table is not the daemon's", ErrUnavailable, root, err)
	}
	return os.ReadFile(candidateIPCMountInfoPath)
}

// candidateIPCNamespaceResolver canonicalises a namespace path. A variable so a test can stall it.
var candidateIPCNamespaceResolver = candidateIPCCanonicalize

// candidateIPCPlace is a path's device and its path within that device's filesystem.
type candidateIPCPlace struct{ dev, path string }

type candidateIPCMount struct{ id, parent, dev, root, point string }

type candidateIPCMounts []candidateIPCMount

// candidateIPCPass is one inventory pass. The mount table, each source and each namespace are asked about once
// per pass and nothing outlives it, so a symlink retargeted or a mount made between two passes is seen by the
// second.
type candidateIPCPass struct {
	mounts     candidateIPCMounts
	sources    map[string]candidateIPCResolution
	namespaces map[string]candidateIPCNamespace
	volumes    map[string]candidateIPCVolumeAnswer
}

// candidateIPCVolumeAnswer is what one volume inspect said in this pass.
type candidateIPCVolumeAnswer struct {
	device string
	err    error
}

// candidateIPCResolution is one source's canonical path (where it lands, for an absent one) and its place.
type candidateIPCResolution struct {
	canonical string
	place     candidateIPCPlace
}

type candidateIPCNamespace struct {
	canonical string
	place     candidateIPCPlace
}

func newCandidateIPCPass() *candidateIPCPass {
	return &candidateIPCPass{sources: map[string]candidateIPCResolution{}, namespaces: map[string]candidateIPCNamespace{}, volumes: map[string]candidateIPCVolumeAnswer{}}
}

// candidateIPCReferenced reports whether a mount source overlaps the namespace directory path. See
// candidateIPCPass.referenced for the exact semantics.
func candidateIPCReferenced(ctx context.Context, path, source string) (bool, error) {
	return newCandidateIPCPass().referenced(ctx, path, source)
}

// referenced reports whether a container mount source overlaps the namespace directory path. A source
// overlaps when it is the namespace, an ancestor of it, or a descendant of it, by either of:
//
//   - name: the canonical source equals the namespace, is a prefix directory of it, is "/", or lies under it;
//   - place: the source and the namespace are on the same device and one's path within the filesystem is a
//     prefix of the other's. That is how a bind mount of the namespace, of the data root or of a nested
//     directory is seen, including a bind-backed local volume, and it also reaches a namespace that does not
//     exist yet.
//
// A source is canonicalised by following symbolic links one hop at a time (at most 40, as the kernel does),
// including a dangling link and a link partway along a path that does not exist. A path that does not exist is
// taken to land where its existing ancestor resolves plus the missing remainder, because Docker creates a missing
// bind source when the container starts (Binds, or -v; HostConfig.Mounts refuses one, but both are treated alike,
// which fails closed). A link cycle or too many hops, a mount table that cannot be read, and a path no mount
// entry covers are refusals. Every filesystem lookup, on the source side and on the namespace side, runs under
// the one-in-flight slot and is abandoned when the context ends.
func (p *candidateIPCPass) referenced(ctx context.Context, path, source string) (bool, error) {
	if source == "" {
		return false, nil
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(source) {
		return false, ErrInvalidResponse
	}
	resolved, ok := p.sources[source]
	if !ok {
		canonical, err := resolveCandidateIPCSource(ctx, source, candidateIPCResolver)
		if err != nil {
			return false, candidateIPCLookupError(ctx, err)
		}
		resolved = candidateIPCResolution{canonical: canonical}
		p.sources[source] = resolved
	}
	namespace, ok := p.namespaces[path]
	if !ok {
		canonical, err := resolveCandidateIPCSource(ctx, path, candidateIPCNamespaceResolver)
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			canonical = filepath.Clean(path)
		default:
			return false, candidateIPCLookupError(ctx, err)
		}
		namespace = candidateIPCNamespace{canonical: canonical}
		p.namespaces[path] = namespace
	}
	for _, ns := range []string{path, namespace.canonical} {
		if candidateIPCNameOverlap(resolved.canonical, ns) {
			return true, nil
		}
	}
	if p.mounts == nil {
		mounts, err := loadCandidateIPCMounts(ctx)
		if err != nil {
			return false, err
		}
		p.mounts = mounts
	}
	if resolved.place == (candidateIPCPlace{}) {
		place, err := p.mounts.place(resolved.canonical)
		if err != nil {
			return false, err
		}
		resolved.place = place
		p.sources[source] = resolved
	}
	if namespace.place == (candidateIPCPlace{}) {
		place, err := p.mounts.place(namespace.canonical)
		if err != nil {
			return false, err
		}
		namespace.place = place
		p.namespaces[path] = namespace
	}
	if candidateIPCPlacesOverlap(resolved.place, namespace.place) {
		return true, nil
	}
	// A bind is recursive unless it says otherwise, and nothing the list shows says otherwise, so the container
	// also sees every mount below the source. Each is a tree of its own: its device and its root within it.
	for _, mount := range p.mounts {
		if mount.point != resolved.canonical && candidateIPCPathWithin(resolved.canonical, mount.point) &&
			candidateIPCPlacesOverlap(candidateIPCPlace{dev: mount.dev, path: mount.root}, namespace.place) {
			return true, nil
		}
	}
	return false, nil
}

func candidateIPCPlacesOverlap(a, b candidateIPCPlace) bool {
	return a.dev == b.dev && (candidateIPCPathWithin(a.path, b.path) || candidateIPCPathWithin(b.path, a.path))
}

func candidateIPCNameOverlap(canonical, path string) bool {
	return canonical == path || canonical == "/" || strings.HasPrefix(path, canonical+"/") || strings.HasPrefix(canonical, path+"/")
}

// candidateIPCPathWithin reports whether inner is dir or lies under it.
func candidateIPCPathWithin(dir, inner string) bool {
	return dir == "/" || dir == inner || strings.HasPrefix(inner, dir+"/")
}

// candidateIPCLookupError keeps a context ending visible and turns every other failure into a refusal.
func candidateIPCLookupError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err
	}
	return ErrUnavailable
}

// candidateIPCCanonicalize resolves a path the way the kernel does, one component and one link at a time,
// except that a component that does not exist ends the walk instead of failing it: the rest is appended as
// written (a "." is dropped, a ".." removes the previous component). The result is where the path lands once
// something creates it. A cycle or more than candidateIPCMaxLinkHops links returns ELOOP, and a component that
// is not a directory returns ENOTDIR.
func candidateIPCCanonicalize(path string) (string, error) {
	pending := candidateIPCComponents(path)
	var resolved []string
	hops := 0
	missing := 0 // components appended after the first that does not exist, not yet undone by a ".."
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			if missing > 0 {
				missing-- // back at a directory that exists: what follows is real again
			}
			continue
		}
		if missing > 0 {
			resolved = append(resolved, name)
			missing++
			continue
		}
		target, err := os.Readlink("/" + strings.Join(append(append([]string{}, resolved...), name), "/"))
		switch {
		case err == nil:
			if hops++; hops > candidateIPCMaxLinkHops {
				return "", syscall.ELOOP
			}
			if strings.HasPrefix(target, "/") {
				resolved = nil
			}
			pending = append(candidateIPCComponents(target), pending...)
		case errors.Is(err, syscall.EINVAL): // exists, not a link
			resolved = append(resolved, name)
		case errors.Is(err, os.ErrNotExist):
			missing = 1
			resolved = append(resolved, name)
		default:
			return "", err
		}
	}
	return "/" + strings.Join(resolved, "/"), nil
}

func candidateIPCComponents(path string) []string {
	return strings.Split(path, "/")
}

func loadCandidateIPCMounts(ctx context.Context) (candidateIPCMounts, error) {
	raw, err := candidateIPCSlotted(ctx, candidateIPCMountInfoReader)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: mount table read: %v", candidateIPCLookupError(ctx, err), err)
	}
	mounts, err := parseCandidateIPCMountInfo(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: mount table: %v", ErrUnavailable, err)
	}
	return mounts, nil
}

// parseCandidateIPCMountInfo reads the fields the overlap needs: the device (3), the mount's root within its
// filesystem (4) and the mount point (5). A mount whose root is not a path (an nsfs namespace file) is skipped. Spaces, tabs, newlines and backslashes are octal escapes in a path.
func parseCandidateIPCMountInfo(raw []byte) (candidateIPCMounts, error) {
	var mounts candidateIPCMounts
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 7 || !strings.Contains(fields[2], ":") {
			return nil, ErrInvalidResponse
		}
		root, point := unescapeCandidateIPCMountField(fields[3]), unescapeCandidateIPCMountField(fields[4])
		if !strings.HasPrefix(point, "/") {
			return nil, ErrInvalidResponse
		}
		if !strings.HasPrefix(root, "/") {
			// A namespace file bind-mounted for a container's network namespace has the root "net:[4026532761]".
			// It holds no directory tree, so no path can be placed on it.
			continue
		}
		mounts = append(mounts, candidateIPCMount{id: fields[0], parent: fields[1], dev: fields[2], root: filepath.Clean(root), point: filepath.Clean(point)})
	}
	if len(mounts) == 0 {
		return nil, ErrInvalidResponse
	}
	return mounts, nil
}

func unescapeCandidateIPCMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// place maps a canonical absolute path to its device and path within the filesystem: the covering mount is the
// one with the longest mount point, and the topmost (latest) of several at the same point. No covering mount is
// an error: the path cannot be placed.
func (m candidateIPCMounts) place(path string) (candidateIPCPlace, error) {
	best := -1
	for i, mount := range m {
		if !candidateIPCPathWithin(mount.point, path) {
			continue
		}
		switch {
		case best < 0 || len(mount.point) > len(m[best].point):
			best = i
		case len(mount.point) == len(m[best].point) && m.stackedOver(mount, m[best]):
			best = i
		}
	}
	if best < 0 {
		return candidateIPCPlace{}, fmt.Errorf("%w: no mount covers %q", ErrUnavailable, path)
	}
	mount := m[best]
	rel := strings.TrimPrefix(path, mount.point)
	return candidateIPCPlace{dev: mount.dev, path: filepath.Join(mount.root, rel)}, nil
}

// stackedOver reports whether a is mounted on top of b at the same mount point, by the mount tree: a's parent
// chain reaches b. Mounts that are unrelated by the tree keep the order of the file, the latest winning.
func (m candidateIPCMounts) stackedOver(a, b candidateIPCMount) bool {
	byID := make(map[string]candidateIPCMount, len(m))
	for _, mount := range m {
		byID[mount.id] = mount
	}
	for cur, hops := a, 0; hops < len(m); hops++ {
		if cur.parent == b.id {
			return true
		}
		next, ok := byID[cur.parent]
		if !ok || next.point != a.point {
			break
		}
		cur = next
	}
	for cur, hops := b, 0; hops < len(m); hops++ {
		if cur.parent == a.id {
			return false
		}
		next, ok := byID[cur.parent]
		if !ok || next.point != b.point {
			break
		}
		cur = next
	}
	return true // unrelated: the later entry wins
}
