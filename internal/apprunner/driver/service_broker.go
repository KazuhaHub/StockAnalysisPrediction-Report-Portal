package driver

import "net"

// ServiceBrokerSocketName is the fixed name of the application-facing broker
// socket inside the per-installation, per-generation broker directory. The
// directory is root-owned, mounted read-only in the container and advertised to
// the application as PORTAL_APP_BROKER_SOCKET.
const ServiceBrokerSocketName = "broker.sock"

// ServiceBroker is a bound broker socket. Listener owns the socket file: closing
// it removes the file, and only if it is still the one that was bound. Intact
// reports whether the socket still sits at its bound place with its bound
// identity; the runner checks it on every call it signs, so a replaced
// directory or socket makes the broker unavailable instead of serving a stale
// binding. Both are nil when the binding was only verified, not bound.
type ServiceBroker struct {
	Listener net.Listener
	Intact   func() error
}
