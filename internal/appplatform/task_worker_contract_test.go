package appplatform

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTaskAdmissionAndBackoffAreBounded(t *testing.T) {
	for _, quota := range []TaskQuota{
		{GlobalActive: MaxGlobalActiveTasks},
		{ApplicationActive: MaxApplicationActiveTasks},
		{InstallationActive: MaxInstallationActiveTasks},
	} {
		if err := AdmitTask(quota); !errors.Is(err, ErrTaskTransition) {
			t.Fatal("quota exceeded", quota)
		}
	}
	if err := AdmitTask(TaskQuota{}); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for index, delay := range want {
		got, err := TaskBackoff(uint64(index + 1))
		if err != nil || got != delay {
			t.Fatal("backoff", index+1, got, err)
		}
	}
	if _, err := TaskBackoff(0); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("zero attempt acquired backoff")
	}
	if _, err := TaskBackoff(MaxTaskAttempts); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("exhausted attempt acquired retry")
	}
}

func TestTaskDeadlineNeverExceedsLease(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	deadline, err := TaskDeadline(now, now.Add(time.Minute), 20*time.Second)
	if err != nil || !deadline.Equal(now.Add(20*time.Second)) {
		t.Fatal("deadline", deadline, err)
	}
	deadline, err = TaskDeadline(now, now.Add(time.Minute), 2*time.Minute)
	if err != nil || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatal("lease cap", deadline, err)
	}
	if _, err := TaskDeadline(now, now, time.Second); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("expired lease accepted")
	}
}

func TestTaskResultClassificationStopsUncertainRetry(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	success, err := ClassifyTaskResult(TaskExecutionResult{Outcome: TaskResultSucceeded, ResultJSON: []byte(`{"ok":true}`), EvidenceSHA: strings.Repeat("a", 64)}, 1, 3, now)
	if err != nil || success.State != TaskSucceeded || string(success.ResultJSON) != `{"ok":true}` {
		t.Fatal("success", success, err)
	}
	retry, err := ClassifyTaskResult(TaskExecutionResult{Outcome: TaskResultRetry, ErrorCode: "provider_unavailable", RetryDelay: 10 * time.Second}, 1, 3, now)
	if err != nil || retry.State != TaskWaiting || !retry.RetryAt.Equal(now.Add(10*time.Second)) {
		t.Fatal("retry", retry, err)
	}
	exhausted, err := ClassifyTaskResult(TaskExecutionResult{Outcome: TaskResultRetry, ErrorCode: "provider_unavailable", RetryDelay: time.Second}, 3, 3, now)
	if err != nil || exhausted.State != TaskFailed || !exhausted.RetryAt.IsZero() {
		t.Fatal("exhaustion", exhausted, err)
	}
	uncertain, err := ClassifyTaskResult(TaskExecutionResult{Outcome: TaskResultUncertain, ErrorCode: "effect_unknown", ResultJSON: []byte(`{"private":"detail"}`)}, 1, 3, now)
	if err != nil || uncertain.State != TaskUncertain || len(uncertain.ResultJSON) != 0 || !uncertain.RetryAt.IsZero() {
		t.Fatal("uncertain became retry or released result", uncertain, err)
	}
	for _, bad := range []TaskExecutionResult{
		{Outcome: TaskResultRetry, RetryDelay: 0},
		{Outcome: TaskResultRetry, RetryDelay: MaxTaskRetryDelay + time.Second},
		{Outcome: TaskResultSucceeded, ErrorCode: "Private Error"},
		{Outcome: TaskResultSucceeded, EvidenceSHA: "bad"},
		{Outcome: TaskResultSucceeded, ResultJSON: []byte(`{"value":`)},
		{Outcome: "invented"},
	} {
		if _, err := ClassifyTaskResult(bad, 1, 3, now); !errors.Is(err, ErrTaskTransition) {
			t.Fatal("unsafe task result accepted", bad)
		}
	}
}
