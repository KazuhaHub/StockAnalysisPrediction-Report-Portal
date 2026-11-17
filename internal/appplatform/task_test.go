package appplatform

import (
	"errors"
	"testing"
	"time"
)

func taskFixture() (TaskSnapshot, time.Time) {
	now := time.UnixMilli(1700000000000)
	return TaskSnapshot{State: TaskWaiting, Revision: 1, MaxAttempts: 3}, now
}

func TestTaskLeaseStateMachine(t *testing.T) {
	current, now := taskFixture()
	leased, err := ClaimTask(current, "worker-one", now, time.Minute)
	if err != nil || leased.State != TaskLeased || leased.Attempt != 1 || leased.Fence != 1 || leased.Revision != 2 || !leased.LeaseUntil.Equal(now.Add(time.Minute)) {
		t.Fatal("claim", leased, err)
	}
	running, err := StartTask(leased, now.Add(time.Second))
	if err != nil || running.State != TaskRunning || running.Revision != 3 || running.Fence != leased.Fence {
		t.Fatal("start", running, err)
	}
	firstFail, err := FailTask(running, now.Add(20*time.Second), now.Add(2*time.Second))
	if err != nil || firstFail.State != TaskWaiting || firstFail.Attempt != 1 || firstFail.LeaseUntil != (time.Time{}) {
		t.Fatal("retry", firstFail, err)
	}
	second, _ := ClaimTask(firstFail, "worker-two", now.Add(20*time.Second), time.Minute)
	second, _ = StartTask(second, now.Add(21*time.Second))
	finalFail, err := FailTask(second, now.Add(40*time.Second), now.Add(22*time.Second))
	if err != nil || finalFail.State != TaskWaiting || finalFail.Attempt != 2 {
		t.Fatal("second retry", finalFail, err)
	}
	third, _ := ClaimTask(finalFail, "worker-three", now.Add(40*time.Second), time.Minute)
	third, _ = StartTask(third, now.Add(41*time.Second))
	exhausted, err := FailTask(third, now.Add(time.Minute), now.Add(42*time.Second))
	if err != nil || exhausted.State != TaskFailed || exhausted.Attempt != 3 {
		t.Fatal("exhaustion", exhausted, err)
	}
}

func TestTaskRefusesExpiredAndConcurrentTransitions(t *testing.T) {
	current, now := taskFixture()
	leased, err := ClaimTask(current, "worker-one", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimTask(leased, "worker-two", now, time.Minute); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("double claim accepted")
	}
	if _, err := StartTask(leased, now.Add(time.Minute)); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("expired lease started")
	}
	running, _ := StartTask(leased, now.Add(time.Second))
	if _, err := CompleteTask(running, now.Add(time.Minute)); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("expired completion accepted")
	}
	if _, err := CompleteTask(running, now.Add(2*time.Second)); err != nil {
		t.Fatal("completion", err)
	}
	if _, err := CancelTask(mustComplete(t, running, now.Add(2*time.Second))); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("terminal task cancelled")
	}
}

func TestTaskUncertaintyAndRestoreDoNotAutoRetry(t *testing.T) {
	current, now := taskFixture()
	leased, _ := ClaimTask(current, "worker-one", now, time.Minute)
	running, _ := StartTask(leased, now.Add(time.Second))
	uncertain, err := MarkTaskUncertain(running, now.Add(2*time.Second))
	if err != nil || uncertain.State != TaskUncertain || !uncertain.LeaseUntil.IsZero() {
		t.Fatal("uncertain", uncertain, err)
	}
	if _, err := RestoreTask(uncertain); err != nil {
		t.Fatal("uncertain restore review", err)
	}
	review, _ := RestoreTask(uncertain)
	if review.State != TaskReviewRequired {
		t.Fatal("restore state", review)
	}
	if _, err := ClaimTask(review, "worker-two", now.Add(3*time.Second), time.Minute); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("review task auto-retried")
	}
	if _, err := ClaimTask(uncertain, "worker-two", now.Add(3*time.Second), time.Minute); !errors.Is(err, ErrTaskTransition) {
		t.Fatal("uncertain task auto-retried")
	}
}

func mustComplete(t *testing.T, running TaskSnapshot, now time.Time) TaskSnapshot {
	t.Helper()
	done, err := CompleteTask(running, now)
	if err != nil {
		t.Fatal(err)
	}
	return done
}
