package broker

import (
	"bytes"
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// CurrentNetworkAuthority must load current controller enrollment and verify the
// command's actor, installation, release, generation, exact reviewed grant and
// revision. It is an online authority boundary, not a cached signature check.
type CurrentNetworkAuthority func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error)

// NetworkCall owns one signed foreground invocation against a shared executor.
// Single use is local to this object; it is not durable replay/idempotency proof.
// A future authenticated transport must never recreate/retry an uncertain call.
// The executor owner, not each request, owns concurrency allocation and lifetime.
type NetworkCall struct {
	used                 atomic.Bool
	executor             *HTTP
	raw, signature, body []byte
	verified             control.VerifiedNetworkHTTPRequest
	current              CurrentNetworkAuthority
	expires              time.Time
	now                  func() time.Time
}

func (n *NetworkCall) Close() {
	if n == nil {
		return
	}
	clear(n.body)
	clear(n.raw)
	clear(n.signature)
	n.verified.Clear()
}

func NewNetworkCall(h *HTTP, raw, signature, body []byte, a control.Authority, current CurrentNetworkAuthority) (*NetworkCall, error) {
	if h == nil || h.authorize == nil || current == nil {
		return nil, ErrDenied
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return nil, ErrDenied
	}
	command := verified.Command()
	selected, err := appplatform.CanonicalHTTPPolicy(h.policy)
	if err != nil || !bytes.Equal(selected, command.PolicyRaw) {
		clear(command.CredentialValue)
		return nil, ErrDenied
	}
	clear(command.CredentialValue)
	expires, err := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if err != nil {
		return nil, ErrDenied
	}
	return &NetworkCall{executor: h, raw: bytes.Clone(raw), signature: bytes.Clone(signature), body: bytes.Clone(body), verified: verified, current: current, expires: time.UnixMilli(expires), now: time.Now}, nil
}

func (n *NetworkCall) Do(parent context.Context) (HTTPResponse, error) {
	if n == nil || n.executor == nil || n.current == nil || parent == nil || !n.used.CompareAndSwap(false, true) {
		return HTTPResponse{}, ErrDenied
	}
	defer n.Close()
	ctx, cancel := context.WithDeadline(parent, n.expires)
	defer cancel()
	c := n.verified.Command()
	defer clear(c.CredentialValue)
	check := func(ctx context.Context) error {
		current := n.verified.Command()
		defer clear(current.CredentialValue)
		a, err := n.current(ctx, current)
		if err != nil {
			return ErrDenied
		}
		if _, err := control.VerifyNetworkHTTPRequest(n.raw, n.signature, a, n.now().UnixMilli()); err != nil {
			return ErrDenied
		}
		return nil
	}
	return n.executor.do(ctx, HTTPRequest{Method: c.Method, URL: c.URL, Accept: c.Accept, ContentType: c.ContentType, Body: n.body, HeaderName: c.CredentialHeader, HeaderValue: c.CredentialValue}, check)
}
