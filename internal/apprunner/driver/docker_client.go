package driver

import (
	"net/http"
	"time"
)

// newDockerClient is the only constructor of a Docker socket client. Every client it returns goes through the
// request allow-list, whatever the caller does with it, and the dialer is never referenced anywhere else. A
// clientTimeout of zero leaves the whole-request bound to the caller's context, as a streaming load needs.
func newDockerClient(socket string, headerTimeout, clientTimeout time.Duration) (*http.Client, func()) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: headerTimeout, DialContext: dockerDialer(socket)}
	client := &http.Client{Transport: allowListTransport{next: transport}, Timeout: clientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport.CloseIdleConnections
}
