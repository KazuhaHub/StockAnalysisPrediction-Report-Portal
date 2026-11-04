package control

import (
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func serviceBackendTaskFixture(t *testing.T) (ServiceBackendTaskCommand, []byte, []byte, []byte, Authority, int64, []byte) {
	t.Helper()
	base, key, authority, now, _, _ := backendRequestFixture(t)
	registration, err := VerifyBackendRegistration(base.RegistrationRaw, base.RegistrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	seed := []byte(strings.Repeat("s", 32))
	identity, err := appplatform.SignServiceIdentity(seed, appplatform.ServiceIdentityGrant{
		Format:          appplatform.ServiceIdentityFormat,
		LeaseID:         "10000000-0000-4000-8000-000000000001",
		AppID:           "notes-sample",
		InstallationID:  candidate.InstallationID,
		ControllerEpoch: candidate.ControllerEpoch,
		Generation:      candidate.TargetGeneration,
		PolicyRevision:  "1",
		ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
		Resources:       []string{"tasks.schedule"},
		IssuedMS:        strconv.FormatInt(now, 10),
		ExpiresMS:       strconv.FormatInt(now+60000, 10),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := appplatform.EncodeTaskBackendRequest(
		"90000000-0000-4000-8000-000000000003",
		"fixture:1", []byte(`{"fixture":true}`))
	if err != nil {
		t.Fatal(err)
	}
	command := ServiceBackendTaskCommand{
		Format:                ServiceBackendTaskFormat,
		RequestID:             "90000000-0000-4000-8000-000000000002",
		TaskID:                "90000000-0000-4000-8000-000000000003",
		LeaseID:               "10000000-0000-4000-8000-000000000001",
		InstallationID:        candidate.InstallationID,
		Generation:            candidate.TargetGeneration,
		PolicyRevision:        "1",
		ControllerEpoch:       candidate.ControllerEpoch,
		ReleaseSHA256:         candidate.Payload.ReleaseSHA256,
		BootID:                base.BootID,
		RegistrationRaw:       base.RegistrationRaw,
		RegistrationSignature: base.RegistrationSignature,
		ServiceIdentity:       identity,
		BodySHA256:            serviceTaskBodyDigest(body),
	}
	raw, signature, err := SignServiceBackendTask(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, signature, key, authority, now, body
}

func TestServiceBackendTaskBindsServiceIdentityAndFixedRoute(t *testing.T) {
	command, raw, signature, key, authority, now, body := serviceBackendTaskFixture(t)
	verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Command().TaskID != command.TaskID ||
		ValidateServiceBackendTaskData(verified, body) != nil {
		t.Fatal("service task command did not verify")
	}
	if !appplatform.ValidBackendRequestRoute("POST", appplatform.TaskBackendPath) {
		t.Fatal("fixed task route is invalid")
	}
	for name, mutate := range map[string]func(*ServiceBackendTaskCommand){
		"lease": func(c *ServiceBackendTaskCommand) {
			c.LeaseID = "10000000-0000-4000-8000-000000000004"
		},
		"installation": func(c *ServiceBackendTaskCommand) {
			c.InstallationID = "90000000-0000-4000-8000-000000000005"
		},
		"generation": func(c *ServiceBackendTaskCommand) {
			c.Generation = "2"
		},
		"release": func(c *ServiceBackendTaskCommand) {
			c.ReleaseSHA256 = strings.Repeat("b", 64)
		},
		"identity": func(c *ServiceBackendTaskCommand) {
			c.ServiceIdentity = append([]byte(nil), c.ServiceIdentity...)
			c.ServiceIdentity[len(c.ServiceIdentity)-2] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			raw, signature, err := SignServiceBackendTask(changed, key, authority, now)
			if err == nil || len(raw) != 0 || len(signature) != 0 {
				t.Fatal("invalid service task was signed")
			}
		})
	}
	if err := ValidateServiceBackendTaskData(verified, []byte(`{"changed":true}`)); err == nil {
		t.Fatal("body change passed service task validation")
	}
}
