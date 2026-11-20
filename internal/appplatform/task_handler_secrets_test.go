package appplatform

import "testing"

// Doc 176 gives the background egress session credential use and leaves tasks
// out: no task handler may ask for secrets.use, so a task's service identity
// can never carry it.
func TestNoTaskHandlerRequiresSecretsUse(t *testing.T) {
	for _, spec := range taskHandlers {
		for _, capability := range spec.RequiredCapabilities {
			if capability == "secrets.use" || capability == "secrets.read" {
				t.Fatalf("task handler %s requires %s", spec.ID, capability)
			}
		}
	}
}
