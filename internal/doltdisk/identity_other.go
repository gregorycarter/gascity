//go:build !windows && !linux && !darwin

package doltdisk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func filesystemIdentity(stat unix.Statfs_t) string {
	return fmt.Sprintf("fsid:%v", stat.Fsid)
}
