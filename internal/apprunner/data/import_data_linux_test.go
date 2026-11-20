//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const importTestNamespace = "00000000-0000-0000-0000-000000000042"

func raiseRequest() QuotaRaiseRequest {
	return QuotaRaiseRequest{
		NamespaceID: importTestNamespace, ProjectID: 4242,
		OldBytes: 8 << 20, OldInodes: 128, NewBytes: 16 << 20, NewInodes: 256,
		ExpectedDevice: 7, ExpectedNamespaceInode: 9,
	}
}

func TestRaiseQuotaRefusesLoweringAndMalformedRequestsBeforeTouchingAnything(t *testing.T) {
	root := &Root{}
	for name, mutate := range map[string]func(*QuotaRaiseRequest){
		"lower bytes":  func(r *QuotaRaiseRequest) { r.NewBytes = 4 << 20 },
		"lower inodes": func(r *QuotaRaiseRequest) { r.NewInodes = 64 },
	} {
		request := raiseRequest()
		mutate(&request)
		if _, err := root.RaiseQuota(context.Background(), RootIdentity{}, request); !errors.Is(err, ErrQuotaLower) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*QuotaRaiseRequest){
		"no project":    func(r *QuotaRaiseRequest) { r.ProjectID = 0 },
		"unaligned":     func(r *QuotaRaiseRequest) { r.NewBytes = 16<<20 + 1 },
		"bad namespace": func(r *QuotaRaiseRequest) { r.NamespaceID = "x" },
		"no inode pin":  func(r *QuotaRaiseRequest) { r.ExpectedNamespaceInode = 0 },
	} {
		request := raiseRequest()
		mutate(&request)
		if _, err := root.RaiseQuota(context.Background(), RootIdentity{}, request); !errors.Is(err, ErrQuotaRaiseUnsafe) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestImportDataRefusesMalformedRequests(t *testing.T) {
	good := ImportDataRequest{
		NamespaceID: importTestNamespace, ProjectID: 4242, ArchiveSHA256: strings.Repeat("a", 64), ArchiveBytes: 4096,
		MaxArchiveBytes: 1 << 20, QuotaBytes: 8 << 20, QuotaInodes: 128, DataFormat: 1,
		ExpectedDevice: 7, ExpectedNamespaceInode: 9, WriterFence: 2,
	}
	if !good.valid() {
		t.Fatal("fixture request is not valid")
	}
	for name, mutate := range map[string]func(*ImportDataRequest){
		"digest":  func(r *ImportDataRequest) { r.ArchiveSHA256 = "short" },
		"size":    func(r *ImportDataRequest) { r.ArchiveBytes = r.MaxArchiveBytes + 1 },
		"quota":   func(r *ImportDataRequest) { r.QuotaBytes = 8<<20 + 1 },
		"fence":   func(r *ImportDataRequest) { r.WriterFence = 0 },
		"project": func(r *ImportDataRequest) { r.ProjectID = 0 },
	} {
		request := good
		mutate(&request)
		if request.valid() {
			t.Fatalf("%s accepted", name)
		}
		root := &Root{}
		if _, err := root.ImportData(context.Background(), RootIdentity{}, &Root{}, RootIdentity{}, request); !errors.Is(err, ErrImportUnsafe) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// importArchiveBytes builds an import archive by hand, to prove the reader and the format agree without the packer.
func importArchiveBytes(t *testing.T, files map[string]string, marker string) []byte {
	t.Helper()
	return importArchiveBytesWith(t, files, marker, true)
}

// importArchiveBytesWith builds an import archive as pack-import does; withDirectories false leaves out the directory
// entries of the files' parents, which pack-import never does.
func importArchiveBytesWith(t *testing.T, files map[string]string, marker string, withDirectories bool) []byte {
	t.Helper()
	var body bytes.Buffer
	writer := tar.NewWriter(&body)
	names := []string{}
	directories := map[string]bool{}
	for name := range files {
		names = append(names, name)
		for dir := path.Dir(name); withDirectories && dir != "."; dir = path.Dir(dir) {
			directories[dir+"/"] = true
		}
	}
	for dir := range directories {
		names = append(names, dir)
	}
	// ascending order: a directory "d/" sorts before its contents
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	var size int64
	for _, name := range names {
		if directories[name] {
			if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: ImportDirectoryMode, Uid: ImportOwner, Gid: ImportOwner,
				ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0640, Uid: 65532, Gid: 65532,
			Size: int64(len(content)), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		size += int64(len(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body.Bytes())
	raw, err := MarshalImportManifest(ImportManifest{Format: ImportManifestFormat, Entries: int64(len(names)), Bytes: size,
		BodySHA256: hex.EncodeToString(sum[:]), Packer: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var head bytes.Buffer
	first := tar.NewWriter(&head)
	if err := first.WriteHeader(&tar.Header{Name: ArchiveManifestEntry, Typeflag: tar.TypeReg, Mode: 0400, Size: int64(len(raw)), Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = marker
	return append(head.Bytes(), body.Bytes()...)
}

// An entry whose parent directory is not an earlier entry cannot be extracted: it must be refused when the archive is
// inspected, before an import clears anything.
func TestInspectImportArchiveRefusesAnEntryWithoutItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.tar")
	if err := os.WriteFile(path, importArchiveBytesWith(t, map[string]string{"seed/a.bin": "alpha"}, "", false), 0400); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got, err := InspectImportArchive(context.Background(), file, 100); !errors.Is(err, ErrImportArchiveInvalid) {
		t.Fatalf("an archive without the directory of its file was accepted: %+v %v", got, err)
	}
}

func TestInspectImportArchiveReadsMarkerAndRefusesTampering(t *testing.T) {
	archive := importArchiveBytes(t, map[string]string{SchemaMarkerName: "4\n", "bars/part": "data"}, "")
	path := filepath.Join(t.TempDir(), "a.tar")
	if err := os.WriteFile(path, archive, 0400); err != nil {
		t.Fatal(err)
	}
	open := func(p string) *os.File {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	got, err := InspectImportArchive(context.Background(), open(path), 100)
	if err != nil || got.SchemaMarker != 4 || got.Manifest.Entries != 3 || got.Manifest.Bytes != int64(len("4\n")+len("data")) {
		t.Fatalf("inspect: %+v %v", got, err)
	}
	// A marker is optional: none means data format 0.
	plain := filepath.Join(t.TempDir(), "p.tar")
	if err := os.WriteFile(plain, importArchiveBytes(t, map[string]string{"x": "y"}, ""), 0400); err != nil {
		t.Fatal(err)
	}
	if got, err := InspectImportArchive(context.Background(), open(plain), 100); err != nil || got.SchemaMarker != 0 {
		t.Fatalf("no marker: %+v %v", got, err)
	}
	// A flipped body byte no longer matches the manifest digest.
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)-1500] ^= 1
	bad := filepath.Join(t.TempDir(), "t.tar")
	if err := os.WriteFile(bad, tampered, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectImportArchive(context.Background(), open(bad), 100); !errors.Is(err, ErrImportArchiveInvalid) {
		t.Fatalf("tampered archive accepted: %v", err)
	}
	// The walk is bounded by the inode quota.
	if _, err := InspectImportArchive(context.Background(), open(path), 1); !errors.Is(err, ErrImportArchiveInvalid) {
		t.Fatalf("entry cap not applied: %v", err)
	}
	// A manifest of another format is refused.
	if _, err := ParseImportManifest([]byte(`{"format":"x","entries":0,"bytes":0,"body_sha256":"` + strings.Repeat("a", 64) + `","packer":"t"}`)); err == nil {
		t.Fatal("foreign manifest format accepted")
	}
	_ = strconv.Itoa
}
