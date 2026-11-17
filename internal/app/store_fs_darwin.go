//go:build darwin

package app

import "syscall"

// darwinNetworkFilesystems are the f_fstypename values of the macOS network and FUSE filesystems.
var darwinNetworkFilesystems = map[string]bool{
	"nfs": true, "smbfs": true, "afpfs": true, "webdav": true, "osxfs": true, "macfuse": true,
	"osxfuse": true, "fuse": true, "fusefs": true, "9p": true, "virtiofs": true,
}

func detectSQLiteFilesystem(path string) (string, bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false, err
	}
	b := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	name := string(b)
	return name, darwinNetworkFilesystems[name], nil
}
