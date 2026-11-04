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
		Handler:               appplatform.TaskHandlerBackend,
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

func TestServiceBackendTaskAcceptsUpdateGenerationRegistration(t *testing.T) {
	registrationCommand, key, authority, now, descriptor, document :=
		updateStartRegistrationFixture(t)
	defer clear(document)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	registrationRaw, registrationSignature, err :=
		SignUpdateStartRegistration(registrationCommand, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := VerifyUpdateStartRegistration(
		registrationRaw, registrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	start := registration.Start().Command()
	taskID := "10000000-0000-4000-8000-000000000031"
	body, err := appplatform.EncodeTaskBackendRequest(
		taskID, "digest:generation-2",
		[]byte(`{"scheduleId":"daily","reportIds":[41]}`))
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "10000000-0000-4000-8000-000000000032"
	identity, err := appplatform.SignServiceIdentity(
		[]byte(strings.Repeat("u", 32)),
		appplatform.ServiceIdentityGrant{
			Format:          appplatform.ServiceIdentityFormat,
			LeaseID:         leaseID,
			AppID:           parsed.Manifest.ID,
			InstallationID:  start.InstallationID,
			ControllerEpoch: start.ControllerEpoch,
			Generation:      start.TargetGeneration,
			PolicyRevision:  start.Payload.TargetPolicyRevision,
			ReleaseSHA256:   start.Payload.TargetReleaseSHA256,
			Resources:       []string{"reports.read", "tasks.schedule"},
			IssuedMS:        strconv.FormatInt(now, 10),
			ExpiresMS:       strconv.FormatInt(now+60000, 10),
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	command := ServiceBackendTaskCommand{
		Format:                ServiceBackendTaskFormat,
		RequestID:             "10000000-0000-4000-8000-000000000033",
		TaskID:                taskID,
		LeaseID:               leaseID,
		Handler:               appplatform.TaskHandlerDigest,
		InstallationID:        start.InstallationID,
		Generation:            start.TargetGeneration,
		PolicyRevision:        start.Payload.TargetPolicyRevision,
		ControllerEpoch:       start.ControllerEpoch,
		ReleaseSHA256:         start.Payload.TargetReleaseSHA256,
		BootID:                "generation-2-boot",
		RegistrationRaw:       registrationRaw,
		RegistrationSignature: registrationSignature,
		ServiceIdentity:       identity,
		BodySHA256:            serviceTaskBodyDigest(body),
	}
	raw, signature, err := SignServiceBackendTask(
		command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
	if err != nil ||
		verified.RegistrationIdentity().Generation != "2" ||
		verified.RegistrationIdentity().PolicyRevision != "2" ||
		ValidateServiceBackendTaskData(verified, body) != nil {
		t.Fatal("generation-2 service task", verified, err)
	}
	for name, mutate := range map[string]func(*ServiceBackendTaskCommand){
		"generation": func(c *ServiceBackendTaskCommand) {
			c.Generation = "1"
		},
		"policy": func(c *ServiceBackendTaskCommand) {
			c.PolicyRevision = "1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if raw, signature, err := SignServiceBackendTask(
				changed, key, authority, now); err == nil ||
				len(raw) != 0 || len(signature) != 0 {
				t.Fatal("changed generation-2 service task signed")
			}
		})
	}
}

func TestServiceEventTaskRequiresEventSubscriptionAuthority(t *testing.T) {
	base, key, authority, now, _, _ := backendRequestFixture(t)
	registration, err := VerifyBackendRegistration(
		base.RegistrationRaw, base.RegistrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	body, _, err := appplatform.BuildEventDelivery(
		"10000000-0000-4000-8000-000000000010",
		"10000000-0000-4000-8000-000000000011",
		appplatform.EventReportIngested, "report", "41", now,
		[]byte(`{"id":41}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := func(resources []string) []byte {
		raw, err := appplatform.SignServiceIdentity(
			[]byte(strings.Repeat("e", 32)),
			appplatform.ServiceIdentityGrant{
				Format:          appplatform.ServiceIdentityFormat,
				LeaseID:         "10000000-0000-4000-8000-000000000012",
				AppID:           "notes-sample",
				InstallationID:  candidate.InstallationID,
				ControllerEpoch: candidate.ControllerEpoch,
				Generation:      candidate.TargetGeneration,
				PolicyRevision:  "1",
				ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
				Resources:       resources,
				IssuedMS:        strconv.FormatInt(now, 10),
				ExpiresMS:       strconv.FormatInt(now+60000, 10),
			}, now)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	command := ServiceBackendTaskCommand{
		Format:                ServiceBackendTaskFormat,
		RequestID:             "10000000-0000-4000-8000-000000000013",
		TaskID:                "10000000-0000-4000-8000-000000000011",
		LeaseID:               "10000000-0000-4000-8000-000000000012",
		Handler:               appplatform.TaskHandlerEvent,
		InstallationID:        candidate.InstallationID,
		Generation:            candidate.TargetGeneration,
		PolicyRevision:        "1",
		ControllerEpoch:       candidate.ControllerEpoch,
		ReleaseSHA256:         candidate.Payload.ReleaseSHA256,
		BootID:                base.BootID,
		RegistrationRaw:       base.RegistrationRaw,
		RegistrationSignature: base.RegistrationSignature,
		ServiceIdentity:       identity([]string{"events.subscribe", "tasks.schedule"}),
		BodySHA256:            serviceTaskBodyDigest(body),
	}
	raw, signature, err := SignServiceBackendTask(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
	if err != nil || ValidateServiceBackendTaskData(verified, body) != nil {
		t.Fatal("event task", verified, err)
	}
	command.ServiceIdentity = identity([]string{"tasks.schedule"})
	if _, _, err := SignServiceBackendTask(command, key, authority, now); err == nil {
		t.Fatal("event task without events.subscribe was signed")
	}
}

func TestServiceDigestTaskUsesFixedTaskBodyAndReportAuthority(t *testing.T) {
	base, key, authority, now, _, _ := backendRequestFixture(t)
	registration, err := VerifyBackendRegistration(
		base.RegistrationRaw, base.RegistrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := registration.Candidate()
	taskID := "10000000-0000-4000-8000-000000000021"
	body, err := appplatform.EncodeTaskBackendRequest(
		taskID, "digest:2026-11-03",
		[]byte(`{"scheduleId":"daily","count":2}`))
	if err != nil {
		t.Fatal(err)
	}
	identity := func(resources []string) []byte {
		raw, err := appplatform.SignServiceIdentity(
			[]byte(strings.Repeat("d", 32)),
			appplatform.ServiceIdentityGrant{
				Format:          appplatform.ServiceIdentityFormat,
				LeaseID:         "10000000-0000-4000-8000-000000000022",
				AppID:           "scheduled-digest",
				InstallationID:  candidate.InstallationID,
				ControllerEpoch: candidate.ControllerEpoch,
				Generation:      candidate.TargetGeneration,
				PolicyRevision:  "1",
				ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
				Resources:       resources,
				IssuedMS:        strconv.FormatInt(now, 10),
				ExpiresMS:       strconv.FormatInt(now+60000, 10),
			}, now)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	command := ServiceBackendTaskCommand{
		Format:                ServiceBackendTaskFormat,
		RequestID:             "10000000-0000-4000-8000-000000000023",
		TaskID:                taskID,
		LeaseID:               "10000000-0000-4000-8000-000000000022",
		Handler:               appplatform.TaskHandlerDigest,
		InstallationID:        candidate.InstallationID,
		Generation:            candidate.TargetGeneration,
		PolicyRevision:        "1",
		ControllerEpoch:       candidate.ControllerEpoch,
		ReleaseSHA256:         candidate.Payload.ReleaseSHA256,
		BootID:                base.BootID,
		RegistrationRaw:       base.RegistrationRaw,
		RegistrationSignature: base.RegistrationSignature,
		ServiceIdentity: identity(
			[]string{"reports.read", "tasks.schedule"}),
		BodySHA256: serviceTaskBodyDigest(body),
	}
	raw, signature, err := SignServiceBackendTask(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
	if err != nil || ValidateServiceBackendTaskData(verified, body) != nil {
		t.Fatal("digest task", verified, err)
	}
	if path, ok := appplatform.TaskHandlerPath(
		appplatform.TaskHandlerDigest); !ok ||
		path != appplatform.TaskDigestPath {
		t.Fatal("digest route", path, ok)
	}
	command.ServiceIdentity = identity([]string{"tasks.schedule"})
	if _, _, err := SignServiceBackendTask(command, key, authority, now); err == nil {
		t.Fatal("digest task without reports.read was signed")
	}
}
