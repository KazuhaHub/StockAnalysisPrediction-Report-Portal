package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func platformObjectRequestFixture(action string, changes map[string]any) []byte {
	request := map[string]any{
		"binding": map[string]string{
			"generation":     "1",
			"policyRevision": "1",
			"releaseSha256":  strings.Repeat("a", 64),
		},
		"action": action,
	}
	switch action {
	case "list":
		request["collection"] = "note"
	case "get":
		request["id"] = "00000000-0000-0000-0000-000000000042"
	case "create":
		request["requestId"] = "create-42"
		request["collection"] = "note"
		request["label"] = "First note"
		request["mediaType"] = "application/json"
		request["contentBase64"] = base64.StdEncoding.EncodeToString([]byte(`{"body":"hello"}`))
	case "update":
		request["id"] = "00000000-0000-0000-0000-000000000042"
		request["expectedRevision"] = 2
		request["label"] = "Updated note"
		request["mediaType"] = "application/json"
		request["contentBase64"] = base64.StdEncoding.EncodeToString([]byte(`{"body":"updated"}`))
	case "delete":
		request["id"] = "00000000-0000-0000-0000-000000000042"
		request["expectedRevision"] = 2
	}
	for key, value := range changes {
		if value == nil {
			delete(request, key)
		} else {
			request[key] = value
		}
	}
	raw, _ := json.Marshal(request)
	return raw
}

func TestPlatformObjectRequestContract(t *testing.T) {
	list, err := parsePlatformObjectRequest(platformObjectRequestFixture("list", nil))
	if err != nil || list.action != "list" || list.collection != "note" || list.limit != 20 {
		t.Fatal("valid list request", list, err)
	}
	created, err := parsePlatformObjectRequest(platformObjectRequestFixture("create", nil))
	if err != nil || created.action != "create" || created.requestID != "create-42" || string(created.content) != `{"body":"hello"}` {
		t.Fatal("valid create request", created, err)
	}
	updated, err := parsePlatformObjectRequest(platformObjectRequestFixture("update", nil))
	if err != nil || updated.action != "update" || updated.expectedRevision != 2 || string(updated.content) != `{"body":"updated"}` {
		t.Fatal("valid update request", updated, err)
	}
	deleted, err := parsePlatformObjectRequest(platformObjectRequestFixture("delete", nil))
	if err != nil || deleted.action != "delete" || deleted.id != "00000000-0000-0000-0000-000000000042" {
		t.Fatal("valid delete request", deleted, err)
	}
	exact := platformObjectRequestFixture("create", map[string]any{"contentBase64": base64.StdEncoding.EncodeToString(make([]byte, maxPlatformObjectBytes))})
	if _, err := parsePlatformObjectRequest(exact); err != nil {
		t.Fatal("exact object byte limit refused", err)
	}
}

func TestPlatformObjectRequestRejectsClosedShapes(t *testing.T) {
	content := base64.StdEncoding.EncodeToString([]byte("body"))
	list := platformObjectRequestFixture("list", nil)
	duplicate := bytes.Replace(list, []byte(`"action":"list"`), []byte(`"action":"list","action":"get"`), 1)
	cases := []struct {
		name string
		raw  []byte
	}{
		{"unknown", platformObjectRequestFixture("list", map[string]any{"extra": true})},
		{"duplicate key", duplicate},
		{"trailing json", append(append([]byte{}, list...), []byte(`{}`)...)},
		{"missing action", platformObjectRequestFixture("", map[string]any{"action": nil})},
		{"missing binding", platformObjectRequestFixture("list", map[string]any{"binding": nil})},
		{"list create field", platformObjectRequestFixture("list", map[string]any{"requestId": "create-42"})},
		{"create list field", platformObjectRequestFixture("create", map[string]any{"cursor": "1:00000000-0000-0000-0000-000000000001"})},
		{"get missing id", platformObjectRequestFixture("get", map[string]any{"id": nil})},
		{"delete zero revision", platformObjectRequestFixture("delete", map[string]any{"expectedRevision": 0})},
		{"bad collection", platformObjectRequestFixture("create", map[string]any{"collection": "Note"})},
		{"bad label control", platformObjectRequestFixture("create", map[string]any{"label": "line\nbreak"})},
		{"bad media type", platformObjectRequestFixture("create", map[string]any{"mediaType": "text"})},
		{"bad base64", platformObjectRequestFixture("create", map[string]any{"contentBase64": "***"})},
		{"noncanonical base64", platformObjectRequestFixture("create", map[string]any{"contentBase64": "YQ"})},
		{"oversized content", platformObjectRequestFixture("create", map[string]any{"contentBase64": base64.StdEncoding.EncodeToString(make([]byte, maxPlatformObjectBytes+1))})},
		{"bad cursor", platformObjectRequestFixture("list", map[string]any{"cursor": "not-a-cursor"})},
		{"oversized limit", platformObjectRequestFixture("list", map[string]any{"limit": 51})},
		{"bad generation", platformObjectRequestFixture("list", map[string]any{"binding": map[string]string{"generation": "0", "policyRevision": "1", "releaseSha256": strings.Repeat("a", 64)}})},
		{"oracle fields", platformObjectRequestFixture("get", map[string]any{"contentBase64": content})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePlatformObjectRequest(tc.raw); !errors.Is(err, errPlatformObjectInvalid) {
				t.Fatalf("invalid request accepted: %v", err)
			}
		})
	}
}

func TestPlatformObjectQuotaContract(t *testing.T) {
	limit := platformObjectQuota{
		global:  platformObjectUsage{MaxBytes: 100, MaxObjects: 4},
		plugin:  platformObjectUsage{MaxBytes: 80, MaxObjects: 3},
		account: platformObjectUsage{MaxBytes: 50, MaxObjects: 2},
	}
	if err := admitPlatformObjectQuota(limit, 10, 1, 0, 0); err != nil {
		t.Fatal("admissible create", err)
	}
	for _, scope := range []string{"account", "plugin", "global"} {
		t.Run(scope, func(t *testing.T) {
			quota := limit
			switch scope {
			case "account":
				quota.account.Objects = 2
			case "plugin":
				quota.plugin.Objects = 3
			case "global":
				quota.global.Objects = 4
			}
			if err := admitPlatformObjectQuota(quota, 1, 1, 0, 0); !errors.Is(err, errPlatformObjectStorageFull) {
				t.Fatalf("%s quota limit was not enforced: %v", scope, err)
			}
		})
	}
	used := limit
	used.global.Bytes, used.global.Objects = 10, 1
	used.plugin.Bytes, used.plugin.Objects = 10, 1
	used.account.Bytes, used.account.Objects = 10, 1
	if err := admitPlatformObjectQuota(used, 0, 0, 10, 1); err != nil {
		t.Fatal("delete did not release quota", err)
	}
	if err := admitPlatformObjectQuota(limit, 61, 1, 0, 0); !errors.Is(err, errPlatformObjectStorageFull) {
		t.Fatalf("replacement byte limit was not enforced: %v", err)
	}
	if err := admitPlatformObjectQuota(limit, -1, 1, 0, 0); !errors.Is(err, errPlatformObjectInvalid) {
		t.Fatalf("negative admission accepted: %v", err)
	}
}

func TestPlatformObjectAccountAndRevisionContract(t *testing.T) {
	owner := platformObjectOwner{Username: "reader", AccountCreated: "100", AccountGeneration: "9007199254740993"}
	if !samePlatformObjectOwner(owner, owner) {
		t.Fatal("exact account identity did not match")
	}
	for _, changed := range []platformObjectOwner{
		{Username: "reader", AccountCreated: "101", AccountGeneration: owner.AccountGeneration},
		{Username: "reader", AccountCreated: owner.AccountCreated, AccountGeneration: "9007199254740994"},
	} {
		if samePlatformObjectOwner(owner, changed) {
			t.Fatal("deleted or recreated account inherited object ownership")
		}
	}
	if err := checkPlatformObjectRevision(3, 3); err != nil {
		t.Fatal("current revision refused", err)
	}
	if err := checkPlatformObjectRevision(3, 0); !errors.Is(err, errPlatformObjectInvalid) {
		t.Fatalf("invalid revision accepted: %v", err)
	}
	for _, expected := range []int64{2, 4} {
		if err := checkPlatformObjectRevision(3, expected); !errors.Is(err, errPlatformObjectConflict) {
			t.Fatalf("stale revision %d accepted: %v", expected, err)
		}
	}
}
