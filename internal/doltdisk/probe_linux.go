//go:build linux

package doltdisk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Probe measures unprivileged write capacity using f_bavail*f_frsize, the
// POSIX quantity that accounts for the filesystem's fundamental block size.
func Probe(path string) (ProbeResult, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return ProbeResult{}, fmt.Errorf("statfs %q: %w", path, err)
	}
	blockSize := stat.Frsize
	if blockSize <= 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 {
		return ProbeResult{}, fmt.Errorf("statfs %q returned zero block size", path)
	}
	if stat.Bavail > uint64(^uint64(0)>>1)/uint64(blockSize) {
		return ProbeResult{}, fmt.Errorf("statfs %q available-byte count overflows int64", path)
	}
	return ProbeResult{
		AvailableBytes: int64(stat.Bavail * uint64(blockSize)),
		Filesystem:     filesystemIdentity(stat),
	}, nil
}

func filesystemIdentity(stat unix.Statfs_t) string {
	return fmt.Sprintf("fsid:%v", stat.Fsid)
}
