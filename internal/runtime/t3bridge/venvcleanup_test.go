package t3bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovePoetryVenvsIgnoresEmptyPath(_ *testing.T) {
	removePoetryVenvs("")
}

func TestRemovePoetryVenvsSkipsNonPythonWorktree(t *testing.T) {
	removePoetryVenvs(t.TempDir())
}

func TestRemovePoetryVenvsSkipsMissingDirectory(t *testing.T) {
	removePoetryVenvs(filepath.Join(t.TempDir(), "gone"))
}

func TestRemovePoetryVenvsHandlesDeletedWorktreeWithPyproject(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[tool.poetry]\n"), 0o644); err != nil {
		t.Fatalf("write pyproject: %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	removePoetryVenvs(worktree)
}

func TestRemovePoetryVenvsRunsPoetryFromWorktree(t *testing.T) {
	bin := t.TempDir()
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[tool.poetry]\n"), 0o644); err != nil {
		t.Fatalf("write pyproject: %v", err)
	}

	argsFile := filepath.Join(t.TempDir(), "poetry-args")
	poetry := filepath.Join(bin, "poetry")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" > \"$POETRY_TEST_ARGS\"\nprintf '%s\\n' \"$*\" >> \"$POETRY_TEST_ARGS\"\n"
	if err := os.WriteFile(poetry, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake poetry: %v", err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("POETRY_TEST_ARGS", argsFile)

	removePoetryVenvs(worktree)

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read fake poetry output: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != worktree || lines[1] != "env remove --all" {
		t.Fatalf("fake poetry invocation = %q, want cwd %q and args %q", string(data), worktree, "env remove --all")
	}
}

func TestTrimForLogBoundsOutput(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := trimForLog([]byte(long))
	if len(got) > 320 {
		t.Fatalf("trimForLog returned %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("trimForLog should mark truncation")
	}

	short := "poetry: no environment found"
	if got := trimForLog([]byte(short)); got != short {
		t.Fatalf("trimForLog altered short output: %q", got)
	}
}
