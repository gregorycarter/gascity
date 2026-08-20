package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckInstalledBinariesChecksGCAndBD(t *testing.T) {
	repoRoot := repoRoot(t)
	for _, tc := range []struct {
		name       string
		gcCommit   string
		bdVersion  string
		wantOutput string
		wantFailed bool
	}{
		{name: "matching binaries", gcCommit: "abc1234", bdVersion: "v1.2.3-0.20260820081939-6c35a31db1ea"},
		{name: "stale gc", gcCommit: "oldgc", bdVersion: "v1.2.3-0.20260820081939-6c35a31db1ea", wantOutput: "gc", wantFailed: true},
		{name: "stale bd", gcCommit: "abc1234", bdVersion: "oldbd", wantOutput: "bd", wantFailed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			binDir := filepath.Join(tmp, "bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			gcBinary := filepath.Join(binDir, "gc")
			bdBinary := filepath.Join(binDir, "bd")
			writeExecutable(t, gcBinary, "#!/bin/sh\nprintf '%s\\n' '{\"commit\":\""+tc.gcCommit+"\"}'\n")
			writeExecutable(t, bdBinary, "#!/bin/sh\nexit 0\n")
			writeExecutable(t, filepath.Join(binDir, "git"), "#!/bin/sh\nprintf '%s\\n' abc1234\n")
			writeExecutable(t, filepath.Join(binDir, "go"), "#!/bin/sh\ncase \"$1\" in\n  list) printf '%s\\n' 'github.com/gregorycarter/beads v1.2.3-0.20260820081939-6c35a31db1ea' ;;\n  version) printf '%s\\n' 'bd: go1.26.5' 'path github.com/steveyegge/beads/cmd/bd' 'mod github.com/steveyegge/beads v1.2.3-0.20260820081939-6c35a31db1ea' '=> github.com/gregorycarter/beads "+tc.bdVersion+"' ;;\n  *) exit 1 ;;\nesac\n")

			cmd := exec.Command(filepath.Join(repoRoot, "scripts", "check-installed-binaries.sh"))
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GC_BINARY="+gcBinary,
				"BD_BINARY="+bdBinary,
			)
			output, err := cmd.CombinedOutput()
			if tc.wantFailed != (err != nil) {
				t.Fatalf("check failed = %v, want %v; output:\n%s", err != nil, tc.wantFailed, output)
			}
			if tc.wantOutput != "" && !strings.Contains(string(output), tc.wantOutput) {
				t.Fatalf("output = %q, want it to mention %q", output, tc.wantOutput)
			}
		})
	}
}
