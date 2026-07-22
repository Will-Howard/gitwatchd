package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Live inotify tests: real filesystem events driving real commits, with a
// short settle so the suite stays fast.

func startWatcher(t *testing.T, spec *RepoSpec) *repoWatcher {
	t.Helper()
	w, err := newRepoWatcher(spec, func(string, *RepoStatus) {}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.stopWatching)
	return w
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatcherCommitsAfterTheSettleWindow(t *testing.T) {
	repo := newTestRepo(t)
	startWatcher(t, repo.spec("-s", "0.2"))
	repo.write("notes.txt", "hello\n")
	waitFor(t, 10*time.Second, "the auto-commit", func() bool { return repo.commitCount() == 1 })
}

func TestWatcherIgnoresItsOwnGitChurn(t *testing.T) {
	repo := newTestRepo(t)
	startWatcher(t, repo.spec("-s", "0.2"))
	repo.write("notes.txt", "hello\n")
	waitFor(t, 10*time.Second, "the auto-commit", func() bool { return repo.commitCount() == 1 })
	// The commit itself churns .git; a retrigger loop would commit again
	// (or spin). Nothing should happen now.
	time.Sleep(1 * time.Second)
	if repo.commitCount() != 1 {
		t.Errorf("commits = %d, the watcher retriggered on .git churn", repo.commitCount())
	}
}

func TestWatcherHonorsExcludes(t *testing.T) {
	repo := newTestRepo(t)
	startWatcher(t, repo.spec("-s", "0.2", "-x", `\.log$`))
	repo.write("debug.log", "noise\n")
	time.Sleep(1 * time.Second)
	if repo.commitCount() != 0 {
		t.Error("an excluded change must not trigger a cycle")
	}
	// A non-excluded change still commits (and sweeps in the .log file,
	// exactly like gitwatch: -x filters events, not git add).
	repo.write("notes.txt", "hello\n")
	waitFor(t, 10*time.Second, "the auto-commit", func() bool { return repo.commitCount() == 1 })
}

func TestWatcherSeesNewSubdirectories(t *testing.T) {
	repo := newTestRepo(t)
	startWatcher(t, repo.spec("-s", "0.2"))
	os.MkdirAll(filepath.Join(repo.path, "fresh", "deep"), 0o755)
	waitFor(t, 10*time.Second, "the first commit", func() bool { return repo.commitCount() >= 1 })
	before := repo.commitCount()
	repo.write("fresh/deep/inner.txt", "made inside a new directory\n")
	waitFor(t, 10*time.Second, "a commit from inside the new subtree", func() bool {
		return repo.commitCount() > before && pendingCount(repo.path, "") == 0
	})
}

func TestWatcherFileTargetSurvivesAtomicSaves(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "v1\n")
	autoCommit(repo.spec())
	repo.write("b.txt", "not watched\n")

	spec, _ := parseRepoSpec([]string{"-s", "0.2", filepath.Join(repo.path, "a.txt")})
	startWatcher(t, spec)

	// An editor-style atomic save: write a temp file, rename over the target.
	tmp := filepath.Join(repo.path, "a.txt.tmp")
	os.WriteFile(tmp, []byte("v2\n"), 0o644)
	os.Rename(tmp, filepath.Join(repo.path, "a.txt"))
	waitFor(t, 10*time.Second, "the file-target commit", func() bool { return repo.commitCount() == 2 })
	if pendingCount(repo.path, "") != 1 {
		t.Error("b.txt stays uncommitted, only the file target is committed")
	}
}

func TestWatcherFlushNowCommitsWithoutEvents(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("pending.txt", "already here\n")
	w := startWatcher(t, repo.spec("-s", "0.2"))
	w.flushNow() // what -f and a resume catch-up do
	waitFor(t, 10*time.Second, "the flush commit", func() bool { return repo.commitCount() == 1 })
}

func TestWatchLimitExhaustionSurfacesAsRepoState(t *testing.T) {
	repo := newTestRepo(t)
	var published *RepoStatus
	w, err := newRepoWatcher(repo.spec("-s", "0.2"),
		func(_ string, s *RepoStatus) { published = s }, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.stopWatching)
	w.addWatchError("/some/dir", syscall.ENOSPC)
	w.publish()
	if published == nil || published.ErrorLabel != "watch failing" {
		t.Fatalf("got %+v", published)
	}
	if !strings.Contains(published.Detail, "max_user_watches") {
		t.Errorf("the fix should be named: %q", published.Detail)
	}
}
