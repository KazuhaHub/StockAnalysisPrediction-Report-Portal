package driver

import (
	"errors"
	"net/http"
	"regexp"
)

// ErrDockerRequestRefused is returned for a request outside the closed set below. It is the SEC-01 boundary: no
// code path can pull an image, build, commit, push, or touch networks and volumes, whatever calls the client.
var ErrDockerRequestRefused = errors.New("docker daemon request is outside the runner allow-list")

const dockerRequestHost = "app-runner-docker.invalid"

type dockerRoute struct {
	method string
	path   *regexp.Regexp
}

var (
	dockerContainerID = `[a-f0-9]{64}`
	dockerImageID     = `sha256:[a-f0-9]{64}`
	// A local reference (development profile only) is addressed by an escaped digest reference. The segment is
	// bounded and can never be "create", "load" or "json" followed by a further segment.
	dockerImageRef = `[A-Za-z0-9._:%@-]{1,640}`
)

// dockerAllowedRoutes is the complete set of daemon requests the runner issues. There is deliberately
// no image pull route, no registry, build, commit, push, network, volume or exec route.
var dockerAllowedRoutes = []dockerRoute{
	{http.MethodGet, regexp.MustCompile(`^/version$`)},
	{http.MethodGet, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/info$`)},
	{http.MethodGet, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/images/json$`)},
	{http.MethodGet, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/images/(` + dockerImageID + `|` + dockerImageRef + `)/json$`)},
	{http.MethodPost, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/images/load$`)},
	{http.MethodDelete, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/images/` + dockerImageID + `$`)},
	{http.MethodGet, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/containers/json$`)},
	{http.MethodGet, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/containers/` + dockerContainerID + `/(json|logs)$`)},
	{http.MethodPost, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/containers/create$`)},
	{http.MethodPost, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/containers/` + dockerContainerID + `/(start|stop|kill)$`)},
	{http.MethodDelete, regexp.MustCompile(`^/v` + regexp.QuoteMeta(APIVersion) + `/containers/` + dockerContainerID + `$`)},
}

// dockerRequestAllowed reports whether a method and escaped path are in the closed set.
func dockerRequestAllowed(method, escapedPath string) bool {
	for _, route := range dockerAllowedRoutes {
		if route.method == method && route.path.MatchString(escapedPath) {
			return true
		}
	}
	return false
}

// allowListTransport refuses every request outside the closed set before it reaches the daemon socket.
type allowListTransport struct{ next http.RoundTripper }

func (t allowListTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.Scheme != "http" || request.URL.Host != dockerRequestHost ||
		!dockerRequestAllowed(request.Method, request.URL.EscapedPath()) {
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, ErrDockerRequestRefused
	}
	return t.next.RoundTrip(request)
}
