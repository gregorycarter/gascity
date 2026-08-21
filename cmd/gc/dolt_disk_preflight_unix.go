//go:build !windows

package main

import (
	"github.com/gastownhall/gascity/internal/doltdisk"
)

// doltContainerFreeBytesFunc is the injectable disk-space reader used by
// checkManagedDoltDiskPreflight. Tests replace this with a fake; production
// uses containerFreeBytes.
var doltContainerFreeBytesFunc = containerFreeBytes

// containerFreeBytes returns the bytes available to an unprivileged process
// in the filesystem containing path. The shared probe uses f_bavail×f_frsize
// where the platform exposes f_frsize and the platform block size otherwise.
func containerFreeBytes(path string) (int64, error) {
	result, err := doltdisk.Probe(path)
	if err != nil {
		return -1, err
	}
	return result.AvailableBytes, nil
}
