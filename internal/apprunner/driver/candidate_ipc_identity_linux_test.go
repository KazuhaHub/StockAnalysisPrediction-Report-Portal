//go:build linux

package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// identityFixture lays out a data root with a namespace, a nested directory in it and an unrelated sibling.
type identityFixture struct {
	root, data, namespace, nested, sibling string
}

func newIdentityFixture(t *testing.T) identityFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := identityFixture{root: root, data: filepath.Join(root, "data")}
	f.namespace = filepath.Join(f.data, "ns-a")
	f.nested = filepath.Join(f.namespace, "deep", "er")
	f.sibling = filepath.Join(f.data, "ns-b")
	for _, dir := range []string{f.nested, f.sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func referencedFresh(t *testing.T, path, source string) (bool, error) {
	t.Helper()
	return candidateIPCReferenced(context.Background(), path, source)
}

// aliasSource makes a path the filesystem cannot reach through names resolve to a real directory, the way a
// bind-backed local volume reports /var/lib/docker/volumes/<id>/_data for a directory it mounts. It stands in
// for a bind mount where the test has no privilege; TestBindMountAliasIsAnOverlap uses a real one.
func aliasSource(t *testing.T, alias, real string) {
	t.Helper()
	oldResolve, oldStat := candidateIPCResolver, candidateIPCStatter
	candidateIPCResolver = func(p string) (string, error) {
		if p == alias {
			return alias, nil
		}
		return oldResolve(p)
	}
	candidateIPCStatter = func(p string) (candidateIPCNode, error) {
		if p == alias {
			return oldStat(real)
		}
		return oldStat(p)
	}
	t.Cleanup(func() { candidateIPCResolver, candidateIPCStatter = oldResolve, oldStat })
}

func TestIdentityOverlapSemantics(t *testing.T) {
	f := newIdentityFixture(t)
	const alias = "/var/lib/docker/volumes/vol/_data"
	cases := []struct {
		name string
		real string // the directory the alias really is
		want bool
	}{
		{"the namespace itself", f.namespace, true},
		{"an ancestor of the namespace (the data root)", f.data, true},
		{"a far ancestor", f.root, true},
		{"a descendant of the namespace", f.nested, true},
		{"a sibling namespace", f.sibling, false},
		{"a directory elsewhere", t.TempDir(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aliasSource(t, alias, c.real)
			got, err := referencedFresh(t, f.namespace, alias)
			if err != nil || got != c.want {
				t.Fatalf("overlap=%v err=%v, want %v", got, err, c.want)
			}
		})
	}
}

// A hard alias a name comparison cannot see: the same directory reached under a path that is not under the
// namespace, not a symlink, and not equal to it. The source's device and inode are the namespace's.
func TestIdentityOverlapSeesAnAliasThatPathsDoNot(t *testing.T) {
	f := newIdentityFixture(t)
	const alias = "/srv/unrelated/looks-harmless"
	aliasSource(t, alias, f.namespace)
	if strings.HasPrefix(alias, f.namespace) || strings.HasPrefix(f.namespace, alias) {
		t.Fatal("fixture: the alias must not overlap by name")
	}
	if got, err := referencedFresh(t, f.namespace, alias); err != nil || !got {
		t.Fatal("alias of the namespace by device and inode missed", got, err)
	}
	// The same alias against the sibling namespace is not an overlap.
	if got, err := referencedFresh(t, f.sibling, alias); err != nil || got {
		t.Fatal("alias reported against the wrong namespace", got, err)
	}
}

func TestAbsentSourceUnderTheNamespaceIsAReference(t *testing.T) {
	f := newIdentityFixture(t)
	for name, source := range map[string]string{
		"directly under the namespace": filepath.Join(f.namespace, "created-on-start"),
		"deep under the namespace":     filepath.Join(f.namespace, "a", "b", "c"),
		"under a nested directory":     filepath.Join(f.nested, "new"),
		"dotdot back into it":          filepath.Join(f.sibling, "..", "ns-a", "x"),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := referencedFresh(t, f.namespace, source); err != nil || !got {
				t.Fatal("an absent source Docker would create inside the namespace passed", got, err)
			}
		})
	}
	// Reached through a symlinked parent, the source would still be created inside the namespace.
	link := filepath.Join(f.root, "link")
	if err := os.Symlink(f.namespace, link); err != nil {
		t.Fatal(err)
	}
	if got, err := referencedFresh(t, f.namespace, filepath.Join(link, "x", "y")); err != nil || !got {
		t.Fatal("absent source behind a symlinked parent passed", got, err)
	}
}

func TestAbsentSourceElsewhereIsNotAReference(t *testing.T) {
	f := newIdentityFixture(t)
	for name, source := range map[string]string{
		"a future namespace under the data root": filepath.Join(f.data, "ns-future"),
		"under a sibling namespace":              filepath.Join(f.sibling, "new"),
		"beside the data root":                   filepath.Join(f.root, "other", "new"),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := referencedFresh(t, f.namespace, source); err != nil || got {
				t.Fatal("an absent source that lands elsewhere was treated as a reference", got, err)
			}
		})
	}
}

// A namespace that does not exist yet is reached by an absent source with the same name.
func TestAbsentSourceThatIsTheMissingNamespaceIsAReference(t *testing.T) {
	f := newIdentityFixture(t)
	future := filepath.Join(f.data, "ns-future")
	for _, source := range []string{future, filepath.Join(future, "x")} {
		if got, err := referencedFresh(t, future, source); err != nil || !got {
			t.Fatal("absent source that names the absent namespace passed", source, got, err)
		}
	}
}

// Docker cannot create the directory behind a dangling symlink, so the start fails and nothing is mounted.
func TestDanglingSymlinkSourceIsNotAReference(t *testing.T) {
	f := newIdentityFixture(t)
	dangling := filepath.Join(f.root, "dangling")
	if err := os.Symlink(filepath.Join(f.root, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	if got, err := referencedFresh(t, f.namespace, dangling); err != nil || got {
		t.Fatal("a dangling symlink source outside the namespace was treated as a reference", got, err)
	}
}

func TestIdentityStatFailureFailsClosed(t *testing.T) {
	f := newIdentityFixture(t)
	old := candidateIPCStatter
	candidateIPCStatter = func(string) (candidateIPCNode, error) { return candidateIPCNode{}, os.ErrPermission }
	t.Cleanup(func() { candidateIPCStatter = old })
	if got, err := referencedFresh(t, f.namespace, f.sibling); !errors.Is(err, ErrUnavailable) || got {
		t.Fatal("an unreadable source identity passed", got, err)
	}
}

func TestNamespaceWalkBoundFailsClosed(t *testing.T) {
	f := newIdentityFixture(t)
	old := candidateIPCNamespaceDirLimit
	candidateIPCNamespaceDirLimit = 2
	t.Cleanup(func() { candidateIPCNamespaceDirLimit = old })
	if got, err := referencedFresh(t, f.namespace, f.sibling); !errors.Is(err, ErrUnavailable) || got {
		t.Fatal("a namespace too large to enumerate passed", got, err)
	}
}

func TestIdentityHonoursCancellation(t *testing.T) {
	f := newIdentityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := candidateIPCReferenced(ctx, f.namespace, f.sibling); err == nil || got {
		t.Fatal("a cancelled check passed", got, err)
	}
}

// A real bind mount of the namespace, of its parent and of a descendant. Needs root and a mount-capable
// environment, so it skips elsewhere; the container acceptance job runs it.
func TestBindMountAliasIsAnOverlap(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bind mounts need root")
	}
	f := newIdentityFixture(t)
	mountRoot := t.TempDir()
	bind := func(name, from string) string {
		to := filepath.Join(mountRoot, name)
		if err := os.MkdirAll(to, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("mount", "--bind", from, to).CombinedOutput(); err != nil {
			t.Skipf("cannot bind mount here: %v: %s", err, out)
		}
		t.Cleanup(func() { unix.Unmount(to, unix.MNT_DETACH) })
		return to
	}
	for name, c := range map[string]struct {
		from string
		want bool
	}{
		"namespace":  {f.namespace, true},
		"data root":  {f.data, true},
		"descendant": {f.nested, true},
		"sibling":    {f.sibling, false},
	} {
		t.Run(name, func(t *testing.T) {
			alias := bind(name, c.from)
			if strings.HasPrefix(alias, f.data) {
				t.Fatal("fixture: the bind must not be under the data root")
			}
			if got, err := referencedFresh(t, f.namespace, alias); err != nil || got != c.want {
				t.Fatalf("bind of %s: overlap=%v err=%v, want %v", name, got, err, c.want)
			}
		})
	}
}
