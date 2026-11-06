package appplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func serviceIdentityFixture(now int64) ServiceIdentityGrant {
	return ServiceIdentityGrant{
		Format: ServiceIdentityFormat, LeaseID: "10000000-0000-4000-8000-000000000001",
		AppID: "notes-sample", InstallationID: "20000000-0000-4000-8000-000000000001",
		ControllerEpoch: "30000000-0000-4000-8000-000000000001", Generation: "2", PolicyRevision: "3",
		ReleaseSHA256: strings.Repeat("a", 64), Resources: []string{"reports.read", "network.http"},
		IssuedMS: "1700000000000", ExpiresMS: "1700000005000",
	}
}

func TestServiceIdentityBindsCurrentApplicationWithoutUser(t *testing.T) {
	now := int64(1700000000000)
	seed := bytes.Repeat([]byte{7}, 32)
	grant := serviceIdentityFixture(now)
	raw, err := SignServiceIdentity(seed, grant, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range grant.Resources {
		verified, err := VerifyServiceIdentity(raw, seed, ServiceIdentityGrant{
			Format: grant.Format, LeaseID: grant.LeaseID, AppID: grant.AppID,
			InstallationID: grant.InstallationID, ControllerEpoch: grant.ControllerEpoch,
			Generation: grant.Generation, PolicyRevision: grant.PolicyRevision, ReleaseSHA256: grant.ReleaseSHA256,
		}, resource, now+1000)
		if err != nil || verified.AppID != grant.AppID || verified.LeaseID != grant.LeaseID {
			t.Fatal("service identity", resource, verified, err)
		}
	}
	text := string(raw)
	for _, forbidden := range []string{"username", "account_id", "viewer", "role", "session"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatal("service identity retained user authority field", forbidden)
		}
	}
}

func TestServiceIdentityRefusesScopeTupleAndTimeDrift(t *testing.T) {
	now := int64(1700000000000)
	seed := bytes.Repeat([]byte{7}, 32)
	grant := serviceIdentityFixture(now)
	raw, err := SignServiceIdentity(seed, grant, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ServiceIdentityGrant){
		"installation": func(g *ServiceIdentityGrant) { g.InstallationID = "20000000-0000-4000-8000-000000000002" },
		"generation":   func(g *ServiceIdentityGrant) { g.Generation = "3" },
		"policy":       func(g *ServiceIdentityGrant) { g.PolicyRevision = "4" },
		"release":      func(g *ServiceIdentityGrant) { g.ReleaseSHA256 = strings.Repeat("b", 64) },
		"epoch":        func(g *ServiceIdentityGrant) { g.ControllerEpoch = "30000000-0000-4000-8000-000000000002" },
		"lease":        func(g *ServiceIdentityGrant) { g.LeaseID = "10000000-0000-4000-8000-000000000002" },
	} {
		t.Run(name, func(t *testing.T) {
			expected := serviceIdentityFixture(now)
			mutate(&expected)
			if _, err := VerifyServiceIdentity(raw, seed, expected, "reports.read", now); !errors.Is(err, ErrServiceIdentity) {
				t.Fatal("drifted tuple accepted")
			}
		})
	}
	if _, err := VerifyServiceIdentity(raw, seed, grant, "storage.files", now); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("unbound resource accepted")
	}
	if _, err := VerifyServiceIdentity(raw, seed, grant, "reports.read", now+5000); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("expired service identity accepted")
	}
	if _, err := VerifyServiceIdentity(raw, bytes.Repeat([]byte{8}, 32), grant, "reports.read", now); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("wrong installation seed accepted")
	}
}

func TestServiceIdentityRefusesTamperedOrOpenPayloads(t *testing.T) {
	now := int64(1700000000000)
	seed := bytes.Repeat([]byte{7}, 32)
	grant := serviceIdentityFixture(now)
	raw, err := SignServiceIdentity(seed, grant, now)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte(`"generation":"2"`), []byte(`"generation":"3"`), 1)
	if _, err := VerifyServiceIdentity(tampered, seed, ServiceIdentityGrant{}, "reports.read", now); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("tampered service identity accepted")
	}
	open := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"username":"admin"}`)...)
	if _, err := VerifyServiceIdentity(open, seed, ServiceIdentityGrant{}, "reports.read", now); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("open service identity accepted")
	}
	duplicate := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"generation":"2"}`)...)
	if _, err := VerifyServiceIdentity(duplicate, seed, ServiceIdentityGrant{}, "reports.read", now); !errors.Is(err, ErrServiceIdentity) {
		t.Fatal("duplicate service field accepted")
	}
	bad := grant
	bad.Resources = []string{"reports.read", "reports.read"}
	if _, err := SignServiceIdentity(seed, bad, now); err == nil {
		t.Fatal("duplicate resource signed")
	}
	bad = grant
	bad.ExpiresMS = "1700000300001"
	if _, err := SignServiceIdentity(seed, bad, now); err == nil {
		t.Fatal("overlong lease signed")
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("service fixture")
	}
}
