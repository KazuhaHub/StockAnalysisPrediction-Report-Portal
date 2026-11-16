//go:build linux

package app

import "syscall"

func detectSQLiteFilesystem(path string) (string, bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false, err
	}
	name, unsafeForWAL := classifyLinuxFilesystem(uint32(st.Type))
	return name, unsafeForWAL, nil
}
