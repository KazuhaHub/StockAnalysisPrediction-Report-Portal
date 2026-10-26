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
	var zero FrontendFile
	if !safeRelativePath(name) {
		return zero, ErrFrontendFileUnavailable
	}
	inspected, err := InspectFrontend(m, raw)
	if err != nil {
		return zero, err
	}
	var metadata FrontendFileInspection
	found := false
	for _, item := range inspected.Files {
		if item.Path == name {
			metadata = item
			found = true
			break
		}
	}
	if !found {
		return zero, ErrFrontendFileUnavailable
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return zero, ErrInvalidFrontend
	}
	for _, file := range archive.File {
		if file.Name != name {
			continue
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
	return zero, ErrFrontendFileUnavailable
}
