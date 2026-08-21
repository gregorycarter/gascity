//go:build darwin

package doltdisk

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

func filesystemIdentity(stat unix.Statfs_t) string {
	device := strings.TrimRight(string(stat.Mntfromname[:]), "\x00")
	mount := strings.TrimRight(string(stat.Mntonname[:]), "\x00")
	if device != "" && mount != "" {
		return device + " mounted at " + mount
	}
	return fmt.Sprintf("fsid:%v", stat.Fsid)
}
