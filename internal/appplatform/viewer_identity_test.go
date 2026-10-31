package appplatform

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func TestViewerIdentityKnownVector(t *testing.T) {
	v, err := DeriveViewerIdentity("notes-sample", "00000000-0000-4000-8000-000000000003", bytes.Repeat([]byte{31}, 32), ViewerAccountIdentity{Name: "reader", Created: "created", Role: "user", Generation: 1}, []ViewerOUIdentity{{1, "created"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Subject != "4005b95e3de908f3ee9465047373197cd5adfd49e03fd3c5d6482abb772c53db" || v.OUs[0] != "876fc7008ae00b72ed358ffe5fd3f88d2eb434c867992e46226760f76a313b79" {
		t.Fatal("stable identity contract changed")
	}
}

func TestViewerIdentityAncestryBoundary(t *testing.T) {
	i := backendIdentityFixture()
	chain := make([]ViewerOUIdentity, 64)
	for n := range chain {
		chain[n] = ViewerOUIdentity{ID: int64(n + 1), Created: "created"}
	}
	a := ViewerAccountIdentity{Name: "内部用户", Created: "created", Role: "user"}
	seed := bytes.Repeat([]byte{31}, 32)
	v, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
	if err != nil || len(v.OUs) != 64 {
		t.Fatal("maximum valid ancestry refused", err)
	}
	chain = append(chain, ViewerOUIdentity{ID: 65, Created: "created"})
	if v, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("oversized valid ancestry truncated or accepted")
	}
}

func TestViewerIdentityStableAcrossBackendRotation(t *testing.T) {
	i := backendIdentityFixture()
	seed := bytes.Repeat([]byte{31}, 32)
	a := ViewerAccountIdentity{Name: "reader@example.test", Created: "2026-10-27T12:00:00Z", Role: "user", Generation: 3, SessionRevision: 4}
	chain := []ViewerOUIdentity{{12, "2026-10-26T12:00:00Z"}, {1, "2026-10-25T12:00:00Z"}}
	v, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
	if err != nil {
		t.Fatal(err)
	}
	old, err := NewBackendRegistration(i, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	oldMeta, _ := old.Metadata()
	i.Generation, i.PolicyRevision, i.ControllerEpoch, i.ReleaseSHA256 = "2", "3", "00000000-0000-4000-8000-000000000099", strings.Repeat("d", 64)
	rotated, err := NewBackendRegistration(i, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close()
	newMeta, _ := rotated.Metadata()
	if oldMeta.InstanceID == newMeta.InstanceID || oldMeta.KeyID == newMeta.KeyID {
		t.Fatal("fixture did not rotate backend authority")
	}
	again, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
	if err != nil || !reflect.DeepEqual(v, again) {
		t.Fatal("backend rotation changed stable viewer", err)
	}
	a.Role, a.SessionRevision = "admin", 5
	current, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
	if err != nil || current.Subject != v.Subject || !reflect.DeepEqual(current.OUs, v.OUs) || current.Role != "admin" || current.SessionRevision != 5 {
		t.Fatal("mutable authority changed identity or failed to update", err)
	}
	// Exercise the actual pinned request protocol with the new boot/key and stable
	// identity, rather than proving only a hash helper's internal behavior.
	signer, err := rotated.Signer("viewer-test-boot")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	now := time.Now()
	token, err := signer.Sign("GET", "/v1/notes", nil, current, now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: signer.BootID, Keys: map[string][]byte{signer.KeyID: signer.Key}, ReplayCapacity: 4})
	claims, err := verifier.Verify(token, "GET", "/v1/notes", nil, now)
	if err != nil || !reflect.DeepEqual(claims.Viewer, current) {
		t.Fatal("shared verifier identity mismatch", err)
	}
}

func TestViewerIdentityNamespacesAndPrincipalRecreation(t *testing.T) {
	for _, mode := range []string{"app", "installation", "seed", "account_created", "account_generation", "username", "ou_created", "ou_id", "domain"} {
		t.Run(mode, func(t *testing.T) {
			i := backendIdentityFixture()
			seed := bytes.Repeat([]byte{31}, 32)
			a := ViewerAccountIdentity{Name: "reader", Created: "created-1", Role: "user", Generation: 1}
			chain := []ViewerOUIdentity{{1, "created-1"}}
			before, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "app":
				i.AppID = "other-app"
			case "installation":
				i.InstallationID = "00000000-0000-4000-8000-000000000088"
			case "seed":
				seed[0]++
			case "account_created":
				a.Created = "created-2"
			case "account_generation":
				a.Generation++
			case "username":
				a.Name = "other"
			case "ou_created":
				chain[0].Created = "created-2"
			case "ou_id":
				chain[0].ID++
			}
			after, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "ou_created", "ou_id":
				if after.Subject != before.Subject || after.OUs[0] == before.OUs[0] {
					t.Fatal("OU recreation failed identity separation")
				}
			case "domain":
				if after.Subject == after.OUs[0] {
					t.Fatal("account/OU domains overlap")
				}
			default:
				if after.Subject == before.Subject {
					t.Fatal("principal or installation identity reused")
				}
				if (mode == "app" || mode == "installation" || mode == "seed") && after.OUs[0] == before.OUs[0] {
					t.Fatal("OU namespace reused")
				}
			}
			if len(after.Subject) != 64 || len(after.OUs[0]) != 64 || strings.Contains(after.Subject, a.Name) {
				t.Fatal("nonopaque identity")
			}
		})
	}
}

func TestViewerIdentityRefusesIncompleteFacts(t *testing.T) {
	for _, mode := range []string{"app", "installation", "zero_installation", "seed", "name", "created", "role", "generation", "revision", "missing_ou", "many_ous", "ou_created", "ou_id", "duplicate_ou", "utf8", "control"} {
		t.Run(mode, func(t *testing.T) {
			i := backendIdentityFixture()
			seed := bytes.Repeat([]byte{31}, 32)
			a := ViewerAccountIdentity{Name: "reader", Created: "created", Role: "user", Generation: 1}
			chain := []ViewerOUIdentity{{1, "created"}}
			switch mode {
			case "app":
				i.AppID = "bad/app"
			case "installation":
				i.InstallationID = "bad"
			case "zero_installation":
				i.InstallationID = "00000000-0000-0000-0000-000000000000"
			case "seed":
				seed = seed[:31]
			case "name":
				a.Name = ""
			case "created":
				a.Created = ""
			case "role":
				a.Role = strings.Repeat("r", 65)
			case "generation":
				a.Generation = -1
			case "revision":
				a.SessionRevision = -1
			case "missing_ou":
				chain = nil
			case "many_ous":
				chain = make([]ViewerOUIdentity, 65)
			case "ou_created":
				chain[0].Created = ""
			case "ou_id":
				chain[0].ID = 0
			case "duplicate_ou":
				chain = append(chain, chain[0])
			case "utf8":
				a.Name = string([]byte{255})
			case "control":
				chain[0].Created = "bad\ncreated"
			}
			v, err := DeriveViewerIdentity(i.AppID, i.InstallationID, seed, a, chain)
			if err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
				t.Fatal("invalid facts emitted a viewer")
			}
		})
	}
}
