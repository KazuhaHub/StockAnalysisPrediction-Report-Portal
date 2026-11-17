package appplatform

import (
	"bytes"
	"testing"
)

// The principal directory (doc 181) lists values that a later launch must present
// byte for byte, so it derives them through the same frame as the viewer does.
func TestViewerPrincipalDeriverMatchesViewerIdentity(t *testing.T) {
	seed := bytes.Repeat([]byte{31}, 32)
	account := ViewerAccountIdentity{Name: "reader", Created: "created", Role: "user", Generation: 1}
	chain := []ViewerOUIdentity{{1, "created"}, {7, "ou-created"}}
	viewer, err := DeriveViewerIdentity("notes-sample", "00000000-0000-4000-8000-000000000003", seed, account, chain)
	if err != nil {
		t.Fatal(err)
	}
	deriver, err := NewViewerPrincipalDeriver("notes-sample", "00000000-0000-4000-8000-000000000003", seed)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := deriver.Account(account)
	if err != nil || subject != viewer.Subject {
		t.Fatal("account principal differs from the viewer subject", err)
	}
	for n, ou := range chain {
		value, err := deriver.OU(ou)
		if err != nil || value != viewer.OUs[n] {
			t.Fatal("OU principal differs from the viewer OU", n, err)
		}
	}
	if _, err := deriver.Account(ViewerAccountIdentity{Name: "", Created: "created"}); err == nil {
		t.Fatal("empty account name derived a principal")
	}
	if _, err := deriver.OU(ViewerOUIdentity{ID: 0, Created: "created"}); err == nil {
		t.Fatal("zero OU ID derived a principal")
	}
	if _, err := NewViewerPrincipalDeriver("notes-sample", "00000000-0000-4000-8000-000000000003", seed[:31]); err == nil {
		t.Fatal("short seed accepted")
	}
}
