package t3bridge

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const venvCleanupTimeout = 60 * time.Second

// removePoetryVenvs removes every Poetry environment associated with a
// worktree. It is best-effort because cleanup must never prevent removal of
// the worktree itself.
//
// Poetry resolves its environment from the project directory, so callers must
// invoke this before deleting worktreePath.
func removePoetryVenvs(worktreePath string) {
	if worktreePath == "" {
		return
	}
	if info, err := os.Stat(worktreePath); err != nil || !info.IsDir() {
		log.Printf("venv cleanup: worktree %s is already gone; cannot remove its virtualenv", worktreePath)
		return
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "pyproject.toml")); err != nil {
		return
	}

	poetry, err := exec.LookPath("poetry")
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), venvCleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, poetry, "env", "remove", "--all")
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		log.Printf("venv cleanup: poetry env remove timed out after %s in %s; virtualenv may be leaked", venvCleanupTimeout, worktreePath)
	case err != nil:
		log.Printf("venv cleanup: poetry env remove in %s: %v (%s)", worktreePath, err, trimForLog(out))
	default:
		log.Printf("venv cleanup: removed poetry virtualenv(s) for %s", worktreePath)
	}
}

func trimForLog(b []byte) string {
	const limit = 300
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "…"
}
