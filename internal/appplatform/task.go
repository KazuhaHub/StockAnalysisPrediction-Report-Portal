package appplatform

import (
	"errors"
	"math"
	"time"
)

var ErrTaskTransition = errors.New("application task transition unavailable")

type TaskState string

const (
	TaskWaiting        TaskState = "waiting"
	TaskLeased         TaskState = "leased"
	TaskRunning        TaskState = "running"
	TaskSucceeded      TaskState = "succeeded"
	TaskFailed         TaskState = "failed"
	TaskCancelled      TaskState = "cancelled"
	TaskUncertain      TaskState = "uncertain"
	TaskReviewRequired TaskState = "review_required"
)

type TaskSnapshot struct {
	State       TaskState
	Revision    uint64
	Attempt     uint64
	MaxAttempts uint64
	Fence       uint64
	LeaseUntil  time.Time
}

const (
	MaxTaskAttempts = 5
	MinTaskLease    = 30 * time.Second
	MaxTaskLease    = 10 * time.Minute
	MaxTaskBackoff  = 24 * time.Hour
)

func taskTerminal(state TaskState) bool {
	return state == TaskSucceeded || state == TaskFailed || state == TaskCancelled
}

func validTaskSnapshot(s TaskSnapshot) bool {
	if s.Revision < 1 || s.MaxAttempts < 1 || s.MaxAttempts > MaxTaskAttempts || s.Attempt > s.MaxAttempts {
		return false
	}
	switch s.State {
	case TaskWaiting, TaskLeased, TaskRunning, TaskSucceeded, TaskFailed, TaskCancelled, TaskUncertain, TaskReviewRequired:
		return true
	default:
		return false
	}
}

func ClaimTask(current TaskSnapshot, worker string, now time.Time, lease time.Duration) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || worker == "" || now.IsZero() || lease < MinTaskLease || lease > MaxTaskLease || current.Revision == math.MaxUint64 || current.Attempt >= current.MaxAttempts || current.Fence == math.MaxUint64 {
		return TaskSnapshot{}, ErrTaskTransition
	}
	if current.State != TaskWaiting || !current.LeaseUntil.IsZero() {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskLeased
	current.Revision++
	current.Attempt++
	current.Fence++
	current.LeaseUntil = now.Add(lease)
	return current, nil
}

func StartTask(current TaskSnapshot, now time.Time) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || current.State != TaskLeased || current.Revision == math.MaxUint64 || current.LeaseUntil.IsZero() || !now.Before(current.LeaseUntil) {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskRunning
	current.Revision++
	return current, nil
}

func CompleteTask(current TaskSnapshot, now time.Time) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || current.State != TaskRunning || current.Revision == math.MaxUint64 || current.LeaseUntil.IsZero() || !now.Before(current.LeaseUntil) {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskSucceeded
	current.Revision++
	current.LeaseUntil = time.Time{}
	return current, nil
}

func FailTask(current TaskSnapshot, retryAt, now time.Time) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || current.State != TaskRunning || current.Revision == math.MaxUint64 || current.LeaseUntil.IsZero() || !now.Before(current.LeaseUntil) {
		return TaskSnapshot{}, ErrTaskTransition
	}
	if current.Attempt < current.MaxAttempts {
		if retryAt.IsZero() || retryAt.Before(now) || retryAt.Sub(now) > MaxTaskBackoff {
			return TaskSnapshot{}, ErrTaskTransition
		}
		current.State = TaskWaiting
		current.LeaseUntil = time.Time{}
	} else {
		current.State = TaskFailed
		current.LeaseUntil = time.Time{}
	}
	current.Revision++
	return current, nil
}

// MarkTaskUncertain stops automatic retry. A later explicit reconciliation may
// choose success/failure or review; uncertainty itself never becomes a retry.
func MarkTaskUncertain(current TaskSnapshot, now time.Time) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || (current.State != TaskLeased && current.State != TaskRunning) || current.Revision == math.MaxUint64 || !now.Before(current.LeaseUntil) {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskUncertain
	current.Revision++
	current.LeaseUntil = time.Time{}
	return current, nil
}

func RestoreTask(current TaskSnapshot) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || taskTerminal(current.State) || current.Revision == math.MaxUint64 {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskReviewRequired
	current.Revision++
	current.LeaseUntil = time.Time{}
	return current, nil
}

func CancelTask(current TaskSnapshot) (TaskSnapshot, error) {
	if !validTaskSnapshot(current) || taskTerminal(current.State) || current.State == TaskUncertain || current.Revision == math.MaxUint64 {
		return TaskSnapshot{}, ErrTaskTransition
	}
	current.State = TaskCancelled
	current.Revision++
	current.LeaseUntil = time.Time{}
	return current, nil
}
