package driver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func limitsClient(status int, body string, seen *string) *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		if seen != nil {
			*seen = r.Method + " " + r.URL.Path
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	}))
}

func TestReadContainerLimitsReadsTheDeclaredLimits(t *testing.T) {
	id := strings.Repeat("a", 64)
	var seen string
	got, err := readContainerLimits(context.Background(), limitsClient(200, `{"Id":"`+id+`","HostConfig":{"Memory":268435456,"NanoCpus":500000000}}`, &seen), id)
	if err != nil || !got.Found || got.MemoryBytes != 268435456 || got.NanoCPUs != 500000000 {
		t.Fatalf("limits = %+v %v", got, err)
	}
	if seen != "GET /v"+APIVersion+"/containers/"+id+"/json" {
		t.Fatalf("request = %s", seen)
	}
}

func TestReadContainerLimitsDistinguishesAbsentFromUnavailable(t *testing.T) {
	id := strings.Repeat("b", 64)
	got, err := readContainerLimits(context.Background(), limitsClient(404, `{}`, nil), id)
	if err != nil || got.Found {
		t.Fatalf("absent container: %+v %v", got, err)
	}
	if _, err := readContainerLimits(context.Background(), limitsClient(500, `{}`, nil), id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("daemon error: %v", err)
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := readContainerLimits(context.Background(), limitsClient(200, `{}`, nil), bad); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("id %q accepted: %v", bad, err)
		}
	}
	// A reply for another container is refused, never trusted.
	if _, err := readContainerLimits(context.Background(), limitsClient(200, `{"Id":"`+strings.Repeat("c", 64)+`","HostConfig":{"Memory":1}}`, nil), id); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("foreign container: %v", err)
	}
}
