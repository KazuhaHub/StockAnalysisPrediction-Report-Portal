package app

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// A scheduler that fails the same way every tick logs that state once, and again only when it
// changes: to another error, or back to success.
func TestPlatformTickLogReportsChangesOnly(t *testing.T) {
	var l platformTickLog
	other := errors.New("database is locked")
	var got []string
	for _, err := range []error{nil, errAppPlatformControlPaused, errAppPlatformControlPaused, fmt.Errorf("wrapped: %w", errAppPlatformControlPaused),
		other, other, nil, nil, errAppPlatformControlPaused} {
		if line, ok := l.report(err); ok {
			got = append(got, line)
		}
	}
	want := []string{
		"app-platform scheduler tick: " + errAppPlatformControlPaused.Error() + " (repeats are not logged until the state changes)",
		"app-platform scheduler tick: wrapped: " + errAppPlatformControlPaused.Error() + " (repeats are not logged until the state changes)",
		"app-platform scheduler tick: database is locked (repeats are not logged until the state changes)",
		"app-platform scheduler tick: recovered",
		"app-platform scheduler tick: " + errAppPlatformControlPaused.Error() + " (repeats are not logged until the state changes)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logged:\n%q\nwant:\n%q", got, want)
	}
	// An error with no text is still a failure, and its end is still a recovery.
	var blank platformTickLog
	if line, ok := blank.report(errors.New("")); !ok || line == "app-platform scheduler tick: recovered" {
		t.Fatalf("an empty error was not logged: %q", line)
	}
	if line, ok := blank.report(nil); !ok || line != "app-platform scheduler tick: recovered" {
		t.Fatalf("recovery after an empty error: %q %v", line, ok)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logged:\n%q\nwant:\n%q", got, want)
	}
}
