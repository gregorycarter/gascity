//go:build !windows && !linux

package doltdisk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Probe measures unprivileged write capacity in the filesystem containing
// path. Statfs is non-mutating and f_bavail is used because it excludes space
// unavailable to the calling user (including APFS purgeable-space illusions).
func Probe(path string) (ProbeResult, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return ProbeResult{}, fmt.Errorf("statfs %q: %w", path, err)
	}
	blockSize := uint64(stat.Bsize) // f_frsize is unavailable on these targets.
	if blockSize == 0 {
		return ProbeResult{}, fmt.Errorf("statfs %q returned zero block size", path)
	}
	if stat.Bavail > uint64(^uint64(0)>>1)/blockSize {
		return ProbeResult{}, fmt.Errorf("statfs %q available-byte count overflows int64", path)
	}
	return ProbeResult{
		AvailableBytes: int64(stat.Bavail * blockSize),
		Filesystem:     filesystemIdentity(stat),
	}, nil
}
