//go:build linux

package driver

import (
	"golang.org/x/sys/unix"
)

// BindServiceBrokerInDir binds the broker socket in an existing directory, the
// way a launch binds it in its pinned broker directory: relative to a descriptor
// of the directory, root-owned, mode 0666. It exists so the controller
// endpoint's acceptance test serves a socket made by the production binder; the
// runner itself binds through BindServiceBroker.
func BindServiceBrokerInDir(dir string) (ServiceBroker, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ServiceBroker{}, ErrUnsafeSocket
	}
	defer unix.Close(fd)
	listener, err := bindServiceBrokerSocket(fd, 0)
	if err != nil {
		return ServiceBroker{}, err
	}
	return ServiceBroker{Listener: listener, Intact: listener.intact}, nil
}
