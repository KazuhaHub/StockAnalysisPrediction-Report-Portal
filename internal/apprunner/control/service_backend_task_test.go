package control

import (
	"crypto/ed25519"
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

func writableServiceTaskFixture(t *testing.T, handler string, policy string, resources []string) (ServiceBackendTaskCommand, ed25519.PrivateKey, Authority, int64, []byte) {
	t.Helper()
	request, key, authority, now, _, _ := writableBackendRequestFixture(t)
	registration, err := VerifyWritableBackendRegistration(
		request.RegistrationRaw, request.RegistrationSignature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	index := registration.Candidate().Index()
	taskID := "10000000-0000-4000-8000-000000000041"
	var body []byte
	if handler == appplatform.TaskHandlerEvent {
		body, _, err = appplatform.BuildEventDelivery(
			"10000000-0000-4000-8000-000000000042", taskID,
			appplatform.EventReportIngested, "report", "41", now,
			[]byte(`{"id":41}`))
	} else {
		body, err = appplatform.EncodeTaskBackendRequest(
			taskID, "writable:1", []byte(`{"fixture":true}`))
	}
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "10000000-0000-4000-8000-000000000043"
	identity, err := appplatform.SignServiceIdentity(
		[]byte(strings.Repeat("w", 32)), appplatform.ServiceIdentityGrant{
			Format: appplatform.ServiceIdentityFormat, LeaseID: leaseID,
			AppID: "notes-demo", InstallationID: index.InstallationID,
			ControllerEpoch: index.ControllerEpoch, Generation: "1",
			PolicyRevision: policy, ReleaseSHA256: index.Payload.ReleaseSHA256,
			Resources: resources, IssuedMS: strconv.FormatInt(now, 10),
			ExpiresMS: strconv.FormatInt(now+60000, 10),
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	return ServiceBackendTaskCommand{
		Format:    ServiceBackendTaskFormat,
		RequestID: "10000000-0000-4000-8000-000000000044", TaskID: taskID,
		LeaseID: leaseID, Handler: handler,
		InstallationID: index.InstallationID, Generation: "1",
		PolicyRevision: policy, ControllerEpoch: index.ControllerEpoch,
		ReleaseSHA256: index.Payload.ReleaseSHA256, BootID: request.BootID,
		RegistrationRaw:       request.RegistrationRaw,
		RegistrationSignature: request.RegistrationSignature,
		ServiceIdentity:       identity, BodySHA256: serviceTaskBodyDigest(body),
	}, key, authority, now, body
}

func TestServiceBackendTaskAcceptsWritableInitialRegistration(t *testing.T) {
	for _, policy := range []string{"1", "4"} {
		command, key, authority, now, body := writableServiceTaskFixture(
			t, appplatform.TaskHandlerBackend, policy, []string{"tasks.schedule"})
		raw, signature, err := SignServiceBackendTask(command, key, authority, now)
		if err != nil {
			t.Fatal(policy, err)
		}
		verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
		if err != nil || ValidateServiceBackendTaskData(verified, body) != nil {
			t.Fatal("writable task", verified, err)
		}
		identity := verified.RegistrationIdentity()
		registration, ok := verified.WritableRegistration()
		if !ok || identity.Format != WritableBackendRegistrationFormat ||
			identity.Generation != "1" || identity.PolicyRevision != policy ||
			identity.InstallationID != registration.Candidate().Index().InstallationID {
			t.Fatal("writable identity", identity, ok)
		}
		if _, ok := verified.InitialRegistration(); ok {
			t.Fatal("writable task exposed the managed registration")
		}
		if _, ok := verified.UpdateStartRegistration(); ok {
			t.Fatal("writable task exposed the update registration")
		}
	}
}

func TestServiceBackendTaskWritableRegistrationRefusesMismatches(t *testing.T) {
	command, key, authority, now, _ := writableServiceTaskFixture(
		t, appplatform.TaskHandlerBackend, "1", []string{"tasks.schedule"})
	for name, mutate := range map[string]func(*ServiceBackendTaskCommand){
		"installation": func(c *ServiceBackendTaskCommand) {
			c.InstallationID = "90000000-0000-4000-8000-000000000005"
		},
		"generation": func(c *ServiceBackendTaskCommand) { c.Generation = "2" },
		"epoch": func(c *ServiceBackendTaskCommand) {
			c.ControllerEpoch = "90000000-0000-4000-8000-000000000006"
		},
		"release": func(c *ServiceBackendTaskCommand) {
			c.ReleaseSHA256 = strings.Repeat("b", 64)
		},
		"policy vs grant": func(c *ServiceBackendTaskCommand) { c.PolicyRevision = "2" },
		"policy zero":     func(c *ServiceBackendTaskCommand) { c.PolicyRevision = "0" },
		"policy leading zero": func(c *ServiceBackendTaskCommand) {
			c.PolicyRevision = "01"
		},
		"registration signature": func(c *ServiceBackendTaskCommand) {
			c.RegistrationSignature = append([]byte(nil), c.RegistrationSignature...)
			c.RegistrationSignature[0] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if raw, signature, err := SignServiceBackendTask(
				changed, key, authority, now); err == nil ||
				len(raw) != 0 || len(signature) != 0 {
				t.Fatal("mismatched writable task was signed")
			}
		})
	}
	// A stale registration (past its expiry) is refused.
	raw, signature, err := SignServiceBackendTask(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyServiceBackendTask(
		raw, signature, authority, now+10*24*3600*1000); err == nil {
		t.Fatal("expired writable registration accepted")
	}
}

func TestServiceBackendTaskWritableHandlersRequireGrantResources(t *testing.T) {
	for handler, resource := range map[string]string{
		appplatform.TaskHandlerDigest: "reports.read",
		appplatform.TaskHandlerEvent:  "events.subscribe",
	} {
		command, key, authority, now, _ := writableServiceTaskFixture(
			t, handler, "1", []string{resource, "tasks.schedule"})
		if _, _, err := SignServiceBackendTask(command, key, authority, now); err != nil {
			t.Fatal(handler, err)
		}
		command, key, authority, now, _ = writableServiceTaskFixture(
			t, handler, "1", []string{"tasks.schedule"})
		if _, _, err := SignServiceBackendTask(command, key, authority, now); err == nil {
			t.Fatal(handler, "signed without", resource)
		}
	}
}

// An access-grant change bumps the policy revision without starting a new
// generation, so a generation-1 managed install must keep accepting tasks at
// the grant's current revision.
func TestServiceBackendTaskManagedInitialAcceptsPolicyRevisionAfterGrantChange(t *testing.T) {
	command, _, _, key, authority, now, body := serviceBackendTaskFixture(t)
	for _, policy := range []string{"1", "2", "9"} {
		identity, err := appplatform.SignServiceIdentity(
			[]byte(strings.Repeat("s", 32)), appplatform.ServiceIdentityGrant{
				Format:          appplatform.ServiceIdentityFormat,
				LeaseID:         command.LeaseID,
				AppID:           "notes-sample",
				InstallationID:  command.InstallationID,
				ControllerEpoch: command.ControllerEpoch,
				Generation:      "1",
				PolicyRevision:  policy,
				ReleaseSHA256:   command.ReleaseSHA256,
				Resources:       []string{"tasks.schedule"},
				IssuedMS:        strconv.FormatInt(now, 10),
				ExpiresMS:       strconv.FormatInt(now+60000, 10),
			}, now)
		if err != nil {
			t.Fatal(err)
		}
		changed := command
		changed.PolicyRevision = policy
		changed.ServiceIdentity = identity
		raw, signature, err := SignServiceBackendTask(changed, key, authority, now)
		if err != nil {
			t.Fatal("policy revision", policy, err)
		}
		verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
		if err != nil || verified.RegistrationIdentity().PolicyRevision != policy ||
			ValidateServiceBackendTaskData(verified, body) != nil {
			t.Fatal("managed task at policy revision", policy, err)
		}
	}
	// The command and the grant still have to agree, and the revision must be canonical.
	for name, policy := range map[string]string{"zero": "0", "leading zero": "02", "empty": "", "signed": "+2"} {
		changed := command
		changed.PolicyRevision = policy
		if _, _, err := SignServiceBackendTask(changed, key, authority, now); err == nil {
			t.Fatal("managed task signed with a", name, "policy revision")
		}
	}
	changed := command
	changed.PolicyRevision = "2"
	if _, _, err := SignServiceBackendTask(changed, key, authority, now); err == nil {
		t.Fatal("managed task signed with a policy revision its grant does not carry")
	}
}

// An updated, restored or resumed app is at generation 2 or later. An access-grant
// change bumps its policy revision without a new generation, so the task of such an
// app must be accepted at the grant's current revision, never below the revision the
// update was started with.
func TestServiceBackendTaskUpdateGenerationAcceptsPolicyRevisionAfterGrantChange(t *testing.T) {
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
	floor := start.Payload.TargetPolicyRevision
	taskID := "10000000-0000-4000-8000-000000000051"
	body, err := appplatform.EncodeTaskBackendRequest(
		taskID, "generation-2:grant-change", []byte(`{"fixture":true}`))
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "10000000-0000-4000-8000-000000000052"
	build := func(policy string) (ServiceBackendTaskCommand, bool) {
		identity, err := appplatform.SignServiceIdentity(
			[]byte(strings.Repeat("u", 32)), appplatform.ServiceIdentityGrant{
				Format: appplatform.ServiceIdentityFormat, LeaseID: leaseID,
				AppID: parsed.Manifest.ID, InstallationID: start.InstallationID,
				ControllerEpoch: start.ControllerEpoch,
				Generation:      start.TargetGeneration, PolicyRevision: policy,
				ReleaseSHA256: start.Payload.TargetReleaseSHA256,
				Resources:     []string{"tasks.schedule"},
				IssuedMS:      strconv.FormatInt(now, 10),
				ExpiresMS:     strconv.FormatInt(now+60000, 10),
			}, now)
		if err != nil {
			// The grant itself refuses a non-canonical revision.
			return ServiceBackendTaskCommand{}, false
		}
		return ServiceBackendTaskCommand{
			Format:    ServiceBackendTaskFormat,
			RequestID: "10000000-0000-4000-8000-000000000053", TaskID: taskID,
			LeaseID: leaseID, Handler: appplatform.TaskHandlerBackend,
			InstallationID: start.InstallationID, Generation: start.TargetGeneration,
			PolicyRevision: policy, ControllerEpoch: start.ControllerEpoch,
			ReleaseSHA256: start.Payload.TargetReleaseSHA256, BootID: "generation-2-boot",
			RegistrationRaw: registrationRaw, RegistrationSignature: registrationSignature,
			ServiceIdentity: identity, BodySHA256: serviceTaskBodyDigest(body),
		}, true
	}
	n, err := strconv.ParseUint(floor, 10, 63)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{floor, strconv.FormatUint(n+1, 10), strconv.FormatUint(n+7, 10)} {
		command, ok := build(policy)
		if !ok {
			t.Fatal("grant refused revision", policy)
		}
		raw, signature, err := SignServiceBackendTask(command, key, authority, now)
		if err != nil {
			t.Fatal("generation-2 task at policy revision", policy, err)
		}
		verified, err := VerifyServiceBackendTask(raw, signature, authority, now)
		if err != nil || verified.RegistrationIdentity().PolicyRevision != policy ||
			verified.RegistrationIdentity().Generation != "2" ||
			ValidateServiceBackendTaskData(verified, body) != nil {
			t.Fatal("generation-2 task verification", policy, err)
		}
	}
	// Below the revision the update started with, or not canonical: refused.
	for name, policy := range map[string]string{"below the floor": strconv.FormatUint(n-1, 10), "leading zero": "0" + floor, "empty": ""} {
		command, ok := build(policy)
		if !ok {
			continue
		}
		if _, _, err := SignServiceBackendTask(command, key, authority, now); err == nil {
			t.Fatal("generation-2 task signed with a policy revision", name)
		}
	}
}

// The registration's own generation is checked, not only the command's. The signed
// chain already refuses another generation, so the guard is tested directly on the
// projection a verified registration yields.
func TestServiceBackendTaskWritableIdentityRequiresGenerationOne(t *testing.T) {
	c, key, a, now := writableRegistrationFixture(t)
	raw, signature, err := SignWritableBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyWritableBackendRegistration(raw, signature, a, now)
	if err != nil {
		t.Fatal(err)
	}
	index := verified.Candidate().Index()
	if identity, err := writableServiceTaskIdentity(index, "1"); err != nil || identity.Generation != "1" {
		t.Fatal("generation-1 registration refused", err)
	}
	for _, generation := range []string{"0", "2", "", "01"} {
		changed := index
		changed.TargetGeneration = generation
		if _, err := writableServiceTaskIdentity(changed, "1"); err == nil {
			t.Fatal("registration at generation", generation, "yielded a task identity")
		}
	}
}
