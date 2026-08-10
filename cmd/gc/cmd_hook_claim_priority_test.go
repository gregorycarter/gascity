package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// The pool claim must honor bead priority: with a P0 and a P2 both ready and
// identically routed in one pool, the claiming worker takes the P0 even when
// the work query serves the P2 first (ga-1jp: a production P0 sat unworked for
// 80+ minutes while pool workers claimed older P1/P2 work because the routed
// tier ordered by created_at). The claim layer imposes the canonical ready
// order (priority, created_at, id) on the candidate batch so no reader
// ordering — created_at-sorted, leg-concatenated, or user-overridden — can
// starve a higher-priority bead that is present in the batch.

func hookClaimPriorityTestRunner(t *testing.T, candidates []map[string]any) WorkQueryRunner {
	t.Helper()
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}
	return func(string, string) (string, error) { return string(output), nil }
}

func TestDoHookClaimTakesHighestPriorityRoutedCandidate(t *testing.T) {
	runner := hookClaimPriorityTestRunner(t, []map[string]any{
		{
			"id":         "old-p2",
			"status":     "open",
			"priority":   2,
			"created_at": "2026-08-10T10:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
		{
			"id":         "new-p0",
			"status":     "open",
			"priority":   0,
			"created_at": "2026-08-10T12:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
	})

	var attempts []string
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			attempts = append(attempts, beadID)
			return beads.Bead{
				ID:       beadID,
				Status:   "in_progress",
				Assignee: assignee,
				Metadata: map[string]string{"gc.routed_to": "rig/pool"},
			}, true, nil
		},
		StampWorkMeta:     func(context.Context, string, []string, string, string, map[string]string) error { return nil },
		ResolveWorkBranch: func(string) string { return "" },
		PublishRunMap:     func(string, string, ...string) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"rig/pool"},
		JSON:         true,
	}, ops, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("doHookClaim = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got := strings.Join(attempts, ","); got != "new-p0" {
		t.Fatalf("claim attempts = %q, want the P0 (new-p0) claimed first over the older P2", got)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result %q: %v", stdout.String(), err)
	}
	if result.BeadID != "new-p0" {
		t.Fatalf("claimed bead = %q, want new-p0", result.BeadID)
	}
}

func TestDoHookClaimEqualPriorityKeepsOldestFirst(t *testing.T) {
	runner := hookClaimPriorityTestRunner(t, []map[string]any{
		{
			"id":         "newer-p1",
			"status":     "open",
			"priority":   1,
			"created_at": "2026-08-10T12:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
		{
			"id":         "older-p1",
			"status":     "open",
			"priority":   1,
			"created_at": "2026-08-10T10:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
	})

	var attempts []string
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			attempts = append(attempts, beadID)
			return beads.Bead{
				ID:       beadID,
				Status:   "in_progress",
				Assignee: assignee,
				Metadata: map[string]string{"gc.routed_to": "rig/pool"},
			}, true, nil
		},
		StampWorkMeta:     func(context.Context, string, []string, string, string, map[string]string) error { return nil },
		ResolveWorkBranch: func(string) string { return "" },
		PublishRunMap:     func(string, string, ...string) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"rig/pool"},
		JSON:         true,
	}, ops, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("doHookClaim = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got := strings.Join(attempts, ","); got != "older-p1" {
		t.Fatalf("claim attempts = %q, want the older bead within an equal-priority band", got)
	}
}

func TestDoHookClaimTakesHighestPriorityReadyAssignment(t *testing.T) {
	runner := hookClaimPriorityTestRunner(t, []map[string]any{
		{
			"id":         "assigned-p2",
			"status":     "open",
			"priority":   2,
			"assignee":   "worker-1",
			"created_at": "2026-08-10T10:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
		{
			"id":         "assigned-p0",
			"status":     "open",
			"priority":   0,
			"assignee":   "worker-1",
			"created_at": "2026-08-10T12:00:00Z",
			"metadata":   map[string]string{"gc.routed_to": "rig/pool"},
		},
	})

	var attempts []string
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			attempts = append(attempts, beadID)
			return beads.Bead{
				ID:       beadID,
				Status:   "in_progress",
				Assignee: assignee,
				Metadata: map[string]string{"gc.routed_to": "rig/pool"},
			}, true, nil
		},
		StampWorkMeta:     func(context.Context, string, []string, string, string, map[string]string) error { return nil },
		ResolveWorkBranch: func(string) string { return "" },
		PublishRunMap:     func(string, string, ...string) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"rig/pool"},
		JSON:         true,
	}, ops, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("doHookClaim = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got := strings.Join(attempts, ","); got != "assigned-p0" {
		t.Fatalf("claim attempts = %q, want the P0 ready assignment promoted first", got)
	}
}
