package git

import (
	"context"
	"testing"
	"time"
)

func TestCommitCountSince(t *testing.T) {
	repo := initTestRepo(t)
	runGit(t, repo, "commit", "--allow-empty", "-m", "one")
	runGit(t, repo, "commit", "--allow-empty", "-m", "two")

	g := New(repo)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	count, err := g.CommitCountSince(ctx, "HEAD", past)
	if err != nil {
		t.Fatalf("CommitCountSince: %v", err)
	}
	// initTestRepo makes one initial commit; two more were added above.
	if count != 3 {
		t.Errorf("CommitCountSince(-1h) = %d, want 3", count)
	}

	future := time.Now().Add(time.Hour)
	count, err = g.CommitCountSince(ctx, "HEAD", future)
	if err != nil {
		t.Fatalf("CommitCountSince(future): %v", err)
	}
	if count != 0 {
		t.Errorf("CommitCountSince(+1h) = %d, want 0", count)
	}
}

func TestCommitCountSinceRejectsEmptyAndUnknownRef(t *testing.T) {
	repo := initTestRepo(t)
	g := New(repo)
	ctx := context.Background()

	if _, err := g.CommitCountSince(ctx, "", time.Now()); err == nil {
		t.Error("CommitCountSince(\"\") succeeded, want error")
	}
	if _, err := g.CommitCountSince(ctx, "refs/heads/does-not-exist", time.Now()); err == nil {
		t.Error("CommitCountSince(unknown ref) succeeded, want error")
	}
}

func TestFetchBranchUpdatesRemoteTrackingRefOnly(t *testing.T) {
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	// Seed the remote from a writer clone.
	writer := t.TempDir()
	runGit(t, writer, "clone", bare, ".")
	runGit(t, writer, "config", "user.email", "test@test.com")
	runGit(t, writer, "config", "user.name", "Test")
	runGit(t, writer, "commit", "--allow-empty", "-m", "init")
	runGit(t, writer, "push", "origin", "HEAD:refs/heads/main")

	// Reader clone observes main, then the writer moves it.
	reader := t.TempDir()
	runGit(t, reader, "clone", bare, ".")
	runGit(t, writer, "commit", "--allow-empty", "-m", "landed")
	runGit(t, writer, "push", "origin", "HEAD:refs/heads/main")

	g := New(reader)
	ctx := context.Background()
	before, err := g.CommitCountSince(ctx, "origin/main", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CommitCountSince before fetch: %v", err)
	}
	if err := g.FetchBranch(ctx, "main"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	after, err := g.CommitCountSince(ctx, "origin/main", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CommitCountSince after fetch: %v", err)
	}
	if after != before+1 {
		t.Errorf("origin/main count = %d after fetch, want %d", after, before+1)
	}

	if err := g.FetchBranch(ctx, ""); err == nil {
		t.Error("FetchBranch(\"\") succeeded, want error")
	}
}
