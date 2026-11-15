package driver

import (
	"encoding/json"
	"testing"
)

// Every way the runner creates an app container carries the same log bound: 5 MiB times two files, 10 MiB.
func TestAppContainerLogsAreBoundedToTenMiB(t *testing.T) {
	plans := cpusetPlans(t, "")
	if len(plans) == 0 {
		t.Fatal("no plans")
	}
	for name, plan := range plans {
		var request containerCreate
		if err := json.Unmarshal(plan.body, &request); err != nil {
			t.Fatal(err)
		}
		log := request.HostConfig.LogConfig
		if log.Type != "json-file" || len(log.Config) != 2 || log.Config["max-size"] != "5m" || log.Config["max-file"] != "2" {
			t.Errorf("%s: log config = %+v, want json-file with max-size 5m and max-file 2", name, log)
		}
		if !validContainerPlan(plan) {
			t.Errorf("%s: the plan is not valid", name)
		}
	}
}

// A body with other log options is not the plan, even when its digest is recomputed to match.
func TestContainerPlanWithOtherLogOptionsIsRefused(t *testing.T) {
	for name, plan := range cpusetPlans(t, "") {
		var request containerCreate
		if err := json.Unmarshal(plan.body, &request); err != nil {
			t.Fatal(err)
		}
		request.HostConfig.LogConfig.Config = map[string]string{"max-size": "1g", "max-file": "9"}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		plan.body, plan.bodySHA = body, containerHash(body)
		if validContainerPlan(plan) {
			t.Errorf("%s: a plan with unbounded logs was accepted", name)
		}
	}
}
