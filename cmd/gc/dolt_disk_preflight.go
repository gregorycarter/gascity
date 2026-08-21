package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/doltdisk"
)

const (
	// doltDiskDefaultMinFreeBytes is the critical floor (500 MiB). Below this
	// threshold managed-Dolt startup is refused to prevent ENOSPC crashes.
	doltDiskDefaultMinFreeBytes = 500 << 20 // 500 MiB

	// doltDiskDefaultWarnFreeBytes is the soft floor (2 GiB). Below this
	// threshold a warning is emitted but operations are not blocked.
	doltDiskDefaultWarnFreeBytes = 2 << 30 // 2 GiB

	doltDiskGiB = float64(1 << 30)
)

// errDiskPreflightUnsupported is returned by the Windows stub (and any
// platform where statfs is unavailable). Call sites that receive this error
// must fail-open without logging — it is not a probe failure.
var errDiskPreflightUnsupported = errors.New("disk preflight unavailable on this platform")

// doltDiskMinFreeBytes returns the critical floor from GC_DOLT_MIN_FREE_BYTES,
// defaulting to 500 MiB. Zero disables the check entirely.
func doltDiskMinFreeBytes() int64 {
	return doltDiskConfig().CriticalBytes
}

// doltDiskWarnFreeBytes returns the soft floor from GC_DOLT_WARN_FREE_BYTES,
// defaulting to 2 GiB.
func doltDiskWarnFreeBytes() int64 {
	return doltDiskConfig().WarningBytes
}

func doltDiskConfig() doltdisk.Config {
	return doltdisk.ConfigFromEnv(os.Getenv)
}

// checkManagedDoltDiskPreflight checks free disk space before a disk-growing
// managed-Dolt operation. Returns a non-nil error when the state is critical
// or unknown. Unknown is deliberately not treated as healthy: starting or
// recovering Dolt can write journals, so an unmeasurable filesystem must not
// authorize that operation. Unsupported platforms remain an explicit skip.
func checkManagedDoltDiskPreflight(dataDir string, minFree, warnFree int64, stderr io.Writer) error {
	if minFree == 0 {
		return nil // check disabled via escape hatch
	}
	cfg := doltdisk.DefaultConfig()
	cfg.CriticalBytes = minFree
	cfg.WarningBytes = warnFree
	cfg.ResumeBytes = maxDiskBytes(diskIncrement(warnFree), diskDouble(minFree))
	cfg.ReserveBytes = minFree
	cfg.ProjectionHorizon = 0
	cfg.ThresholdSource = "managed-start"
	budget := doltdisk.New(cfg, func(path string) (doltdisk.ProbeResult, error) {
		free, err := doltContainerFreeBytesFunc(path)
		if err != nil {
			return doltdisk.ProbeResult{}, err
		}
		return doltdisk.ProbeResult{AvailableBytes: free, Filesystem: path, SampledAt: time.Now()}, nil
	}, time.Now)
	report := budget.Sample(dataDir)
	if report.State == doltdisk.StateUnknown {
		if strings.Contains(report.ProbeError, errDiskPreflightUnsupported.Error()) {
			return nil // platform stub — skip without claiming healthy
		}
		return fmt.Errorf("managed-dolt: disk state unknown: %s", report.ProbeError)
	}
	if report.State == doltdisk.StateCritical {
		return fmt.Errorf(
			"refusing to start managed Dolt: container free space %d bytes (%.1f GiB) "+
				"is below the floor %d bytes (%.1f GiB) on %s; "+
				"free disk space or set GC_DOLT_MIN_FREE_BYTES=0 to disable",
			report.AvailableBytes, float64(report.AvailableBytes)/doltDiskGiB,
			minFree, float64(minFree)/doltDiskGiB,
			dataDir)
	}
	if report.State == doltdisk.StateWarning {
		fmt.Fprintf(stderr, //nolint:errcheck
			"managed-dolt: disk WARN — %.1f GiB free (floor %.1f GiB, source %s) on %s\n",
			float64(report.AvailableBytes)/doltDiskGiB, float64(warnFree)/doltDiskGiB,
			report.ThresholdSource, dataDir)
	}
	return nil
}

func maxDiskBytes(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func diskIncrement(value int64) int64 {
	const maxInt64Value = int64(^uint64(0) >> 1)
	if value == maxInt64Value {
		return value
	}
	return value + 1
}

func diskDouble(value int64) int64 {
	const maxInt64Value = int64(^uint64(0) >> 1)
	if value > maxInt64Value/2 {
		return maxInt64Value
	}
	return value * 2
}
