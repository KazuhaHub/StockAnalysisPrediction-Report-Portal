package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExitStatusFollowsTheVerdict(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, strings.NewReader("not json"), &out, &errOut); code != 1 || !strings.Contains(out.String(), `"problem"`) {
		t.Fatalf("an unrecognised document: exit %d, %s", code, out.String())
	}
	if code := run([]string{"/nonexistent/inspect.json"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("a missing file: exit %d", code)
	}
	if code := run([]string{"a", "b"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("too many arguments: exit %d", code)
	}
}
