package main

import (
	"strconv"
	"time"
)

type managedDoltProbeState string

const (
	managedDoltProbeServingOwned            managedDoltProbeState = "serving_owned"
	managedDoltProbeServingOwnershipUnknown managedDoltProbeState = "serving_ownership_unknown"
	managedDoltProbeUnreachable             managedDoltProbeState = "unreachable"
	managedDoltProbeIdentityMismatch        managedDoltProbeState = "identity_mismatch"
	managedDoltProbeStaleRuntime            managedDoltProbeState = "stale_runtime"
)

type managedDoltLiveness string

const (
	managedDoltLivenessServing     managedDoltLiveness = "serving"
	managedDoltLivenessUnreachable managedDoltLiveness = "unreachable"
)

type managedDoltOwnership string

const (
	managedDoltOwnershipOwned   managedDoltOwnership = "owned"
	managedDoltOwnershipUnknown managedDoltOwnership = "unknown"
	managedDoltOwnershipForeign managedDoltOwnership = "foreign"
)

type managedDoltIntegrity string

const (
	managedDoltIntegrityIntact           managedDoltIntegrity = "intact"
	managedDoltIntegrityUnknown          managedDoltIntegrity = "unknown"
	managedDoltIntegrityIdentityMismatch managedDoltIntegrity = "identity_mismatch"
	managedDoltIntegrityStaleRuntime     managedDoltIntegrity = "stale_runtime"
)

type managedDoltProbeReport struct {
	State                   managedDoltProbeState
	Liveness                managedDoltLiveness
	SQLLive                 bool
	Ownership               managedDoltOwnership
	Integrity               managedDoltIntegrity
	Running                 bool
	PortHolderPID           int
	PortHolderOwned         bool
	PortHolderDeletedInodes bool
	TCPReachable            bool
}

var (
	managedDoltProbeQueryFn        = managedDoltQueryProbe
	managedDoltProbeTCPReachableFn = managedDoltTCPReachable
)

func probeManagedDolt(cityPath, host, port string) (managedDoltProbeReport, error) {
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		return managedDoltProbeReport{}, err
	}
	info, err := inspectManagedDoltProcess(cityPath, port)
	if err != nil {
		return managedDoltProbeReport{}, err
	}
	tcpReachable := managedDoltProbeTCPReachableFn(host, port)
	sqlLive := managedDoltProbeQueryFn(host, port, "root") == nil
	report := classifyManagedDoltProbe(info, sqlLive, tcpReachable)
	report.PortHolderPID = info.PortHolderPID
	report.PortHolderOwned = info.PortHolderOwned
	report.PortHolderDeletedInodes = info.PortHolderDeletedInodes
	report.TCPReachable = tcpReachable
	if report.PortHolderPID > 0 && report.PortHolderOwned && !report.PortHolderDeletedInodes {
		report.PortHolderDeletedInodes = processHasDeletedDataInodesWithin(report.PortHolderPID, layout.DataDir, 300*time.Millisecond)
		if report.PortHolderDeletedInodes {
			info.PortHolderDeletedInodes = true
			report = classifyManagedDoltProbe(info, sqlLive, tcpReachable)
			report.PortHolderPID = info.PortHolderPID
			report.PortHolderOwned = info.PortHolderOwned
			report.PortHolderDeletedInodes = true
			report.TCPReachable = tcpReachable
		}
	}
	return report, nil
}

func classifyManagedDoltProbe(info managedDoltProcessInspection, sqlLive, tcpReachable bool) managedDoltProbeReport {
	ownership := info.PortHolderOwnership
	if ownership == "" {
		switch {
		case info.PortHolderOwned:
			ownership = managedDoltOwnershipOwned
		case info.PortHolderPID > 0:
			ownership = managedDoltOwnershipUnknown
		case info.ManagedOwnership != "":
			ownership = info.ManagedOwnership
		default:
			ownership = managedDoltOwnershipUnknown
		}
	}

	integrity := managedDoltIntegrityUnknown
	state := managedDoltProbeUnreachable
	switch {
	case info.PortHolderDeletedInodes || info.ManagedDeletedInodes:
		integrity = managedDoltIntegrityStaleRuntime
		state = managedDoltProbeStaleRuntime
	case info.RuntimeIdentityMismatch:
		integrity = managedDoltIntegrityIdentityMismatch
		state = managedDoltProbeIdentityMismatch
	case ownership == managedDoltOwnershipForeign:
		integrity = managedDoltIntegrityIdentityMismatch
		state = managedDoltProbeIdentityMismatch
	case sqlLive && ownership == managedDoltOwnershipOwned && info.PortHolderPID > 0:
		integrity = managedDoltIntegrityIntact
		state = managedDoltProbeServingOwned
	case !sqlLive && ownership == managedDoltOwnershipOwned && info.PortHolderPID > 0:
		integrity = managedDoltIntegrityIntact
		state = managedDoltProbeUnreachable
	case sqlLive:
		ownership = managedDoltOwnershipUnknown
		integrity = managedDoltIntegrityUnknown
		state = managedDoltProbeServingOwnershipUnknown
	case !sqlLive && (info.ManagedPID > 0 || info.RuntimeStateRunning || info.RuntimeStatePID > 0) && !tcpReachable:
		integrity = managedDoltIntegrityStaleRuntime
		state = managedDoltProbeStaleRuntime
	}

	liveness := managedDoltLivenessUnreachable
	if sqlLive {
		liveness = managedDoltLivenessServing
	}
	return managedDoltProbeReport{
		State:     state,
		Liveness:  liveness,
		SQLLive:   sqlLive,
		Ownership: ownership,
		Integrity: integrity,
		Running:   state == managedDoltProbeServingOwned,
	}
}

func managedDoltProbeFields(report managedDoltProbeReport) []string {
	return []string{
		"running\t" + strconv.FormatBool(report.Running),
		"state\t" + string(report.State),
		"overall_state\t" + string(report.State),
		"status\t" + string(report.State),
		"liveness\t" + string(report.Liveness),
		"sql_live\t" + strconv.FormatBool(report.SQLLive),
		"ownership\t" + string(report.Ownership),
		"integrity\t" + string(report.Integrity),
		"port_holder_pid\t" + strconv.Itoa(report.PortHolderPID),
		"port_holder_owned\t" + strconv.FormatBool(report.PortHolderOwned),
		"port_holder_deleted_inodes\t" + strconv.FormatBool(report.PortHolderDeletedInodes),
		"tcp_reachable\t" + strconv.FormatBool(report.TCPReachable),
	}
}
