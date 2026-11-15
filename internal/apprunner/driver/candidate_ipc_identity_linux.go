//go:build linux

package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Mount overlap is decided by identity as well as by name. A name comparison cannot see a bind mount: a local
// volume created with type=none,o=bind is listed with a /var/lib/docker/volumes/<id>/_data source, and a bind
// mount of a directory under another path is a different name for the same directory. Both are the same
// device and inode as the directory they mount.

// candidateIPCNode is the identity of a filesystem object: st_dev and st_ino.
type candidateIPCNode struct{ dev, ino uint64 }

// candidateIPCStatter returns the identity of a path, following symlinks. A variable so a test can stand in for
// a bind mount where it has no privilege to make one.
var candidateIPCStatter = func(path string) (candidateIPCNode, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return candidateIPCNode{}, err
	}
	return candidateIPCNode{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil
}

// candidateIPCNamespaceDirLimit bounds how many directories below a namespace are enumerated. A namespace with
// more is refused as unavailable rather than guessed at.
var candidateIPCNamespaceDirLimit = 4096

// candidateIPCResolution is one source's answer as one inventory pass saw it.
type candidateIPCResolution struct {
	// canonical is the symlink-free path the source names. For an absent source it is the path the source
	// would have once Docker creates it: the resolved deepest existing ancestor plus the missing remainder.
	canonical string
	// missing marks a source that does not exist yet.
	missing bool
	// node is the identity of the source, or of its deepest existing ancestor when it is missing.
	node candidateIPCNode
}

// candidateIPCNamespace is the identity picture of one namespace directory.
type candidateIPCNamespace struct {
	// ancestors holds the namespace's strict ancestors up to the filesystem root.
	ancestors map[candidateIPCNode]struct{}
	// inside holds the namespace itself and every directory below it.
	inside map[candidateIPCNode]struct{}
}

// candidateIPCPass is one inventory pass. The filesystem is asked about a source once per pass and about a
// namespace once per pass. Nothing outlives the pass, so a symlink retargeted or a mount made between two
// passes is seen by the second.
type candidateIPCPass struct {
	sources    map[string]candidateIPCResolution
	namespaces map[string]*candidateIPCNamespace
}

func newCandidateIPCPass() *candidateIPCPass {
	return &candidateIPCPass{sources: map[string]candidateIPCResolution{}, namespaces: map[string]*candidateIPCNamespace{}}
}

// referenced reports whether a container mount source overlaps the namespace directory path. A source
// overlaps when it is the namespace, an ancestor of it, or a descendant of it, by any of:
//
//   - name: the canonical (symlink-free) source equals the namespace, is a prefix directory of it, or lies
//     under it;
//   - identity: the source's st_dev/st_ino equals that of the namespace, of one of its ancestors up to the
//     filesystem root, or of a directory below it, which is how a bind mount of any of them is seen;
//   - creation: a source that does not exist is judged by the deepest existing ancestor it would be created
//     under (Docker creates a missing -v source when the container starts), so an absent source that would
//     land in the namespace is a reference. An absent source that lands elsewhere, including a not yet created
//     sibling namespace, is not.
//
// A source or namespace that cannot be examined for a reason other than absence is a refusal.
func (p *candidateIPCPass) referenced(ctx context.Context, path, source string) (bool, error) {
	if source == "" {
		return false, nil
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(source) {
		return false, ErrInvalidResponse
	}
	resolved, ok := p.sources[source]
	if !ok {
		var err error
		if resolved, err = resolveCandidateIPCIdentity(ctx, source); err != nil {
			return false, err
		}
		p.sources[source] = resolved
	}
	canonical := resolved.canonical
	if canonical == path || canonical == "/" || strings.HasPrefix(path, canonical+"/") || strings.HasPrefix(canonical, path+"/") {
		return true, nil
	}
	namespace, ok := p.namespaces[path]
	if !ok {
		var err error
		if namespace, err = buildCandidateIPCNamespace(ctx, path); err != nil {
			return false, err
		}
		p.namespaces[path] = namespace
	}
	if _, in := namespace.inside[resolved.node]; in {
		return true, nil
	}
	if resolved.missing {
		// An absent source cannot be an ancestor of an existing namespace; only landing inside counts.
		return false, nil
	}
	_, above := namespace.ancestors[resolved.node]
	return above, nil
}

// resolveCandidateIPCIdentity canonicalises a source and takes its identity. Every lookup runs under the
// process-wide slot, so a source on a stuck volume cannot pile up blocked threads.
func resolveCandidateIPCIdentity(ctx context.Context, source string) (candidateIPCResolution, error) {
	canonical, err := resolveCandidateIPCSource(ctx, source, candidateIPCResolver)
	switch {
	case err == nil:
		node, err := candidateIPCStatNode(ctx, canonical)
		if err != nil {
			return candidateIPCResolution{}, err
		}
		return candidateIPCResolution{canonical: canonical, node: node}, nil
	case errors.Is(err, os.ErrNotExist):
		return resolveAbsentCandidateIPCSource(ctx, source)
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return candidateIPCResolution{}, err
	default:
		return candidateIPCResolution{}, ErrUnavailable
	}
}

func candidateIPCStatNode(ctx context.Context, path string) (candidateIPCNode, error) {
	node, err := candidateIPCSlotted(ctx, func() (candidateIPCNode, error) { return candidateIPCStatter(path) })
	switch {
	case err == nil:
		return node, nil
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return candidateIPCNode{}, err
	default:
		return candidateIPCNode{}, ErrUnavailable
	}
}

// resolveAbsentCandidateIPCSource finds where a source that does not exist would be created: it climbs to the
// deepest ancestor that resolves, and keeps the missing remainder. A dangling symlink is taken at its own
// name: Docker cannot create the directory behind one and fails the start, so nothing is ever mounted from it.
func resolveAbsentCandidateIPCSource(ctx context.Context, source string) (candidateIPCResolution, error) {
	current, rest := filepath.Clean(source), ""
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return candidateIPCResolution{}, ErrUnavailable
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
		// Ancestors are climbed with the real resolver, so the hook above still counts one resolution per source.
		canonical, err := resolveCandidateIPCSource(ctx, current, filepath.EvalSymlinks)
		switch {
		case err == nil:
			node, err := candidateIPCStatNode(ctx, canonical)
			if err != nil {
				return candidateIPCResolution{}, err
			}
			return candidateIPCResolution{canonical: filepath.Join(canonical, rest), missing: true, node: node}, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		case ctx.Err() != nil && errors.Is(err, ctx.Err()):
			return candidateIPCResolution{}, err
		default:
			return candidateIPCResolution{}, ErrUnavailable
		}
	}
}

// buildCandidateIPCNamespace collects the identities that decide an overlap: the namespace's ancestors, the
// namespace and the directories below it. Symlinks are not followed below the namespace. A namespace that does
// not exist contributes only its existing ancestors.
func buildCandidateIPCNamespace(ctx context.Context, path string) (*candidateIPCNamespace, error) {
	namespace := &candidateIPCNamespace{ancestors: map[candidateIPCNode]struct{}{}, inside: map[candidateIPCNode]struct{}{}}
	physical := filepath.Clean(path)
	if canonical, err := filepath.EvalSymlinks(physical); err == nil {
		physical = canonical
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnavailable
	}
	for _, start := range []string{filepath.Clean(path), physical} {
		for dir := filepath.Dir(start); ; dir = filepath.Dir(dir) {
			node, err := candidateIPCStatter(dir)
			switch {
			case err == nil:
				namespace.ancestors[node] = struct{}{}
			case !errors.Is(err, os.ErrNotExist):
				return nil, ErrUnavailable
			}
			if dir == "/" {
				break
			}
		}
	}
	self, err := candidateIPCStatter(physical)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return namespace, ctx.Err()
	case err != nil:
		return nil, ErrUnavailable
	}
	namespace.inside[self] = struct{}{}
	pending, walked := []string{physical}, 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if walked++; walked > candidateIPCNamespaceDirLimit {
			return nil, ErrUnavailable
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			child := filepath.Join(dir, entry.Name())
			var st unix.Stat_t
			if err := unix.Lstat(child, &st); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, ErrUnavailable
			}
			namespace.inside[candidateIPCNode{dev: uint64(st.Dev), ino: uint64(st.Ino)}] = struct{}{}
			pending = append(pending, child)
		}
	}
	return namespace, ctx.Err()
}
