package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestFixtureDigestHandlerBindsTaskAndServiceResources(t *testing.T) {
	now := time.Now().UnixMilli()
	identity := appplatform.ServiceIdentityGrant{
		Format:          appplatform.ServiceIdentityFormat,
		LeaseID:         "10000000-0000-4000-8000-000000000031",
		AppID:           "scheduled-digest",
		InstallationID:  "10000000-0000-4000-8000-000000000032",
		ControllerEpoch: "10000000-0000-4000-8000-000000000033",
		Generation:      "1", PolicyRevision: "1",
		ReleaseSHA256: strings.Repeat("a", 64),
		Resources:     []string{"reports.read", "tasks.schedule"},
		IssuedMS:      strconv.FormatInt(now, 10),
		ExpiresMS:     strconv.FormatInt(now+60000, 10),
		Mac:           strings.Repeat("b", 64),
	}
	rawIdentity, _ := json.Marshal(identity)
	if !fixtureDigestIdentity(string(rawIdentity), appplatform.BackendIdentity{
		AppID: identity.AppID, InstallationID: identity.InstallationID,
		ControllerEpoch: identity.ControllerEpoch, Generation: identity.Generation,
		PolicyRevision: identity.PolicyRevision, ReleaseSHA256: identity.ReleaseSHA256,
	}) {
		t.Fatal("valid digest identity refused")
	}
	identity.Resources = []string{"tasks.schedule"}
	changed, _ := json.Marshal(identity)
	if fixtureDigestIdentity(string(changed), appplatform.BackendIdentity{
		AppID: identity.AppID, InstallationID: identity.InstallationID,
		ControllerEpoch: identity.ControllerEpoch, Generation: identity.Generation,
		PolicyRevision: identity.PolicyRevision, ReleaseSHA256: identity.ReleaseSHA256,
	}) {
		t.Fatal("digest identity without reports.read passed")
	}
}
