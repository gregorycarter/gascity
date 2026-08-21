package main

import (
	"errors"
	"strings"
	"testing"
)

func TestProbeManagedDoltClassifiesStructuredStates(t *testing.T) {
	tests := []struct {
		name       string
		inspection managedDoltProcessInspection
		sqlLive    bool
		tcpLive    bool
		wantState  managedDoltProbeState
		wantRun    bool
	}{
		{
			name: "owned serving",
			inspection: managedDoltProcessInspection{
				PortHolderPID:           123,
				PortHolderOwned:         true,
				PortHolderOwnership:     managedDoltOwnershipOwned,
				PortHolderDeletedInodes: false,
			},
			sqlLive:   true,
			tcpLive:   true,
			wantState: managedDoltProbeServingOwned,
			wantRun:   true,
		},
		{
			name: "serving ownership unknown",
			inspection: managedDoltProcessInspection{
				PortHolderPID:       123,
				PortHolderOwnership: managedDoltOwnershipUnknown,
			},
			sqlLive:   true,
			tcpLive:   true,
			wantState: managedDoltProbeServingOwnershipUnknown,
		},
		{
			name: "foreign listener",
			inspection: managedDoltProcessInspection{
				PortHolderPID:       123,
				PortHolderOwnership: managedDoltOwnershipForeign,
			},
			sqlLive:   true,
			tcpLive:   true,
			wantState: managedDoltProbeIdentityMismatch,
		},
		{
			name: "deleted data inode",
			inspection: managedDoltProcessInspection{
				PortHolderPID:           123,
				PortHolderOwned:         true,
				PortHolderOwnership:     managedDoltOwnershipOwned,
				PortHolderDeletedInodes: true,
			},
			sqlLive:   true,
			tcpLive:   true,
			wantState: managedDoltProbeStaleRuntime,
		},
		{
			name: "sql unreachable with owned listener",
			inspection: managedDoltProcessInspection{
				PortHolderPID:       123,
				PortHolderOwned:     true,
				PortHolderOwnership: managedDoltOwnershipOwned,
			},
			tcpLive:   true,
			wantState: managedDoltProbeUnreachable,
		},
		{
			name: "stale pid without listener",
			inspection: managedDoltProcessInspection{
				ManagedPID:       123,
				ManagedOwnership: managedDoltOwnershipOwned,
				ManagedOwned:     true,
			},
			wantState: managedDoltProbeStaleRuntime,
		},
		{
			name: "stale runtime state without live pid",
			inspection: managedDoltProcessInspection{
				RuntimeStateRunning: true,
				RuntimeStatePID:     123,
			},
			wantState: managedDoltProbeStaleRuntime,
		},
		{
			name: "runtime data directory mismatch",
			inspection: managedDoltProcessInspection{
				RuntimeIdentityMismatch: true,
			},
			sqlLive:   true,
			wantState: managedDoltProbeIdentityMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyManagedDoltProbe(tt.inspection, tt.sqlLive, tt.tcpLive)
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q", got.State, tt.wantState)
			}
			if got.Running != tt.wantRun {
				t.Fatalf("running = %t, want %t", got.Running, tt.wantRun)
			}
		})
	}
}

func TestManagedDoltProbeFieldsExposeDimensionsAndCompatibility(t *testing.T) {
	report := managedDoltProbeReport{
		Running:                 true,
		State:                   managedDoltProbeServingOwned,
		Liveness:                managedDoltLivenessServing,
		SQLLive:                 true,
		Ownership:               managedDoltOwnershipOwned,
		Integrity:               managedDoltIntegrityIntact,
		PortHolderPID:           123,
		PortHolderOwned:         true,
		PortHolderDeletedInodes: false,
		TCPReachable:            true,
	}
	fields := managedDoltProbeFields(report)
	joined := strings.Join(fields, "\n")
	for _, want := range []string{
		"running\ttrue",
		"state\tserving_owned",
		"overall_state\tserving_owned",
		"liveness\tserving",
		"sql_live\ttrue",
		"ownership\towned",
		"integrity\tintact",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fields missing %q:\n%s", want, joined)
		}
	}
}

func TestProbeManagedDoltUsesBoundedSQLLivenessProbe(t *testing.T) {
	originalQuery := managedDoltProbeQueryFn
	originalTCP := managedDoltProbeTCPReachableFn
	t.Cleanup(func() {
		managedDoltProbeQueryFn = originalQuery
		managedDoltProbeTCPReachableFn = originalTCP
	})
	var gotHost, gotPort, gotUser string
	managedDoltProbeQueryFn = func(host, port, user string) error {
		gotHost, gotPort, gotUser = host, port, user
		return nil
	}
	managedDoltProbeTCPReachableFn = func(string, string) bool { return true }

	report, err := probeManagedDolt(t.TempDir(), "127.0.0.1", "3311")
	if err != nil {
		t.Fatalf("probeManagedDolt: %v", err)
	}
	if gotHost != "127.0.0.1" || gotPort != "3311" || gotUser != "root" {
		t.Fatalf("SQL probe args = (%q, %q, %q), want (127.0.0.1, 3311, root)", gotHost, gotPort, gotUser)
	}
	if !report.SQLLive || report.Liveness != managedDoltLivenessServing {
		t.Fatalf("report = %+v, want SQL-live serving report", report)
	}
}

func TestManagedDoltOwnershipTreatsProcessInspectionEPERMAsUnknown(t *testing.T) {
	originalArgs := managedDoltProcessArgsFn
	originalCWD := managedDoltProcessCWDMatchesFn
	t.Cleanup(func() {
		managedDoltProcessArgsFn = originalArgs
		managedDoltProcessCWDMatchesFn = originalCWD
	})
	managedDoltProcessArgsFn = func(int) (string, error) { return "", errors.New("operation not permitted") }
	managedDoltProcessCWDMatchesFn = func(int, string) bool { return false }

	layout, err := resolveManagedDoltRuntimeLayout(t.TempDir())
	if err != nil {
		t.Fatalf("resolveManagedDoltRuntimeLayout: %v", err)
	}
	got := managedDoltProcessOwnershipWithStateDir(4242, layout, "")
	if got != managedDoltOwnershipUnknown {
		t.Fatalf("ownership = %q, want unknown when process inspection returns EPERM", got)
	}
}
