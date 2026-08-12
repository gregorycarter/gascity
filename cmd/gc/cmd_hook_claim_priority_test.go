package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestDoHookClaimPrioritizesRoutedPoolCandidates(t *testing.T) {
	tests := []struct {
		name       string
		candidates string
		want       string
	}{
		{
			name: "higher priority before older lower priority work",
			candidates: `[
				{"id":"older-p2","status":"open","priority":2,"created_at":"2026-08-10T10:00:00Z","metadata":{"gc.routed_to":"rig/pool"}},
				{"id":"newer-p0","status":"open","priority":0,"created_at":"2026-08-10T12:00:00Z","metadata":{"gc.routed_to":"rig/pool"}}
			]`,
			want: "newer-p0",
		},
		{
			name: "oldest first within one priority band",
			candidates: `[
				{"id":"newer-p1","status":"open","priority":1,"created_at":"2026-08-10T12:00:00Z","metadata":{"gc.routed_to":"rig/pool"}},
				{"id":"older-p1","status":"open","priority":1,"created_at":"2026-08-10T10:00:00Z","metadata":{"gc.routed_to":"rig/pool"}}
			]`,
			want: "older-p1",
		},
		{
			name: "higher priority ready assignment before older assignment",
			candidates: `[
				{"id":"assigned-p2","status":"open","priority":2,"assignee":"worker-1","created_at":"2026-08-10T10:00:00Z","metadata":{"gc.routed_to":"rig/pool"}},
				{"id":"assigned-p0","status":"open","priority":0,"assignee":"worker-1","created_at":"2026-08-10T12:00:00Z","metadata":{"gc.routed_to":"rig/pool"}}
			]`,
			want: "assigned-p0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts []string
			ops := hookClaimOps{
				Runner: func(string, string) (string, error) { return tt.candidates, nil },
				Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
					attempts = append(attempts, beadID)
					return beads.Bead{
						ID:       beadID,
						Status:   "in_progress",
						Assignee: assignee,
						Metadata: beads.StringMap{"gc.routed_to": "rig/pool"},
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
			if got := strings.Join(attempts, ","); got != tt.want {
				t.Fatalf("claim attempts = %q, want %q first", got, tt.want)
			}
		})
	}
}
