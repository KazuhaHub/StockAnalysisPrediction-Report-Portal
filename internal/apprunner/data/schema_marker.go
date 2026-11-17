package data

import (
	"errors"
	"strconv"
)

// SchemaMarkerName is the file a migration writes as its last action. It holds the decimal data
// schema the namespace now has. It is self-declared by the migration, so it is a commit point and not proof.
const SchemaMarkerName = ".portal-data-schema"

// MaxSchemaMarkerBytes bounds the marker read.
const MaxSchemaMarkerBytes = 32

const maxSchemaMarkerValue = 1000000

var ErrMigrationUnsafe = errors.New("data schema marker is unsafe or unreadable")
var ErrMigrationUnsupported = errors.New("data schema marker is unsupported on this platform")

// parseSchemaMarker accepts one canonical positive decimal integer, optionally ending in one newline.
func parseSchemaMarker(raw []byte) (int, error) {
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 || len(raw) > MaxSchemaMarkerBytes {
		return 0, ErrMigrationUnsafe
	}
	for _, b := range raw {
		if b < '0' || b > '9' {
			return 0, ErrMigrationUnsafe
		}
	}
	value, err := strconv.Atoi(string(raw))
	if err != nil || value < 1 || value > maxSchemaMarkerValue || strconv.Itoa(value) != string(raw) {
		return 0, ErrMigrationUnsafe
	}
	return value, nil
}

// SchemaMarkerSnapshot is one point-in-time read of a namespace after a migration job: the marker
// (absent, or a schema) and the namespace usage against its hard quota.
type SchemaMarkerSnapshot struct {
	NamespaceID                     string
	ProjectID                       uint32
	Present                         bool
	Schema                          int
	UsedBytes, UsedInodes           int64
	ByteLimit, InodeLimit           int64
	NamespaceDevice, NamespaceInode uint64
	ObservedMS                      int64
}

// SchemaMarkerObservation cannot be constructed or decoded outside the native read.
type SchemaMarkerObservation struct {
	snapshot SchemaMarkerSnapshot
	valid    bool
}

func (SchemaMarkerObservation) MarshalJSON() ([]byte, error) { return nil, ErrMigrationUnsafe }
func (o *SchemaMarkerObservation) UnmarshalJSON([]byte) error {
	*o = SchemaMarkerObservation{}
	return ErrMigrationUnsafe
}
func (o SchemaMarkerObservation) Snapshot() (SchemaMarkerSnapshot, error) {
	s := o.snapshot
	if !o.valid || !namespaceID.MatchString(s.NamespaceID) || s.ProjectID == 0 || s.NamespaceDevice == 0 || s.NamespaceInode == 0 ||
		s.UsedBytes < 0 || s.UsedInodes < 0 || s.ByteLimit <= 0 || s.InodeLimit <= 0 ||
		s.UsedBytes > s.ByteLimit || s.UsedInodes > s.InodeLimit || s.ObservedMS <= 0 ||
		s.Present != (s.Schema != 0) || s.Schema < 0 || s.Schema > maxSchemaMarkerValue {
		return SchemaMarkerSnapshot{}, ErrMigrationUnsafe
	}
	return s, nil
}
