package appplatform

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

const (
	TaskBackendRequestFormat  = "portal.app-task.request/1-dev.1"
	TaskBackendResponseFormat = "portal.app-task.response/1-dev.1"
	TaskServiceIdentityHeader = "X-Portal-Service-Identity"
	MaxTaskBackendBody        = 12288
)

type TaskBackendRequest struct {
	Format       string          `json:"format"`
	TaskID       string          `json:"task_id"`
	OccurrenceID string          `json:"occurrence_id"`
	Payload      json.RawMessage `json:"payload"`
}

func EncodeTaskBackendRequest(taskID, occurrenceID string, payload []byte) ([]byte, error) {
	canonical, _, err := ValidateTaskPayload(payload)
	if !taskUUID(taskID) || !TaskOccurrence(occurrenceID) || err != nil {
		return nil, ErrTaskHandler
	}
	raw, err := json.Marshal(TaskBackendRequest{
		Format: TaskBackendRequestFormat, TaskID: taskID,
		OccurrenceID: occurrenceID, Payload: canonical,
	})
	if err != nil || len(raw) > MaxTaskBackendBody {
		return nil, ErrTaskHandler
	}
	return raw, nil
}

func DecodeTaskBackendRequest(raw []byte) (TaskBackendRequest, error) {
	var request TaskBackendRequest
	if len(raw) == 0 || len(raw) > MaxTaskBackendBody ||
		strictTaskBackendJSON(raw, &request) != nil ||
		request.Format != TaskBackendRequestFormat ||
		!taskUUID(request.TaskID) || !TaskOccurrence(request.OccurrenceID) {
		return request, ErrTaskHandler
	}
	canonical, _, err := ValidateTaskPayload(request.Payload)
	if err != nil || !bytes.Equal(canonical, request.Payload) {
		return request, ErrTaskHandler
	}
	return request, nil
}

type TaskBackendResponse struct {
	Format         string            `json:"format"`
	Outcome        TaskResultOutcome `json:"outcome"`
	ErrorCode      string            `json:"error_code,omitempty"`
	Result         json.RawMessage   `json:"result,omitempty"`
	EvidenceSHA256 string            `json:"evidence_sha256,omitempty"`
}

func EncodeTaskBackendResponse(result TaskExecutionResult) ([]byte, error) {
	check := result
	if check.Outcome == TaskResultRetry {
		check.RetryDelay = MinTaskRetryDelay
	}
	decision, err := ClassifyTaskResult(check, 1, MaxTaskAttempts, time.Unix(1, 0))
	if err != nil {
		return nil, ErrTaskHandler
	}
	result.ResultJSON = decision.ResultJSON
	raw, err := json.Marshal(TaskBackendResponse{
		Format: TaskBackendResponseFormat, Outcome: result.Outcome,
		ErrorCode: result.ErrorCode, Result: result.ResultJSON,
		EvidenceSHA256: result.EvidenceSHA,
	})
	if err != nil || len(raw) > MaxTaskResultBytes {
		return nil, ErrTaskHandler
	}
	return raw, nil
}

func DecodeTaskBackendResponse(raw []byte) (TaskExecutionResult, error) {
	var response TaskBackendResponse
	if len(raw) == 0 || len(raw) > MaxTaskResultBytes ||
		strictTaskBackendJSON(raw, &response) != nil ||
		response.Format != TaskBackendResponseFormat {
		return TaskExecutionResult{}, ErrTaskHandler
	}
	result := TaskExecutionResult{
		Outcome: response.Outcome, ErrorCode: response.ErrorCode,
		ResultJSON:  append([]byte(nil), response.Result...),
		EvidenceSHA: response.EvidenceSHA256,
	}
	if result.Outcome == TaskResultRetry {
		result.RetryDelay = MinTaskRetryDelay
	}
	if _, err := ClassifyTaskResult(result, 1, MaxTaskAttempts, time.Unix(1, 0)); err != nil {
		return TaskExecutionResult{}, ErrTaskHandler
	}
	return result, nil
}

func TaskOccurrence(value string) bool {
	return len(value) >= 1 && len(value) <= 128 &&
		strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') &&
				!(r >= '0' && r <= '9') && !strings.ContainsRune("_.:-", r)
		}) < 0
}

func taskUUID(value string) bool {
	return backendUUID.MatchString(value)
}

func strictTaskBackendJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrTaskHandler
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrTaskHandler
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrTaskHandler
	}
	return nil
}
