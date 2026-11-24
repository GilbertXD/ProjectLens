package gitrepo

import "syscall"

func InspectRootReadOnly(path string) (readOnly bool, known bool, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, false, err
	}
	const mntReadOnly = 1 // MNT_RDONLY from <sys/mount.h>
	return stat.Flags&mntReadOnly != 0, true, nil
}
