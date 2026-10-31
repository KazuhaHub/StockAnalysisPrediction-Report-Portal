package appplatform

import (
	"encoding/json"
	"regexp"
	"time"
)

const (
	MaxGlobalActiveTasks       = 10000
	MaxApplicationActiveTasks  = 1000
	MaxInstallationActiveTasks = 256
	MaxTaskResultBytes         = 8192
	MinTaskRetryDelay          = time.Second
	MaxTaskRetryDelay          = 6 * time.Hour
)

var taskResultCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,95}$`)

type TaskQuota struct {
	GlobalActive, ApplicationActive, InstallationActive uint64
}

func AdmitTask(quota TaskQuota) error {
	if quota.GlobalActive >= MaxGlobalActiveTasks || quota.ApplicationActive >= MaxApplicationActiveTasks || quota.InstallationActive >= MaxInstallationActiveTasks {
		return ErrTaskTransition
	}
	return nil
}

func TaskBackoff(attempt uint64) (time.Duration, error) {
	if attempt < 1 || attempt >= MaxTaskAttempts {
		return 0, ErrTaskTransition
	}
	delay := MinTaskRetryDelay
	for range attempt - 1 {
		if delay >= MaxTaskRetryDelay/2 {
			return MaxTaskRetryDelay, nil
		}
		delay *= 2
	}
	if delay > MaxTaskRetryDelay {
		delay = MaxTaskRetryDelay
	}
	return delay, nil
}

func TaskDeadline(started, leaseUntil time.Time, timeout time.Duration) (time.Time, error) {
	if started.IsZero() || leaseUntil.IsZero() || timeout < time.Second || timeout > MaxTaskLease || !started.Before(leaseUntil) {
		return time.Time{}, ErrTaskTransition
	}
	requested := started.Add(timeout)
	if requested.After(leaseUntil) {
		return leaseUntil, nil
	}
	return requested, nil
}

type TaskResultOutcome string

const (
	TaskResultSucceeded TaskResultOutcome = "succeeded"
	TaskResultRetry     TaskResultOutcome = "retry"
	TaskResultFailed    TaskResultOutcome = "failed"
	TaskResultUncertain TaskResultOutcome = "uncertain"
)

type TaskExecutionResult struct {
	Outcome     TaskResultOutcome
	ErrorCode   string
	RetryDelay  time.Duration
	ResultJSON  []byte
	EvidenceSHA string
}

type TaskResultDecision struct {
	State       TaskState
	RetryAt     time.Time
	ErrorCode   string
	ResultJSON  []byte
	EvidenceSHA string
}

// ClassifyTaskResult accepts only safe worker output. "Uncertain" never
// schedules an automatic retry, and an exhausted retry becomes failed.
func ClassifyTaskResult(result TaskExecutionResult, attempt, maxAttempts uint64, now time.Time) (TaskResultDecision, error) {
	var zero TaskResultDecision
	if now.IsZero() || attempt < 1 || maxAttempts < 1 || maxAttempts > MaxTaskAttempts || attempt > maxAttempts {
		return zero, ErrTaskTransition
	}
	if result.ErrorCode != "" && !taskResultCodePattern.MatchString(result.ErrorCode) {
		return zero, ErrTaskTransition
	}
	if result.EvidenceSHA != "" && !digestPattern.MatchString(result.EvidenceSHA) {
		return zero, ErrTaskTransition
	}
	if len(result.ResultJSON) > MaxTaskResultBytes || len(result.ResultJSON) > 0 && !json.Valid(result.ResultJSON) {
		return zero, ErrTaskTransition
	}
	decision := TaskResultDecision{ErrorCode: result.ErrorCode, ResultJSON: append([]byte(nil), result.ResultJSON...), EvidenceSHA: result.EvidenceSHA}
	switch result.Outcome {
	case TaskResultSucceeded:
		decision.State = TaskSucceeded
	case TaskResultFailed:
		decision.State = TaskFailed
	case TaskResultUncertain:
		decision.State = TaskUncertain
		decision.ResultJSON = nil
	case TaskResultRetry:
		if result.RetryDelay < MinTaskRetryDelay || result.RetryDelay > MaxTaskRetryDelay {
			return zero, ErrTaskTransition
		}
		if attempt >= maxAttempts {
			decision.State = TaskFailed
		} else {
			decision.State = TaskWaiting
			decision.RetryAt = now.Add(result.RetryDelay)
		}
	default:
		return zero, ErrTaskTransition
	}
	return decision, nil
}
