package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// This file is the application-facing side of background egress: the HTTP
// server an application reaches on its broker socket (portal.broker/1). The
// socket is the identity. The installation, generation and session are the ones
// the runner bound the socket to; nothing in a request names them.

// egressServer serves one installation's broker socket for as long as the
// socket lives, across sessions.
type egressServer struct {
	server   *http.Server
	listener *egressLimitListener
}

func (s *egressServer) close() {
	_ = s.server.Close()
	_ = s.listener.Close()
}

func (r *egressRegistry) startServer(in *egressInstall) {
	// The raw listener is sniffed first: a PRXY0001 stream goes to the installation's proxy, and every
	// other connection is the HTTP server's. The peer check is applied by the sniffer, to every
	// connection before a byte is read, so the limit listener below needs none.
	sniffer := newEgressSniffListener(in)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		sniffer.run(&r.wg)
	}()
	limited := &egressLimitListener{Listener: sniffer, slots: make(chan struct{}, egressBrokerConnections)}
	server := &http.Server{
		Handler:           in,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      25 * time.Second,
		MaxHeaderBytes:    8192,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	// One request per connection keeps the connection budget a call budget.
	server.SetKeepAlivesEnabled(false)
	in.server = &egressServer{server: server, listener: limited}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		_ = server.Serve(limited)
	}()
}

// egressLimitListener admits at most len(slots) live connections, drops the rest
// at once (a client that cannot connect learns so immediately), and refuses any
// peer the installation's peer check does not accept before a byte is read.
type egressLimitListener struct {
	net.Listener
	peerOK func(net.Conn) bool
	slots  chan struct{}
}

func (l *egressLimitListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.peerOK != nil && !l.peerOK(conn) {
			_ = conn.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return &egressLimitConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type egressLimitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *egressLimitConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func writeAppJSON(w http.ResponseWriter, status int, body []byte, retryAfter string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeAppError(w http.ResponseWriter, code string) {
	writeAppJSON(w, broker.AppErrorStatus(code), broker.AppErrorBody(code), broker.AppErrorRetryAfter(code))
}

// ServeHTTP routes the three calls of portal.broker/1; everything else is an
// invalid request.
func (in *egressInstall) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.Fragment != "" {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/network/http":
		in.handlePost(w, r, broker.MaxAppRequestBytes, in.network)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/notifications":
		in.handlePost(w, r, broker.MaxAppNoticeBytes, in.notice)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
		in.status(w)
	default:
		writeAppError(w, broker.AppErrInvalid)
	}
}

func (in *egressInstall) handlePost(w http.ResponseWriter, r *http.Request, limit int64, handler func(http.ResponseWriter, *http.Request, []byte)) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" || r.ContentLength > limit {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	defer clear(raw)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	handler(w, r, raw)
}

func (in *egressInstall) status(w http.ResponseWriter) {
	in.mu.Lock()
	s := in.current
	usable := s != nil && !s.draining && in.registry.now().Before(s.expires)
	var resources []string
	if usable {
		resources = slices.Clone(s.resources)
	}
	in.mu.Unlock()
	writeAppJSON(w, 200, broker.AppStatusBody(usable, resources), "")
}

// begin takes the admission a call needs: a current, unexpired session carrying
// the resource, a free concurrency slot (network calls only) and a rate token.
// It returns the session and the function that ends the call's accounting, or
// the application error code to answer with.
func (in *egressInstall) begin(resource string) (*egressSession, func(), string) {
	network := resource == "network.http"
	in.mu.Lock()
	defer in.mu.Unlock()
	s := in.current
	if in.closed || s == nil || s.draining || !in.registry.now().Before(s.expires) || in.intact != nil && in.intact() != nil {
		return nil, nil, broker.AppErrUnavailable
	}
	if !s.allows(resource) {
		return nil, nil, broker.AppErrDenied
	}
	if network && in.inflight >= s.concurrency {
		return nil, nil, broker.AppErrBusy
	}
	if !in.bucket.Allow() {
		return nil, nil, broker.AppErrBusy
	}
	s.inflight++
	if network {
		in.inflight++
	}
	return s, func() { s.release(network) }, ""
}

// refusalCode maps a Portal refusal to an application error code.
func refusalCode(code byte) string {
	switch code {
	case EgressRefusalDenied:
		return broker.AppErrDenied
	case EgressRefusalUnavailable:
		return broker.AppErrUnavailable
	case EgressRefusalBusy:
		return broker.AppErrBusy
	case EgressRefusalConflict:
		return broker.AppErrConflict
	case EgressRefusalCapacity:
		return broker.AppErrCapacity
	default:
		return broker.AppErrInvalid
	}
}

// newCallCode maps a failure to start a call. Nothing was dispatched, so only a
// full session is busy; a session that just closed, a protocol error or a
// cancelled context is unavailable, never uncertain.
func newCallCode(err error) string {
	if errors.Is(err, broker.ErrBusy) {
		return broker.AppErrBusy
	}
	return broker.AppErrUnavailable
}

// awaitPortal waits for Portal's first answer to a call. A timeout or a closed
// session means nothing was dispatched, so the call is unavailable.
func awaitPortal(ctx context.Context, call *EgressCall) (EgressFrame, string) {
	wait, cancel := context.WithTimeout(ctx, egressAdmitTimeout)
	defer cancel()
	frame, err := call.Recv(wait)
	if err != nil {
		return EgressFrame{}, broker.AppErrUnavailable
	}
	return frame, ""
}

func (in *egressInstall) network(w http.ResponseWriter, r *http.Request, raw []byte) {
	request, err := broker.DecodeAppHTTPRequest(raw)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	defer clear(request.Body)
	digest := sha256.Sum256(request.Body)
	admitted := EgressAdmit{Method: request.Method, URL: request.URL, Accept: request.Accept, ContentType: request.ContentType, BodyBytes: len(request.Body), BodySHA256: hex.EncodeToString(digest[:]), Credential: request.Credential}
	if request.Signing != (appplatform.SigningRequest{}) {
		admitted.Credential = request.Signing.Credential
		admitted.SigningScheme, admitted.SigningAction, admitted.SigningVersion = request.Signing.Scheme, request.Signing.Action, request.Signing.Version
	}
	// An input no admit frame can carry is invalid, and costs no rate token.
	admit, err := EncodeEgressAdmit(admitted)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	session, done, code := in.begin("network.http")
	if code != "" {
		writeAppError(w, code)
		return
	}
	defer done()
	// Naming a binding needs a session that carries secrets.use. Portal is not
	// asked: a session that never held the resource cannot use a credential.
	if admitted.Credential != "" && !session.allows("secrets.use") {
		writeAppError(w, broker.AppErrDenied)
		return
	}
	call, err := session.mux.NewCall()
	if err != nil {
		writeAppError(w, newCallCode(err))
		return
	}
	defer call.Finish()
	ctx, cancel := context.WithTimeout(r.Context(), egressCallTimeout)
	defer cancel()
	if err := call.Send(EgressFrameAdmit, admit); err != nil {
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	frame, code := awaitPortal(ctx, call)
	if code != "" {
		writeAppError(w, code)
		return
	}
	switch frame.Type {
	case EgressFrameRefusal:
		refused, err := DecodeEgressRefusal(frame.Payload)
		if err != nil {
			session.mux.Close(EgressCloseRefused)
			writeAppError(w, broker.AppErrUnavailable)
			return
		}
		writeAppError(w, refusalCode(refused))
		return
	case EgressFrameCommand:
	default:
		session.mux.Close(EgressCloseRefused)
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	// The command frame and its decoded copy carry the credential value Portal
	// placed: they are wiped when the call ends, as the foreground path wipes its.
	defer clear(frame.Payload)
	commandRaw, commandSig, err := DecodeEgressCommand(frame.Payload)
	if err != nil {
		session.mux.Close(EgressCloseRefused)
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	defer clear(commandRaw)
	response, runErr := in.run(ctx, session, call, commandRaw, commandSig, request.Body, admitted)
	outcome := egressOutcomeOf(response, runErr)
	if payload, err := EncodeEgressOutcome(outcome); err == nil {
		_ = call.Send(EgressFrameOutcome, payload)
	}
	if runErr != nil {
		writeAppError(w, broker.AppErrorFor(runErr))
		return
	}
	writeAppJSON(w, 200, broker.AppHTTPSuccessBody(response), "")
}

// run verifies Portal's command against the session and the admit, then
// executes it on the shared pool. A command that does not match is refused as
// denied and never executes.
func (in *egressInstall) run(ctx context.Context, session *egressSession, call *EgressCall, raw, signature, body []byte, admit EgressAdmit) (broker.HTTPResponse, error) {
	var zero broker.HTTPResponse
	authority, err := in.registry.provider.enrollment(ctx)
	if err != nil {
		return zero, broker.ErrDenied
	}
	verified, err := control.VerifyNetworkHTTPRequest(raw, signature, authority, in.registry.now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, body) != nil {
		return zero, broker.ErrDenied
	}
	defer verified.Clear()
	command := verified.Command()
	defer clear(command.CredentialValue)
	defer command.ClearSignedHeaders()
	if session.checkCommand(command, admit, body) != nil || !session.claim(command.RequestID) {
		return zero, broker.ErrDenied
	}
	return in.registry.provider.executeEgress(ctx, call, raw, signature, body, authority)
}

// egressOutcomeOf is what Portal's summary audit learns about a call: its class,
// and, for a released response, status, size and Retry-After. No body, URL or
// header crosses.
func egressOutcomeOf(response broker.HTTPResponse, err error) EgressOutcome {
	outcome := EgressOutcome{RetryAfter: -1}
	switch {
	case err == nil:
		outcome.Class = EgressOutcomeOK
		outcome.Status = uint16(response.Status)
		outcome.ResponseBytes = uint32(len(response.Body))
		outcome.RetryAfter = int32(response.RetryAfterSeconds)
	case errors.Is(err, broker.ErrDenied):
		outcome.Class = EgressOutcomeDenied
	case errors.Is(err, broker.ErrBusy):
		outcome.Class = EgressOutcomeBusy
	case errors.Is(err, broker.ErrUnavailable):
		outcome.Class = EgressOutcomeUnavailable
	default:
		outcome.Class = EgressOutcomeUncertain
	}
	return outcome
}

func (in *egressInstall) notice(w http.ResponseWriter, r *http.Request, raw []byte) {
	notice, err := broker.DecodeAppNotice(raw)
	if err != nil {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	payload := notice.JSON()
	if len(payload) > egressNoticeMaxBytes {
		writeAppError(w, broker.AppErrInvalid)
		return
	}
	session, done, code := in.begin("notifications.publish")
	if code != "" {
		writeAppError(w, code)
		return
	}
	defer done()
	call, err := session.mux.NewCall()
	if err != nil {
		writeAppError(w, newCallCode(err))
		return
	}
	defer call.Finish()
	ctx, cancel := context.WithTimeout(r.Context(), egressCallTimeout)
	defer cancel()
	if err := call.Send(EgressFrameNotice, payload); err != nil {
		writeAppError(w, broker.AppErrUnavailable)
		return
	}
	frame, code := awaitPortal(ctx, call)
	if code != "" {
		writeAppError(w, code)
		return
	}
	switch frame.Type {
	case EgressFrameRefusal:
		refused, err := DecodeEgressRefusal(frame.Payload)
		if err != nil {
			session.mux.Close(EgressCloseRefused)
			writeAppError(w, broker.AppErrUnavailable)
			return
		}
		writeAppError(w, refusalCode(refused))
	case EgressFrameNoticeResult:
		result, err := DecodeEgressNoticeResult(frame.Payload)
		if err != nil {
			session.mux.Close(EgressCloseRefused)
			writeAppError(w, broker.AppErrUnavailable)
			return
		}
		writeAppJSON(w, 200, broker.AppNoticeSuccessBody(result.EventID, result.Created), "")
	default:
		session.mux.Close(EgressCloseRefused)
		writeAppError(w, broker.AppErrUnavailable)
	}
}
