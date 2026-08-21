package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestDoHookClaimRetriesOnlyTheSameCandidateAfterDefiniteAbort(t *testing.T) {
	var attempts []string
	claim := func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
		attempts = append(attempts, beadID)
		if len(attempts) < 2 {
			return beads.Bead{}, false, errors.New("Error 1213 (40001): serialization failure")
		}
		return beads.Bead{ID: beadID, Status: "in_progress", Assignee: assignee}, true, nil
	}
	var stdout, stderr bytes.Buffer
	code := doHookClaim(`[ {"id":"one","status":"open","metadata":{"gc.routed_to":"worker"}}, {"id":"two","status":"open","metadata":{"gc.routed_to":"worker"}} ]`, ".", hookClaimOptions{
		Assignee:     "worker",
		RouteTargets: []string{"worker"},
		JSON:         true,
	}, hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[ {"id":"one","status":"open","metadata":{"gc.routed_to":"worker"}}, {"id":"two","status":"open","metadata":{"gc.routed_to":"worker"}} ]`, nil
		},
		Claim: claim,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(attempts, ","); got != "one,one" {
		t.Fatalf("claim attempts = %q, want same candidate retried", got)
	}
}

func TestDoHookClaimIndeterminateCommitNeedsRecovery(t *testing.T) {
	var attempts []string
	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", ".", hookClaimOptions{
		Assignee:     "worker",
		RouteTargets: []string{"worker"},
		JSON:         true,
	}, hookClaimOps{
		Runner: func(string, string) (string, error) {
			return `[{"id":"one","status":"open","metadata":{"gc.routed_to":"worker"}},{"id":"two","status":"open","metadata":{"gc.routed_to":"worker"}}]`, nil
		},
		Claim: func(_ context.Context, _ string, _ []string, beadID, _ string) (beads.Bead, bool, error) {
			attempts = append(attempts, beadID)
			return beads.Bead{}, false, fmt.Errorf("claim response lost: %w", beads.ErrCommitIndeterminate)
		},
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doHookClaim = %d, want 1", code)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("recovery result is not JSON: %v; output=%q", err, stdout.String())
	}
	if result.Action != "recovery_needed" || !result.RecoveryNeeded || result.BeadID != "one" {
		t.Fatalf("recovery result = %+v", result)
	}
	if got := strings.Join(attempts, ","); got != "one" {
		t.Fatalf("claim attempts = %q, want no replay or rescan", got)
	}
}
