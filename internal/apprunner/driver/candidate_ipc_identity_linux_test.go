//go:build linux

package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// aliasPath is a path in its own temporary directory that does not exist and that an unprivileged runner can
// search: a lookup that cannot be examined (EACCES under /var/lib/docker, say) is a refusal, not an absence.
func aliasPath(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, name)
}

func referencedFresh(t *testing.T, path, source string) (bool, error) {
	t.Helper()
	return candidateIPCReferenced(context.Background(), path, source)
}

// fakeMountInfo points the inventory at a synthetic /proc/self/mountinfo: every path lives on device 8:1 mounted
// at "/", and each alias entry is a bind mount of an fs-relative directory at an unrelated mount point, the shape
// the host shows for a bind-backed local volume. It stands in for a bind mount where the test has no privilege;
// TestBindMountAliasIsAnOverlap uses real ones.
func fakeMountInfo(t *testing.T, binds map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("1 0 8:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw\n")
	n := 2
	for point, root := range binds {
		fmt.Fprintf(&b, "%d 1 8:1 %s %s rw,relatime shared:1 - ext4 /dev/vda1 rw\n", n, root, point)
		n++
	}
	file := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(file, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	old := candidateIPCMountInfoPath
	candidateIPCMountInfoPath = file
	t.Cleanup(func() { candidateIPCMountInfoPath = old })
}

func TestIdentityOverlapSemantics(t *testing.T) {
	f := newIdentityFixture(t)
	alias := aliasPath(t, "volumes/vol/_data")
	cases := []struct {
		name string
		real string // the directory the alias really is
		want bool
	}{
		{"the namespace itself", f.namespace, true},
		{"an ancestor of the namespace (the data root)", f.data, true},
		{"a far ancestor", f.root, true},
		{"the filesystem root", "/", true},
		{"a descendant of the namespace", f.nested, true},
		{"a sibling namespace", f.sibling, false},
		{"a directory elsewhere", "/srv/elsewhere", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeMountInfo(t, map[string]string{alias: c.real})
			got, err := referencedFresh(t, f.namespace, alias)
			if err != nil || got != c.want {
				t.Fatalf("overlap=%v err=%v, want %v", got, err, c.want)
			}
		})
	}
}

// The alias is not a symlink, is not under the namespace and does not name it: only the mount table shows
// where it leads.
func TestIdentityOverlapSeesAnAliasThatPathsDoNot(t *testing.T) {
	f := newIdentityFixture(t)
	alias := aliasPath(t, "unrelated/looks-harmless")
	fakeMountInfo(t, map[string]string{alias: f.namespace})
	if strings.HasPrefix(alias, f.namespace) || strings.HasPrefix(f.namespace, alias) {
		t.Fatal("fixture: the alias must not overlap by name")
	}
	if got, err := referencedFresh(t, f.namespace, alias); err != nil || !got {
		t.Fatal("alias of the namespace by device and path within the filesystem missed", got, err)
	}
	if got, err := referencedFresh(t, f.sibling, alias); err != nil || got {
		t.Fatal("alias reported against the wrong namespace", got, err)
	}
	// An alias of a path below the data root reaches a namespace that does not exist yet.
	future := filepath.Join(f.data, "ns-future")
	fakeMountInfo(t, map[string]string{alias: future})
	if got, err := referencedFresh(t, future, alias); err != nil || !got {
		t.Fatal("alias of a namespace that does not exist yet missed", got, err)
	}
}

func TestDifferentDeviceNeverOverlaps(t *testing.T) {
	f := newIdentityFixture(t)
	file := filepath.Join(t.TempDir(), "mountinfo")
	other := aliasPath(t, "other")
	body := "1 0 8:1 / / rw - ext4 /dev/vda1 rw\n2 1 9:9 / " + other + " rw - ext4 /dev/vdb rw\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	old := candidateIPCMountInfoPath
	candidateIPCMountInfoPath = file
	t.Cleanup(func() { candidateIPCMountInfoPath = old })
	if got, err := referencedFresh(t, f.namespace, other+"/sub"); err != nil || got {
		t.Fatal("a path on another device was an overlap", got, err)
	}
}

func TestMountInfoParsing(t *testing.T) {
	raw := "36 35 98:0 /mnt\\040a /mnt2/with\\040space rw,noatime master:1 - ext3 /dev/root rw\n" +
		"37 36 98:0 / /mnt2 rw - ext3 /dev/root rw\n" +
		"290 32 0:5 net:[4026532761] /run/docker/netns/a94d17aa1840 rw shared:429 - nsfs nsfs rw\n"
	mounts, err := parseCandidateIPCMountInfo([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) < 2 || mounts[0].point != "/mnt2/with space" || mounts[0].root != "/mnt a" || mounts[0].dev != "98:0" {
		t.Fatalf("octal escapes or fields misread: %+v", mounts)
	}
	if len(mounts) != 2 {
		t.Fatalf("a namespace file mount must be skipped, not placed or refused: %+v", mounts)
	}
	for _, bad := range []string{"1 2 3\n", "x y z w\n", ""} {
		if _, err := parseCandidateIPCMountInfo([]byte(bad)); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestStackedMountUsesTheTopmostEntry(t *testing.T) {
	f := newIdentityFixture(t)
	file := filepath.Join(t.TempDir(), "mountinfo")
	body := "1 0 8:1 / / rw - ext4 a rw\n" +
		"2 1 8:2 / " + f.data + " rw - ext4 b rw\n" +
		"3 1 8:3 / " + f.data + " rw - ext4 c rw\n" // covers the previous one
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	old := candidateIPCMountInfoPath
	candidateIPCMountInfoPath = file
	t.Cleanup(func() { candidateIPCMountInfoPath = old })
	mounts, err := loadCandidateIPCMounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	place, err := mounts.place(f.namespace)
	if err != nil || place.dev != "8:3" {
		t.Fatal("the covering mount was not used", place, err)
	}
}

func TestUnmappablePathsFailClosed(t *testing.T) {
	f := newIdentityFixture(t)
	old := candidateIPCMountInfoPath
	t.Cleanup(func() { candidateIPCMountInfoPath = old })
	candidateIPCMountInfoPath = filepath.Join(t.TempDir(), "missing")
	if got, err := referencedFresh(t, f.namespace, f.sibling); !errors.Is(err, ErrUnavailable) || got {
		t.Fatal("an unreadable mount table passed", got, err)
	}
	file := filepath.Join(t.TempDir(), "mountinfo")
	os.WriteFile(file, []byte("1 0 8:1 / /somewhere rw - ext4 a rw\n"), 0600) // nothing covers the namespace
	candidateIPCMountInfoPath = file
	if got, err := referencedFresh(t, f.namespace, f.sibling); !errors.Is(err, ErrUnavailable) || got {
		t.Fatal("a path with no mount entry passed", got, err)
	}
	os.WriteFile(file, []byte("garbage\n"), 0600)
	if got, err := referencedFresh(t, f.namespace, f.sibling); !errors.Is(err, ErrUnavailable) || got {
		t.Fatal("a malformed mount table passed", got, err)
	}
}

func TestAbsentSourceUnderTheNamespaceIsAReference(t *testing.T) {
	f := newIdentityFixture(t)
	for name, source := range map[string]string{
		"directly under the namespace": filepath.Join(f.namespace, "created-on-start"),
		"deep under the namespace":     filepath.Join(f.namespace, "a", "b", "c"),
		"under a nested directory":     filepath.Join(f.nested, "new"),
		"dotdot back into it":          f.sibling + "/../ns-a/x",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := referencedFresh(t, f.namespace, source); err != nil || !got {
				t.Fatal("an absent source Docker would create inside the namespace passed", got, err)
			}
		})
	}
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

func TestAbsentSourceThatIsTheMissingNamespaceIsAReference(t *testing.T) {
	f := newIdentityFixture(t)
	future := filepath.Join(f.data, "ns-future")
	for _, source := range []string{future, filepath.Join(future, "x")} {
		if got, err := referencedFresh(t, future, source); err != nil || !got {
			t.Fatal("absent source that names the absent namespace passed", source, got, err)
		}
	}
}

// A symlink is followed to where it lands, existing or not: Docker follows it when the target appears, and it
// creates the target of a missing source on each start.
func TestSymlinkSourcesAreJudgedWhereTheyLand(t *testing.T) {
	f := newIdentityFixture(t)
	link := func(name, target string) string {
		p := filepath.Join(f.root, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("dangling link into the namespace", func(t *testing.T) {
		l := link("into-ns", filepath.Join(f.namespace, "not-yet"))
		if got, err := referencedFresh(t, f.namespace, l); err != nil || !got {
			t.Fatal("dangling link into the namespace passed", got, err)
		}
	})
	t.Run("dangling link into a subdirectory that does not exist", func(t *testing.T) {
		l := link("into-missing", filepath.Join(f.namespace, "nope", "deeper", "still"))
		if got, err := referencedFresh(t, f.namespace, l); err != nil || !got {
			t.Fatal("link into a missing subdirectory passed", got, err)
		}
	})
	t.Run("relative dangling link into the namespace", func(t *testing.T) {
		l := link("rel", "data/ns-a/later")
		if got, err := referencedFresh(t, f.namespace, l); err != nil || !got {
			t.Fatal("relative dangling link passed", got, err)
		}
	})
	t.Run("link chain", func(t *testing.T) {
		l2 := link("chain2", filepath.Join(f.namespace, "end"))
		l1 := link("chain1", l2)
		if got, err := referencedFresh(t, f.namespace, l1); err != nil || !got {
			t.Fatal("link chain into the namespace passed", got, err)
		}
	})
	t.Run("symlink component partway along an absent path", func(t *testing.T) {
		l := link("mid", filepath.Join(f.namespace, "gone"))
		if got, err := referencedFresh(t, f.namespace, filepath.Join(l, "a", "b")); err != nil || !got {
			t.Fatal("absent path through a dangling link passed", got, err)
		}
	})
	t.Run("dangling link elsewhere", func(t *testing.T) {
		l := link("away", filepath.Join(f.root, "nowhere"))
		if got, err := referencedFresh(t, f.namespace, l); err != nil || got {
			t.Fatal("dangling link outside the namespace was a reference", got, err)
		}
	})
	t.Run("cycle fails closed", func(t *testing.T) {
		a, b := filepath.Join(f.root, "cyc-a"), filepath.Join(f.root, "cyc-b")
		os.Symlink(b, a)
		os.Symlink(a, b)
		if got, err := referencedFresh(t, f.namespace, a); !errors.Is(err, ErrUnavailable) || got {
			t.Fatal("a link cycle passed", got, err)
		}
		self := filepath.Join(f.root, "self")
		os.Symlink(self, self)
		if got, err := referencedFresh(t, f.namespace, filepath.Join(self, "x")); !errors.Is(err, ErrUnavailable) || got {
			t.Fatal("a self link passed", got, err)
		}
	})
	t.Run("hop bound", func(t *testing.T) {
		chain := func(n int) string {
			prev := filepath.Join(f.namespace, "final")
			for i := 0; i < n; i++ {
				cur := filepath.Join(f.root, fmt.Sprintf("hop%d-%d", n, i))
				if err := os.Symlink(prev, cur); err != nil {
					t.Fatal(err)
				}
				prev = cur
			}
			return prev
		}
		if got, err := referencedFresh(t, f.namespace, chain(candidateIPCMaxLinkHops)); err != nil || !got {
			t.Fatal("a chain at the bound was refused", got, err)
		}
		if got, err := referencedFresh(t, f.namespace, chain(candidateIPCMaxLinkHops+1)); !errors.Is(err, ErrUnavailable) || got {
			t.Fatal("a chain past the bound passed", got, err)
		}
	})
}

// Docker cannot create the directory behind a dangling symlink at a path it cannot make, but a link that lands
// elsewhere is simply elsewhere.
func TestCanonicalizeMatchesTheKernelForExistingPaths(t *testing.T) {
	f := newIdentityFixture(t)
	link := filepath.Join(f.root, "to-data")
	if err := os.Symlink("data/ns-a/../ns-b", link); err != nil {
		t.Fatal(err)
	}
	got, err := candidateIPCCanonicalize(link + "/")
	want, _ := filepath.EvalSymlinks(link)
	if err != nil || got != want {
		t.Fatalf("canonical %q (%v), kernel %q", got, err, want)
	}
	if _, err := candidateIPCCanonicalize(filepath.Join(f.sibling, "file", "x")); err != nil {
		t.Fatal("a missing tail must resolve", err)
	}
	file := filepath.Join(f.sibling, "regular")
	os.WriteFile(file, nil, 0600)
	if _, err := candidateIPCCanonicalize(filepath.Join(file, "x")); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatal("a path through a regular file must fail", err)
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

// Namespace-side lookups are abandoned at the deadline like source lookups: a stuck mount cannot hold the caller.
func TestNamespaceLookupIsAbandonedWhenTheContextEnds(t *testing.T) {
	f := newIdentityFixture(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	old := candidateIPCNamespaceResolver
	candidateIPCNamespaceResolver = func(string) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { close(release); candidateIPCNamespaceResolver = old })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got, err := candidateIPCReferenced(ctx, f.namespace, f.sibling); err == nil || got {
		t.Fatal("a stuck namespace lookup held the caller", got, err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("the namespace lookup was not routed through the resolver")
	}
}

func TestMountInfoReadIsAbandonedWhenTheContextEnds(t *testing.T) {
	f := newIdentityFixture(t)
	release := make(chan struct{})
	old := candidateIPCMountInfoReader
	candidateIPCMountInfoReader = func() ([]byte, error) { <-release; return nil, os.ErrNotExist }
	t.Cleanup(func() { close(release); candidateIPCMountInfoReader = old })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got, err := candidateIPCReferenced(ctx, f.namespace, f.sibling); err == nil || got {
		t.Fatal("a stuck mount table read held the caller", got, err)
	}
}

// Real bind mounts of the namespace, of the data root, of a descendant and of a sibling, read back through the
// real /proc/self/mountinfo. Needs root and a mount-capable environment, so it skips elsewhere; the foreign
// container job of the app runner probe workflow runs it as root on a GitHub-hosted runner.
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
