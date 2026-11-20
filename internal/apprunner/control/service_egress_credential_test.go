package control

import (
	"strings"
	"testing"
)

// secrets.use joins a session only beside network.http: a credential is placed
// into a network request, so a session that cannot make one has no use for it.
func TestServiceEgressSessionSecretsUseNeedsNetwork(t *testing.T) {
	command, key, authority, now := serviceEgressFixture(t)
	for _, c := range []struct {
		resources []string
		ok        bool
	}{
		{[]string{"network.http", "notifications.publish", "secrets.use"}, true},
		{[]string{"network.http", "secrets.use"}, true},
		{[]string{"notifications.publish", "secrets.use"}, false},
		{[]string{"secrets.use"}, false},
		{[]string{"network.http", "secrets.use", "secrets.use"}, false},
		{[]string{"secrets.use", "network.http"}, false},
		{[]string{"network.http", "secrets.read"}, false},
	} {
		edited := command
		edited.Resources = c.resources
		raw, signature, err := SignServiceEgressSession(edited, key, authority, now)
		if (err == nil) != c.ok {
			t.Fatalf("%s: signed=%v", strings.Join(c.resources, ","), err == nil)
		}
		if err != nil {
			continue
		}
		verified, err := VerifyServiceEgressSession(raw, signature, authority, now)
		if err != nil || strings.Join(verified.Command().Resources, ",") != strings.Join(c.resources, ",") {
			t.Fatal("verify", err)
		}
	}
}
