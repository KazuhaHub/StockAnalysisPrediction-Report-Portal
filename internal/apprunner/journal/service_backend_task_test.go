package journal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestServiceBackendTaskResultIsBoundAndBounded(t *testing.T) {
	result := ServiceBackendTaskResult{
		Format:                ServiceBackendTaskResultFormat,
		RequestID:             fixtureID(701),
		TaskID:                fixtureID(702),
		LeaseID:               fixtureID(703),
		RequestSHA256:         strings.Repeat("a", 64),
		DeliveryReceiptSHA256: strings.Repeat("b", 64),
		NativeID:              strings.Repeat("c", 64),
		BootID:                "boot",
		Response: driver.BackendResponse{
			Status: 200,
			Body:   []byte(`{"format":"portal.app-task.response/1-dev.1","outcome":"succeeded","result":{"count":1}}`),
		},
		ObservedMS: "1",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseServiceBackendTaskResult(raw)
	if err != nil || parsed.TaskID != result.TaskID ||
		parsed.RequestID != result.RequestID {
		t.Fatal("result parse", parsed, err)
	}
	for _, bad := range [][]byte{
		nil,
		append(append([]byte(nil), raw...), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"task_id":"`+result.TaskID+`"`, `"task_id":""`, 1)),
		[]byte(strings.Replace(string(raw), `"status":200`, `"status":302`, 1)),
		[]byte(strings.Replace(string(raw), `"observed_ms":"1"`, `"observed_ms":"01"`, 1)),
	} {
		if _, err := ParseServiceBackendTaskResult(bad); err == nil {
			t.Fatal("invalid service task result accepted")
		}
	}
}
