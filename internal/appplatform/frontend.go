package appplatform

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

const (
	MaxFrontendArchiveBytes = 16 << 20
	MaxFrontendFileBytes    = 8 << 20
	MaxFrontendTotalBytes   = 64 << 20
	MaxFrontendEntries      = 2048
)

var (
	ErrInvalidFrontend        = errors.New("invalid application frontend archive")
	ErrFrontendDigestMismatch = errors.New("application frontend digest mismatch")
)

type FrontendFileInspection struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// FrontendInspection contains public integrity metadata, never file contents.
// It does not establish publisher trust or permission to serve or run these files.
type FrontendInspection struct {
	SHA256            string                   `json:"sha256"`
	Entry             string                   `json:"entry"`
	ArchiveBytes      int64                    `json:"archive_bytes"`
	UncompressedBytes int64                    `json:"uncompressed_bytes"`
	Files             []FrontendFileInspection `json:"files"`
}

type frontendLimits struct {
	archiveBytes, fileBytes, totalBytes int64
	entries                             int
}

// InspectFrontend hashes the exact artifact and streams each accepted asset to
// validate sizes/checksums. It never extracts files, executes scripts or performs IO
// outside the provided bytes. Limits are provisional until package contract freeze.
func InspectFrontend(m Manifest, raw []byte) (FrontendInspection, error) {
	return inspectFrontend(m, raw, frontendLimits{
		archiveBytes: MaxFrontendArchiveBytes, fileBytes: MaxFrontendFileBytes,
		totalBytes: MaxFrontendTotalBytes, entries: MaxFrontendEntries,
	})
}

func inspectFrontend(m Manifest, raw []byte, limits frontendLimits) (FrontendInspection, error) {
	if !validManifest(m) {
		return FrontendInspection{}, ErrInvalidDescriptor
	}
	if len(raw) == 0 || int64(len(raw)) > limits.archiveBytes {
		return FrontendInspection{}, ErrInvalidFrontend
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != m.Frontend.SHA256 {
		return FrontendInspection{}, ErrFrontendDigestMismatch
	}
	if !boundedFrontendDirectory(raw, limits.entries) {
		return FrontendInspection{}, ErrInvalidFrontend
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) == 0 || len(reader.File) > limits.entries {
		return FrontendInspection{}, ErrInvalidFrontend
	}
	entryType := frontendContentType(m.Presentation.Entry)
	if entryType != "text/html; charset=utf-8" {
		return FrontendInspection{}, ErrInvalidFrontend
	}

	type node struct {
		spelling  string
		directory bool
	}
	nodes := map[string]node{}
	explicit := map[string]bool{}
	files := make([]*zip.File, 0, len(reader.File))
	var advertised int64
	var compressed uint64
	for _, file := range reader.File {
		if file.CompressedSize64 > uint64(len(raw))-compressed {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		compressed += file.CompressedSize64
		mode := file.Mode()
		directory := strings.HasSuffix(file.Name, "/")
		name := strings.TrimSuffix(file.Name, "/")
		// Accept only the flag bits used by ordinary stored/deflated UTF-8 ZIPs.
		// Encryption and unsupported methods are refused before opening streams.
		if !safeRelativePath(name) || file.Flags & ^uint16(0x080e) != 0 ||
			(file.Method != zip.Store && file.Method != zip.Deflate) ||
			directory != mode.IsDir() || (mode.Type() != 0 && mode.Type() != fs.ModeDir) ||
			mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || (!directory && (!mode.IsRegular() || mode.Perm()&0111 != 0)) {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		folded := strings.ToLower(name)
		if explicit[folded] {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		explicit[folded] = true
		parts := strings.Split(name, "/")
		for i := 1; i <= len(parts); i++ {
			prefix := strings.Join(parts[:i], "/")
			key := strings.ToLower(prefix)
			isDirectory := i < len(parts) || directory
			if previous, ok := nodes[key]; ok && (previous.spelling != prefix || previous.directory != isDirectory) {
				return FrontendInspection{}, ErrInvalidFrontend
			}
			nodes[key] = node{prefix, isDirectory}
		}
		if directory {
			if file.UncompressedSize64 != 0 || file.CRC32 != 0 || !validFrontendDirectory(file) {
				return FrontendInspection{}, ErrInvalidFrontend
			}
			continue
		}
		if frontendContentType(name) == "" || file.UncompressedSize64 > uint64(limits.fileBytes) ||
			file.UncompressedSize64 > uint64(limits.totalBytes-advertised) {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		advertised += int64(file.UncompressedSize64)
		files = append(files, file)
	}
	result := FrontendInspection{SHA256: digest, Entry: m.Presentation.Entry, ArchiveBytes: int64(len(raw))}
	foundEntry := false
	scratch := make([]byte, 32<<10)
	for _, file := range files {
		stream, err := file.Open()
		if err != nil {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		hash := sha256.New()
		size, readErr := io.CopyBuffer(hash, io.LimitReader(stream, limits.fileBytes+1), scratch)
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || size > limits.fileBytes || uint64(size) != file.UncompressedSize64 || size > limits.totalBytes-result.UncompressedBytes {
			return FrontendInspection{}, ErrInvalidFrontend
		}
		if file.Name == m.Presentation.Entry {
			if size == 0 {
				return FrontendInspection{}, ErrInvalidFrontend
			}
			foundEntry = true
		}
		result.UncompressedBytes += size
		result.Files = append(result.Files, FrontendFileInspection{
			Path: file.Name, ContentType: frontendContentType(file.Name), Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil)),
		})
	}
	if !foundEntry {
		return FrontendInspection{}, ErrInvalidFrontend
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

// Keep content types independent of the host OS MIME registry and file sniffing.
// A future server must additionally sandbox every asset response and use nosniff.
func frontendContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".txt", ".md", ".csv":
		return "text/plain; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".wasm":
		return "application/wasm"
	}
	return ""
}

// archive/zip deliberately treats directory Open calls as empty. Inspect their
// raw stream as well so forged directory sizes do not bypass decompression checks.
func validFrontendDirectory(file *zip.File) bool {
	raw, err := file.OpenRaw()
	if err != nil {
		return false
	}
	if file.Method == zip.Store {
		return file.CompressedSize64 == 0
	}
	reader := flate.NewReader(raw)
	size, readErr := io.Copy(io.Discard, io.LimitReader(reader, 1))
	closeErr := reader.Close()
	return size == 0 && readErr == nil && closeErr == nil
}

// Count central records before archive/zip allocates per-entry metadata. The
// compressed-byte bound alone could otherwise admit hundreds of thousands of
// tiny entries. Archive-level ZIP64/multidisk and trailing data are not supported.
func boundedFrontendDirectory(raw []byte, maxEntries int) bool {
	if len(raw) < 22 || !bytes.HasPrefix(raw, []byte{'P', 'K', 3, 4}) {
		return false
	}
	start := len(raw) - 22 - 65535
	if start < 0 {
		start = 0
	}
	for end := len(raw) - 22; end >= start; end-- {
		if !bytes.Equal(raw[end:end+4], []byte{'P', 'K', 5, 6}) {
			continue
		}
		h := raw[end:]
		if end+22+int(binary.LittleEndian.Uint16(h[20:22])) != len(raw) {
			continue
		}
		count := int(binary.LittleEndian.Uint16(h[10:12]))
		if binary.LittleEndian.Uint16(h[4:6]) != 0 || binary.LittleEndian.Uint16(h[6:8]) != 0 ||
			int(binary.LittleEndian.Uint16(h[8:10])) != count || count == 0 || count > maxEntries {
			return false
		}
		size := uint64(binary.LittleEndian.Uint32(h[12:16]))
		offset := uint64(binary.LittleEndian.Uint32(h[16:20]))
		if offset+size != uint64(end) {
			return false
		}
		records := 0
		for cursor := int(offset); cursor < end; {
			if end-cursor < 46 || !bytes.Equal(raw[cursor:cursor+4], []byte{'P', 'K', 1, 2}) {
				return false
			}
			record := raw[cursor:]
			if binary.LittleEndian.Uint16(record[34:36]) != 0 {
				return false
			}
			length := 46 + int(binary.LittleEndian.Uint16(record[28:30])) + int(binary.LittleEndian.Uint16(record[30:32])) + int(binary.LittleEndian.Uint16(record[32:34]))
			if length > end-cursor {
				return false
			}
			records++
			if records > maxEntries {
				return false
			}
			cursor += length
		}
		return records == count
	}
	return false
}
