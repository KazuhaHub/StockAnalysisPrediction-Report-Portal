package appplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskBackendRequestIsClosedAndCanonical(t *testing.T) {
	raw, err := EncodeTaskBackendRequest(
		"10000000-0000-4000-8000-000000000001",
		"digest:2026-11-03", []byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeTaskBackendRequest(raw)
	if err != nil || request.TaskID != "10000000-0000-4000-8000-000000000001" ||
		request.OccurrenceID != "digest:2026-11-03" ||
		string(request.Payload) != `{"a":1,"b":2}` {
		t.Fatalf("request = %+v err=%v", request, err)
	}
	for _, bad := range [][]byte{
		append(append([]byte(nil), raw...), []byte("{}")...),
		bytes.Replace(raw, []byte(`"task_id"`), []byte(`"Task_ID"`), 1),
		bytes.Replace(raw, []byte(`"digest:2026-11-03"`), []byte(`"bad occurrence"`), 1),
		bytes.Replace(raw, []byte(`"payload":{"a":1,"b":2}`), []byte(`"payload":{"b":2,"a":1}`), 1),
	} {
		if _, err := DecodeTaskBackendRequest(bad); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("invalid task backend request accepted")
		}
	}
}

func TestTaskBackendResponseOnlyReturnsSafeOutcome(t *testing.T) {
	for _, result := range []TaskExecutionResult{
		{Outcome: TaskResultSucceeded, ResultJSON: []byte(`{"count":2}`), EvidenceSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Outcome: TaskResultFailed, ErrorCode: "provider_rejected"},
		{Outcome: TaskResultRetry, ErrorCode: "provider_unavailable"},
		{Outcome: TaskResultUncertain, ErrorCode: "effect_unknown"},
	} {
		raw, err := EncodeTaskBackendResponse(result)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeTaskBackendResponse(raw)
		if err != nil || decoded.Outcome != result.Outcome ||
			decoded.ErrorCode != result.ErrorCode {
			t.Fatalf("decoded = %+v err=%v", decoded, err)
		}
		if result.Outcome == TaskResultUncertain && len(decoded.ResultJSON) != 0 {
			t.Fatal("uncertain response released result bytes")
		}
	}
	for _, result := range []TaskExecutionResult{
		{Outcome: TaskResultSucceeded, ResultJSON: []byte(`{"token":"secret"}`)},
		{Outcome: TaskResultSucceeded, ErrorCode: "Private Error"},
		{Outcome: TaskResultRetry, ErrorCode: "retry", ResultJSON: json.RawMessage(`{"url":"https://example.com"}`)},
	} {
		if _, err := EncodeTaskBackendResponse(result); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("unsafe task backend response was encoded")
		}
	}
	valid, err := EncodeTaskBackendResponse(TaskExecutionResult{
		Outcome: TaskResultSucceeded, ResultJSON: []byte(`{"count":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		append(append([]byte(nil), valid...), []byte("{}")...),
		bytes.Replace(valid, []byte(`"outcome"`), []byte(`"Outcome"`), 1),
		bytes.Replace(valid, []byte(`"succeeded"`), []byte(`"unknown"`), 1),
	} {
		if _, err := DecodeTaskBackendResponse(bad); !errors.Is(err, ErrTaskHandler) {
			t.Fatal("invalid task backend response accepted")
		}
	}
}
