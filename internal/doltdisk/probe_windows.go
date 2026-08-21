//go:build windows

package doltdisk

import "errors"

// Probe reports unsupported rather than fabricating a healthy capacity.
func Probe(_ string) (ProbeResult, error) {
	return ProbeResult{}, errors.New("disk capacity probe unavailable on windows")
}
