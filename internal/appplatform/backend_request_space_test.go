package appplatform

import (
	"testing"
	"time"
)

// The reserved route spaces are judged on the decoded path, so an encoded or dot-segment spelling
// can never put a token of one kind into another's space.
func TestBackendRequestSpacesAreJudgedOnTheDecodedPath(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	body := []byte(`{"a":1}`)
	ing, err := SignIngressAssertion(signer, 1, "heartbeat", "api", "/v1/ingress/heartbeat", now, "3f9c0000000000000000", body, now)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := SignPublicAssertion(signer, 1, "status", "/v1/public/status", "", now)
	if err != nil {
		t.Fatal(err)
	}
	const viewer = "abc.def"
	reserved := map[string][]string{
		"ingress": {"/v1/%69ngress/x", "/v1/ingres%73/x", "/v1/ingress%2fx", "/v1/%2569ngress/x", "/v1/./ingress/x", "/v1/x/../ingress/x", "/v1//ingress/x", "/v1/ingress//x", "/v1/ingress/./x", "/v1/ingress/%2e%2e/ingress/x", "/v1/ingress", "/v1/ingress/x/", "/v1/INGRESS/x", "/v1/Ingress/x", "/v1/%49NGRESS/x", "/v1/ingress;x/y", "/v1/ingress/x;y", "/v1/ingress%3bx/y"},
		"public":  {"/v1/%70ublic/x", "/v1/publi%63/x", "/v1/public%2fx", "/v1/%2570ublic/x", "/v1/./public/x", "/v1/x/../public/x", "/v1//public/x", "/v1/public//x", "/v1/public/./x", "/v1/public/%2e%2e/public/x", "/v1/public", "/v1/PUBLIC/x", "/v1/Public/x", "/v1/public;x/y", "/v1/public/x;y", "/v1/public%3bx/y"},
	}
	for space, paths := range reserved {
		for _, path := range paths {
			// A viewer token never reaches a reserved space, however it is spelled.
			if ValidBackendRequestData("GET", path, nil, viewer) || ValidBackendRequestData("POST", path, body, viewer) {
				t.Fatalf("viewer token accepted on %s", path)
			}
			// Neither does a token of the other kind.
			if space == "ingress" && ValidBackendRequestData("GET", path, nil, pub) {
				t.Fatalf("public token accepted on %s", path)
			}
			if space == "public" && ValidBackendRequestData("POST", path, body, ing) {
				t.Fatalf("ingress token accepted on %s", path)
			}
		}
	}
	// A token of the space's own kind takes only the canonical spelling: no encoding, no dot segment.
	own := []string{"/v1/ingress/x%41", "/v1/ingress/x%2fy", "/v1/ingress/./x", "/v1/ingress//x", "/v1/ingress/../ingress/x", "/v1/%69ngress/x", "/v1/ingress%2fx", "/v1/INGRESS/x", "/v1/ingress/x;y", "/v1/ingress;x/y"}
	for _, path := range own {
		if ValidBackendRequestData("POST", path, body, ing) {
			t.Fatalf("ingress token accepted on %s", path)
		}
	}
	ownPublic := []string{"/v1/public/x%41", "/v1/public/x%2fy", "/v1/public/./x", "/v1/public//x", "/v1/public/../public/x", "/v1/%70ublic/x", "/v1/public%2fx", "/v1/PUBLIC/x", "/v1/public/x;y", "/v1/public;x/y"}
	for _, path := range ownPublic {
		if ValidBackendRequestData("GET", path, nil, pub) {
			t.Fatalf("public token accepted on %s", path)
		}
	}
	// The canonical spellings still work, and a viewer token still reaches ordinary routes (even encoded).
	if !ValidBackendRequestData("POST", "/v1/ingress/heartbeat", body, ing) || !ValidBackendRequestData("GET", "/v1/public/status?limit=1", nil, pub) ||
		!ValidBackendRequestData("GET", "/v1/notes/a%20b", nil, viewer) || !ValidBackendRequestData("GET", "/v1/notes", nil, viewer) {
		t.Fatal("a canonical request was refused")
	}
	// A look-alike that is not the reserved segment is ordinary.
	if !ValidBackendRequestData("GET", "/v1/ingress-log", nil, viewer) || !ValidBackendRequestData("GET", "/v1/publication", nil, viewer) {
		t.Fatal("a sibling of a reserved name was refused")
	}
}

// A semicolon is never part of a backend request path: some backends treat it as a path parameter
// separator, so it could hide a reserved segment.
func TestBackendRequestPathsRefuseSemicolons(t *testing.T) {
	for _, path := range []string{"/v1/notes;a=b", "/v1/notes/x;y", "/v1/no%3btes", "/v1/notes;/x"} {
		if ValidBackendRequestData("GET", path, nil, "abc.def") {
			t.Fatalf("accepted %s", path)
		}
	}
}
