package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const detachedProbeTestTimeout = 5 * time.Second

func TestParseDetachedProbeSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    detachedProbeSpec
		wantErr bool
	}{
		{
			name: "tmux spec",
			spec: "tmux:gascity:soak-loop",
			want: detachedProbeSpec{
				Kind:    "tmux",
				Socket:  "gascity",
				Session: "soak-loop",
			},
		},
		{
			name: "session keeps extra colons",
			spec: "tmux:gascity:scope:soak-loop",
			want: detachedProbeSpec{
				Kind:    "tmux",
				Socket:  "gascity",
				Session: "scope:soak-loop",
			},
		},
		{
			name:    "unsupported kind",
			spec:    "k8s:pod:agent",
			wantErr: true,
		},
		{
			name:    "missing session",
			spec:    "tmux:gascity",
			wantErr: true,
		},
		{
			name:    "empty socket",
			spec:    "tmux::soak-loop",
			wantErr: true,
		},
		{
			name:    "empty session",
			spec:    "tmux:gascity:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDetachedProbeSpec(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDetachedProbeSpec(%q) error = nil, want error", tt.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDetachedProbeSpec(%q) error = %v", tt.spec, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDetachedProbeSpec(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestProbeDetachedWork_TmuxExitStatus(t *testing.T) {
	tests := []struct {
		name       string
		exitCode   int
		wantStatus detachedProbeStatus
	}{
		{name: "alive on exit zero", exitCode: 0, wantStatus: detachedProbeAlive},
		{name: "dead on exit one", exitCode: 1, wantStatus: detachedProbeDead},
		{name: "error on other exit", exitCode: 2, wantStatus: detachedProbeError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotSpec detachedProbeSpec
			setDetachedProbeCommandForTest(t, func(_ context.Context, spec detachedProbeSpec) detachedProbeCommandResult {
				gotSpec = spec
				if tt.exitCode == 0 {
					return detachedProbeCommandResult{}
				}
				return detachedProbeCommandResult{ExitCode: tt.exitCode, Err: errors.New("fake tmux exit")}
			})

			got := probeDetachedWork(context.Background(), "tmux:gascity:soak-loop")
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q (err=%v)", got.Status, tt.wantStatus, got.Err)
			}
			wantSpec := detachedProbeSpec{Kind: "tmux", Socket: "gascity", Session: "soak-loop"}
			if gotSpec != wantSpec {
				t.Fatalf("tmux spec = %+v, want %+v", gotSpec, wantSpec)
			}
		})
	}
}

func TestProbeDetachedWork_UsesProductionTimeoutByDefault(t *testing.T) {
	if got, want := detachedProbeTimeoutBudget, time.Second; got != want {
		t.Fatalf("detachedProbeTimeoutBudget = %s, want production default %s", got, want)
	}
}

func TestProbeDetachedWork_MalformedSpecIsError(t *testing.T) {
	got := probeDetachedWorkWithTimeout(context.Background(), "tmux:gascity", time.Second)
	if got.Status != detachedProbeError {
		t.Fatalf("Status = %q, want %q", got.Status, detachedProbeError)
	}
	if got.Err == nil {
		t.Fatal("Err = nil, want parse error")
	}
}

func TestProbeDetachedWork_Timeout(t *testing.T) {
	setDetachedProbeCommandForTest(t, func(ctx context.Context, _ detachedProbeSpec) detachedProbeCommandResult {
		<-ctx.Done()
		return detachedProbeCommandResult{Err: ctx.Err()}
	})

	got := probeDetachedWorkWithTimeout(context.Background(), "tmux:gascity:soak-loop", 10*time.Millisecond)
	if got.Status != detachedProbeTimeout {
		t.Fatalf("Status = %q, want %q (err=%v)", got.Status, detachedProbeTimeout, got.Err)
	}
	if got.Err == nil {
		t.Fatal("Err = nil, want timeout error")
	}
}

func installFakeTmux(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	pathEnv := dir
	if existing := os.Getenv("PATH"); strings.TrimSpace(existing) != "" {
		pathEnv += string(os.PathListSeparator) + existing
	}
	t.Setenv("PATH", pathEnv)
}

func setDetachedProbeTimeoutForTest(t *testing.T) {
	t.Helper()
	previous := detachedProbeTimeoutBudget
	detachedProbeTimeoutBudget = detachedProbeTestTimeout
	t.Cleanup(func() {
		detachedProbeTimeoutBudget = previous
	})
}

func setDetachedProbeCommandForTest(t *testing.T, run func(context.Context, detachedProbeSpec) detachedProbeCommandResult) {
	t.Helper()
	previous := runDetachedProbeCommand
	runDetachedProbeCommand = run
	t.Cleanup(func() {
		runDetachedProbeCommand = previous
	})
}
