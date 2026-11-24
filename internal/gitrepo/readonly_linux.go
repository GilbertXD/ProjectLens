package gitrepo

import "syscall"

func InspectRootReadOnly(path string) (readOnly bool, known bool, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, false, err
	}
	const stReadOnly = 1 // ST_RDONLY from <sys/statvfs.h>
	return stat.Flags&stReadOnly != 0, true, nil
}
