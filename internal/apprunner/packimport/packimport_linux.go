//go:build linux

// Package packimport packs a prepared directory tree into an import archive for the runner's import inbox
// (doc 185 section 5.1). It is the host-side half of the administrator data import: it validates the tree with
// the checkpoint walker's rules, normalises owners and modes, and never overwrites an existing archive.
package packimport

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

const (
	maxDepth      = 64
	maxNameBytes  = 255
	maxPathBytes  = 4096
	maxEntries    = 1000000
	maxFileBytes  = int64(16) << 30
	partialPrefix = ".packing-"
)

// ErrExists is an output name that is already taken. Pack never replaces an archive.
var ErrExists = errors.New("an archive with that name already exists in the import inbox")

// TreeError is a refusal of the source tree or the output location, naming the path that caused it.
type TreeError struct{ Path, Reason string }

func (e *TreeError) Error() string {
	if e.Path == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

func refuse(rel, format string, args ...any) error {
	return &TreeError{Path: rel, Reason: fmt.Sprintf(format, args...)}
}

// Options names the tree and the archive. Out must be <checkpoint_root>/.portal-imports/<name>.tar.
type Options struct {
	From, Out string
	// Packer is the packer version recorded in the manifest.
	Packer string
}

// Result is what the operator copies into the Portal review.
type Result struct {
	// SHA256 is the manifest's body digest, the archive's identity.
	SHA256 string
	// Bytes is the archive file size; Entries counts directories and files; ContentBytes totals file content.
	Bytes, Entries, ContentBytes int64
}

var outputName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,126}\.tar$`)

type node struct {
	rel       string
	directory bool
	size      int64
	dev, ino  uint64
	mtime     unix.Timespec
	// nlink is the file's link count when it was walked (more than one only for a file with hard links).
	nlink uint64
	// link is the first path of the same inode when this is a later path of a hard-linked file. Such a node is
	// written as a link entry: no content, no inode.
	link string
}

// linkedFile is a regular file met more than once during the walk.
type linkedFile struct {
	first        string
	nlink, found uint64
}

type inodeKey struct{ dev, ino uint64 }

// Pack validates and packs a tree. It writes nothing under the inbox until the whole tree has been accepted, and
// removes its partial file on any failure.
func Pack(ctx context.Context, opts Options) (Result, error) {
	var zero Result
	if opts.Packer == "" {
		return zero, errors.New("packer version is required")
	}
	from, err := filepath.Abs(opts.From)
	if err != nil || opts.From == "" {
		return zero, refuse("", "--from must name a directory")
	}
	out, err := filepath.Abs(opts.Out)
	if err != nil || opts.Out == "" {
		return zero, refuse("", "--out must name an archive")
	}
	name := filepath.Base(out)
	inbox := filepath.Dir(out)
	if !outputName.MatchString(name) || filepath.Base(inbox) != data.ImportInboxDirectory {
		return zero, refuse("", "--out must be <checkpoint_root>/%s/<name>.tar with a plain name of letters, digits, dot, dash and underscore", data.ImportInboxDirectory)
	}
	if inbox == from || strings.HasPrefix(inbox+"/", from+"/") || strings.HasPrefix(from+"/", inbox+"/") {
		return zero, refuse("", "the source tree and the import inbox must not contain one another")
	}
	nodes, err := walk(ctx, from)
	if err != nil {
		return zero, err
	}
	dir, err := openInbox(inbox)
	if err != nil {
		return zero, err
	}
	defer dir.Close()
	return write(ctx, from, dir, name, nodes, opts.Packer)
}

func walk(ctx context.Context, from string) ([]node, error) {
	var root unix.Stat_t
	if err := unix.Lstat(from, &root); err != nil || root.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, refuse("", "the source must be an existing directory and not a symbolic link")
	}
	var nodes []node
	var total int64
	linked := map[inodeKey]*linkedFile{}
	var visit func(dir, rel string, depth int) error
	visit = func(dir, rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return refuse(rel, "cannot read directory: %v", err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			child := path.Join(rel, name)
			if rel == "" {
				child = name
			}
			if depth+1 > maxDepth {
				return refuse(child, "deeper than %d levels", maxDepth)
			}
			if len(name) > maxNameBytes {
				return refuse(child, "a name longer than %d bytes", maxNameBytes)
			}
			if !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n\\") {
				return refuse(child, "a name that is not valid UTF-8 or holds a control character or backslash")
			}
			if len(child) > maxPathBytes {
				return refuse(child, "a path longer than %d bytes", maxPathBytes)
			}
			if child == data.ArchiveManifestEntry {
				return refuse(child, "reserved name; rename or remove it")
			}
			var st unix.Stat_t
			if err := unix.Lstat(filepath.Join(dir, name), &st); err != nil {
				return refuse(child, "cannot stat: %v", err)
			}
			if st.Dev != root.Dev {
				return refuse(child, "on another filesystem; pack one filesystem at a time")
			}
			if st.Mode&unix.S_ISUID != 0 {
				return refuse(child, "has the setuid bit; clear it with chmod u-s")
			}
			if st.Mode&unix.S_ISGID != 0 {
				return refuse(child, "has the setgid bit; clear it with chmod g-s")
			}
			if st.Mode&unix.S_ISVTX != 0 {
				return refuse(child, "has the sticky bit; clear it with chmod -t")
			}
			if len(nodes) >= maxEntries {
				return refuse(child, "more than %d entries", maxEntries)
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				nodes = append(nodes, node{rel: child, directory: true, dev: uint64(st.Dev), ino: st.Ino})
				if err := visit(filepath.Join(dir, name), child, depth+1); err != nil {
					return err
				}
			case unix.S_IFREG:
				if st.Nlink > 1 {
					// Doc 185 section 4: a file with several links is accepted when every link is inside the
					// tree. The first path in archive order carries the content; later paths are link entries.
					key := inodeKey{uint64(st.Dev), st.Ino}
					if file := linked[key]; file != nil {
						if file.nlink != uint64(st.Nlink) {
							return refuse(child, "changed while packing")
						}
						file.found++
						nodes = append(nodes, node{rel: child, dev: uint64(st.Dev), ino: st.Ino, nlink: file.nlink, link: file.first})
						continue
					}
					linked[key] = &linkedFile{first: child, nlink: uint64(st.Nlink), found: 1}
				}
				if st.Size < 0 || st.Size > maxFileBytes || total > maxFileBytes-st.Size {
					return refuse(child, "the tree is larger than %d GiB", maxFileBytes>>30)
				}
				total += st.Size
				nodes = append(nodes, node{rel: child, size: st.Size, dev: uint64(st.Dev), ino: st.Ino, mtime: st.Mtim, nlink: uint64(st.Nlink)})
			case unix.S_IFLNK:
				return refuse(child, "is a symbolic link; the tree may hold only regular files and directories")
			default:
				return refuse(child, "is not a regular file or directory; the tree may hold only regular files and directories")
			}
		}
		return nil
	}
	if err := visit(from, "", 0); err != nil {
		return nil, err
	}
	// A link outside the tree would make the restored file differ from the source, and the namespace cannot hold
	// it: the paths found must equal the link count. The first such file in name order is named.
	outside := ""
	var missing uint64
	for _, file := range linked {
		if file.found != file.nlink && (outside == "" || file.first < outside) {
			outside, missing = file.first, file.nlink-file.found
		}
	}
	if outside != "" {
		return nil, refuse(outside, "has a hard link outside the tree (%d more than the tree holds); a hard link must have all its links inside the tree, so copy the file or remove the outside link", missing)
	}
	// The restore reader requires strictly ascending full names with a directory written as "name/". A tree whose
	// walk order differs (a directory "a" next to a sibling such as "a.txt", which sorts before "a/") cannot be
	// restored, so it is refused here rather than at import.
	last := ""
	for _, n := range nodes {
		name := n.rel
		if n.directory {
			name += "/"
		}
		if name <= last {
			return nil, refuse(n.rel, "sorts before the entry that precedes it in the archive (a directory next to a sibling whose name sorts between it and its contents, such as \"a\" and \"a.txt\"); rename one of them")
		}
		last = name
	}
	return nodes, nil
}

// openInbox opens the inbox directory, creating it (root-owned, 0700) under an existing checkpoint root, and
// checks that it is a private directory of the caller.
func openInbox(inbox string) (*os.File, error) {
	if err := unix.Mkdir(inbox, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, refuse("", "cannot create the import inbox (the checkpoint root must exist): %v", err)
	}
	fd, err := unix.Open(inbox, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, refuse("", "cannot open the import inbox: %v", err)
	}
	dir := os.NewFile(uintptr(fd), "import-inbox")
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || int(st.Uid) != os.Geteuid() || st.Mode&07777 != 0700 {
		dir.Close()
		return nil, refuse("", "the import inbox must be a directory owned by the caller with mode 0700")
	}
	return dir, nil
}

func write(ctx context.Context, from string, dir *os.File, name string, nodes []node, packer string) (Result, error) {
	var zero Result
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return zero, err
	}
	partial := partialPrefix + hex.EncodeToString(suffix) + ".partial"
	fd, err := unix.Openat(int(dir.Fd()), partial, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return zero, err
	}
	file := os.NewFile(uintptr(fd), partial)
	cleanup := true
	defer func() {
		file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(dir.Fd()), partial, 0)
		}
	}()
	if _, err := file.Write(make([]byte, data.ImportManifestBlockBytes)); err != nil {
		return zero, err
	}
	hash := sha256.New()
	body := io.MultiWriter(file, hash)
	writer := tar.NewWriter(body)
	var entries, content int64
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		header := &tar.Header{
			Format: tar.FormatPAX, Name: n.rel, Uid: data.ImportOwner, Gid: data.ImportOwner,
			ModTime: time.Unix(0, 0).UTC(),
		}
		if n.directory {
			header.Name, header.Typeflag, header.Mode = n.rel+"/", tar.TypeDir, data.ImportDirectoryMode
			if err := writer.WriteHeader(header); err != nil {
				return zero, err
			}
			entries++
			continue
		}
		if n.link != "" {
			header.Typeflag, header.Mode, header.Linkname = tar.TypeLink, data.ImportFileMode, n.link
			if err := writer.WriteHeader(header); err != nil {
				return zero, err
			}
			entries++
			continue
		}
		header.Typeflag, header.Mode, header.Size = tar.TypeReg, data.ImportFileMode, n.size
		if err := writer.WriteHeader(header); err != nil {
			return zero, err
		}
		if err := copyFile(writer, filepath.Join(from, filepath.FromSlash(n.rel)), n); err != nil {
			return zero, refuse(n.rel, "%v", err)
		}
		entries++
		content += n.size
	}
	if err := writer.Close(); err != nil {
		return zero, err
	}
	manifest := data.ImportManifest{
		Format: data.ImportManifestFormat, Entries: entries, Bytes: content,
		BodySHA256: hex.EncodeToString(hash.Sum(nil)), Packer: packer,
	}
	raw, err := data.MarshalImportManifest(manifest)
	if err != nil {
		return zero, err
	}
	var block bytes.Buffer
	first := tar.NewWriter(&block)
	if err := first.WriteHeader(&tar.Header{Format: tar.FormatUSTAR, Name: data.ArchiveManifestEntry, Typeflag: tar.TypeReg,
		Mode: 0400, Size: int64(len(raw)), ModTime: time.Unix(0, 0).UTC()}); err != nil {
		return zero, err
	}
	if _, err := first.Write(raw); err != nil {
		return zero, err
	}
	if err := first.Flush(); err != nil || block.Len() != data.ImportManifestBlockBytes {
		return zero, errors.New("manifest block has an unexpected size")
	}
	if _, err := file.WriteAt(block.Bytes(), 0); err != nil {
		return zero, err
	}
	if err := file.Chmod(0400); err != nil {
		return zero, err
	}
	if err := file.Sync(); err != nil {
		return zero, err
	}
	info, err := file.Stat()
	if err != nil {
		return zero, err
	}
	// link fails when the name is taken, so an existing archive is never replaced.
	if err := unix.Linkat(int(dir.Fd()), partial, int(dir.Fd()), name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return zero, ErrExists
		}
		return zero, err
	}
	cleanup = false
	_ = unix.Unlinkat(int(dir.Fd()), partial, 0)
	if err := dir.Sync(); err != nil {
		return zero, err
	}
	return Result{SHA256: manifest.BodySHA256, Bytes: info.Size(), Entries: entries, ContentBytes: content}, nil
}

// copyFile copies one file and refuses it if it changed or is not the file the walk saw.
func copyFile(w io.Writer, name string, n node) error {
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("cannot open: %v", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG ||
		uint64(before.Dev) != n.dev || before.Ino != n.ino || before.Size != n.size || uint64(before.Nlink) != max(n.nlink, 1) {
		return errors.New("changed while packing")
	}
	copied, err := io.CopyN(w, file, n.size)
	if err != nil || copied != n.size {
		return errors.New("changed while packing")
	}
	var extra [1]byte
	if k, err := file.Read(extra[:]); k != 0 || err != io.EOF {
		return errors.New("changed while packing")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || after.Size != before.Size || after.Mtim != before.Mtim {
		return errors.New("changed while packing")
	}
	return nil
}
