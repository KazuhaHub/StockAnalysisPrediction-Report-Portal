package appplatform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

var ErrFrontendFileUnavailable = errors.New("application frontend file is unavailable")

type FrontendFile struct {
	Metadata FrontendFileInspection
	Content  []byte
}

// ReadFrontendFile returns a fresh bounded buffer from a fully verified archive.
// A file name is exact and case sensitive: no URL decoding or path cleanup takes
// place here. Integrity is not publisher verification or permission to serve.
func ReadFrontendFile(m Manifest, raw []byte, name string) (FrontendFile, error) {
	if !safeRelativePath(name) {
		return FrontendFile{}, ErrFrontendFileUnavailable
	}
	archive, err := newFrontendArchive(m, raw)
	if err != nil {
		return FrontendFile{}, err
	}
	return archive.read(name)
}

type frontendArchive struct {
	files    map[string]*zip.File
	metadata map[string]FrontendFileInspection
}

func newFrontendArchive(m Manifest, raw []byte) (*frontendArchive, error) {
	inspected, err := InspectFrontend(m, raw)
	if err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, ErrInvalidFrontend
	}
	archive := &frontendArchive{files: map[string]*zip.File{}, metadata: map[string]FrontendFileInspection{}}
	for _, file := range reader.File {
		archive.files[file.Name] = file
	}
	for _, meta := range inspected.Files {
		archive.metadata[meta.Path] = meta
	}
	return archive, nil
}
func (a *frontendArchive) read(name string) (FrontendFile, error) {
	var zero FrontendFile
	metadata, ok := a.metadata[name]
	if !ok || !safeRelativePath(name) {
		return zero, ErrFrontendFileUnavailable
	}
	file := a.files[name]
	if file == nil {
		return zero, ErrInvalidFrontend
	}
	stream, err := file.Open()
	if err != nil {
		return zero, ErrInvalidFrontend
	}
	content, readErr := io.ReadAll(io.LimitReader(stream, MaxFrontendFileBytes+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || int64(len(content)) != metadata.Bytes || len(content) > MaxFrontendFileBytes {
		return zero, ErrInvalidFrontend
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != metadata.SHA256 {
		return zero, ErrFrontendDigestMismatch
	}
	return FrontendFile{Metadata: metadata, Content: content}, nil
}
