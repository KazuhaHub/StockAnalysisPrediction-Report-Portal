package data

import "testing"

func TestRequestBounds(t *testing.T) {
	r := Request{"00000000-0000-0000-0000-000000000001", 1, 8 << 20, 128}
	if !r.valid() {
		t.Fatal("valid reservation refused")
	}
	for _, bad := range []Request{{"../other", 1, 512, 1}, {r.NamespaceID, 0, 512, 1}, {r.NamespaceID, 1, 513, 1}, {r.NamespaceID, 1, 0, 1}, {r.NamespaceID, 1, 512, 0}} {
		if bad.valid() {
			t.Fatal("unsafe reservation accepted", bad)
		}
	}
}

func TestRootIdentityBounds(t *testing.T) {
	good := RootIdentity{FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", Inode: 1}
	if !good.valid() {
		t.Fatal("valid root identity refused")
	}
	for _, bad := range []RootIdentity{
		{}, {FilesystemUUID: good.FilesystemUUID}, {FilesystemUUID: "00000000-0000-0000-0000-000000000000", Inode: 1},
		{FilesystemUUID: "01234567-89AB-cdef-0123-456789abcdef", Inode: 1},
		{FilesystemUUID: "../root", Inode: 1},
	} {
		if bad.valid() {
			t.Fatal("invalid physical root identity accepted", bad)
		}
	}
}
