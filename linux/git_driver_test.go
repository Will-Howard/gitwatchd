package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type testRepo struct {
	t    *testing.T
	path string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, path: filepath.Join(t.TempDir(), "repo")}
	os.MkdirAll(r.path, 0o755)
	r.git("init", "-q", "-b", "main")
	r.configureUser()
	return r
}

// A second working copy of a remote (a colleague's machine).
func newCloneOf(t *testing.T, remote *bareRemote) *testRepo {
	t.Helper()
	r := &testRepo{t: t, path: filepath.Join(t.TempDir(), "clone")}
	runCommand("git", []string{"clone", "-q", remote.path, r.path}, "")
	r.configureUser()
	return r
}

func (r *testRepo) configureUser() {
	r.git("config", "user.email", "tests@gitwatchd.local")
	r.git("config", "user.name", "gitwatchd tests")
	r.git("config", "commit.gpgsign", "false")
}

// The spec `gitwatchd add <flags> <path>` would produce.
func (r *testRepo) spec(flags ...string) *RepoSpec {
	r.t.Helper()
	spec, errMsg := parseRepoSpec(append(flags, r.path))
	if spec == nil {
		r.t.Fatalf("spec did not parse: %s", errMsg)
	}
	return spec
}

func (r *testRepo) git(args ...string) string {
	_, out := gitRun(args, r.path, "")
	return out
}

func (r *testRepo) write(file, contents string) {
	r.t.Helper()
	full := filepath.Join(r.path, file)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) commitCount() int {
	n, _ := strconv.Atoi(r.git("rev-list", "--count", "HEAD"))
	return n
}

func (r *testRepo) lastMessage() string { return r.git("log", "-1", "--pretty=%s") }

func (r *testRepo) midMerge() bool {
	_, err := os.Stat(filepath.Join(r.path, ".git", "MERGE_HEAD"))
	return err == nil
}

func (r *testRepo) midRebase() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(r.path, ".git", d)); err == nil {
			return true
		}
	}
	return false
}

func (r *testRepo) addOrigin() *bareRemote {
	remote := newBareRemote(r.t)
	r.git("remote", "add", "origin", remote.path)
	return remote
}

func (r *testRepo) addOriginURL(url string) {
	r.git("remote", "add", "origin", url)
}

func (r *testRepo) setOriginURL(url string) { r.git("remote", "set-url", "origin", url) }

func (r *testRepo) installFailingPreCommitHook(printing string) {
	hook := filepath.Join(r.path, ".git", "hooks", "pre-commit")
	os.WriteFile(hook, []byte("#!/bin/sh\necho '"+printing+"'\nexit 1\n"), 0o755)
}

func (r *testRepo) commit(file, contents, message string) {
	r.write(file, contents)
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
}

// Set main to track origin/main, as a cloned repo would. The branch-less
// `pull --rebase <remote>` needs it.
func (r *testRepo) trackOrigin() {
	r.git("fetch", "-q", "origin")
	r.git("branch", "-q", "--set-upstream-to=origin/main", "main")
}

// Stop this repo mid-merge on a real conflict. Plain git only, so tests
// never exercise the engine during their own setup.
func (r *testRepo) conflictedMerge() {
	r.commit("f.txt", "base\n", "base")
	r.git("checkout", "-q", "-b", "side")
	r.commit("f.txt", "side\n", "side edit")
	r.git("checkout", "-q", "main")
	r.commit("f.txt", "main\n", "main edit")
	r.git("merge", "side") // conflicts, leaving MERGE_HEAD behind
}

type bareRemote struct {
	path string
}

func newBareRemote(t *testing.T) *bareRemote {
	t.Helper()
	b := &bareRemote{path: filepath.Join(t.TempDir(), "remote.git")}
	runCommand("git", []string{"init", "-q", "--bare", "-b", "main", b.path}, "")
	return b
}

func (b *bareRemote) commitCount() int {
	code, out := gitRun([]string{"rev-list", "--count", "main"}, b.path, "")
	if code != 0 {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

func (b *bareRemote) lastMessage() string {
	_, out := gitRun([]string{"log", "-1", "--pretty=%s", "main"}, b.path, "")
	return out
}

// Engine tests drive autoCommit / push against real throwaway git repos,
// asserting both the reported outcome and the repo state left behind
// (the gitwatch parity contract: same commands, same end state).

func TestCleanRepoDoesNothing(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("seed.txt", "v1")
	autoCommit(repo.spec()) // absorb the initial commit
	if got := autoCommit(repo.spec()); got.Kind != Clean {
		t.Errorf("got %+v", got)
	}
	if repo.commitCount() != 1 {
		t.Error("no extra commit should appear")
	}
}

func TestChangesCommitLocallyWithoutRemote(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("notes.txt", "hello")
	if got := autoCommit(repo.spec()); got.Kind != Committed {
		t.Errorf("got %+v", got)
	}
	if repo.commitCount() != 1 {
		t.Errorf("commits = %d", repo.commitCount())
	}
	if !strings.HasPrefix(repo.lastMessage(), "gitwatchd auto-commit") {
		t.Errorf("default message expected, got: %s", repo.lastMessage())
	}
}

func TestCustomMessageAndDateExpansion(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec("-m", "saved on %d", "-d", "+%Y"))
	if !strings.HasPrefix(repo.lastMessage(), "saved on 2") { // "saved on 2026"
		t.Errorf("got: %s", repo.lastMessage())
	}
}

// Sharp corner, kept for upstream parity: -d goes to date(1) raw, so a
// format without a leading + splices an empty date. Git's commit-message
// cleanup then trims the trailing whitespace.
func TestSharpCornerRawDateFormatWithoutPlusSplicesEmptyDate(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec("-m", "at %d", "-d", "%Y"))
	if repo.lastMessage() != "at" {
		t.Errorf("got %q, want %q", repo.lastMessage(), "at")
	}
}

func TestOnlyTheFirstDateTokenIsExpanded(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec("-m", "saved %d then %d", "-d", "+%Y"))
	got := repo.lastMessage()
	if !strings.HasPrefix(got, "saved 2") || !strings.HasSuffix(got, " then %d") {
		t.Errorf("upstream splices the date into the first %%d only, got: %s", got)
	}
}

func TestPushToRemote(t *testing.T) {
	repo := newTestRepo(t)
	origin := repo.addOrigin()
	repo.write("a.txt", "1")
	if got := autoCommit(repo.spec("-r", "origin", "-b", "main")); got.Kind != Pushed {
		t.Errorf("got %+v", got)
	}
	if origin.commitCount() != 1 {
		t.Error("the remote should have the commit")
	}
}

func TestUnreachableRemoteReportsPushFailedButCommitSurvives(t *testing.T) {
	repo := newTestRepo(t)
	repo.addOriginURL(filepath.Join(t.TempDir(), "not-a-remote.git"))
	repo.write("a.txt", "1")
	got := autoCommit(repo.spec("-r", "origin", "-b", "main"))
	if got.Kind != PushFailed {
		t.Fatalf("expected pushFailed, got %+v", got)
	}
	if got.Detail == "" {
		t.Error("the git error is captured for status")
	}
	if repo.commitCount() != 1 {
		t.Error("gitwatch parity: commit stays, only the push failed")
	}
}

func TestPushRetriesStrandedCommitAfterOutage(t *testing.T) {
	repo := newTestRepo(t)
	origin := newBareRemote(t)
	repo.addOriginURL(filepath.Join(t.TempDir(), "offline.git")) // remote "down"
	repo.write("a.txt", "1")
	if got := autoCommit(repo.spec("-r", "origin", "-b", "main")); got.Kind != PushFailed {
		t.Fatalf("setup: expected the first push to fail, got %+v", got)
	}
	repo.setOriginURL(origin.path) // remote "back up"
	if got := push(repo.spec("-r", "origin", "-b", "main")); got.Kind != Pushed {
		t.Errorf("got %+v", got)
	}
	if origin.commitCount() != 1 {
		t.Error("the earlier commit reached the remote")
	}
}

func TestPullFailureWhileOfflineIsTransientNotConflict(t *testing.T) {
	repo := newTestRepo(t)
	repo.addOriginURL(filepath.Join(t.TempDir(), "gone.git"))
	repo.write("a.txt", "1")
	got := autoCommit(repo.spec("-r", "origin", "-b", "main", "-R"))
	if got.Kind != PushFailed {
		t.Fatalf("expected pushFailed, got %+v", got)
	}
	if repo.midRebase() {
		t.Error("no rebase was ever started, so the retry loop may heal this")
	}
}

func TestIdempotentRetryStillReportsPushed(t *testing.T) {
	repo := newTestRepo(t)
	repo.addOrigin()
	repo.write("a.txt", "1")
	spec := repo.spec("-r", "origin", "-b", "main")
	autoCommit(spec)
	if got := push(spec); got.Kind != Pushed { // "Everything up-to-date"
		t.Errorf("got %+v", got)
	}
}

func TestPushFormWithoutBranch(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-r", "origin", "/tmp/x"})
	got := pushArgs("origin", spec)
	if strings.Join(got, " ") != "push origin" {
		t.Errorf("got %v", got)
	}
}

func TestPushFormWithBranch(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec())
	repo.git("checkout", "-q", "-b", "feature")
	got := pushArgs("origin", repo.spec("-r", "origin", "-b", "main"))
	if strings.Join(got, " ") != "push origin feature:main" {
		t.Errorf("got %v", got)
	}
}

func TestPushFormFromDetachedHead(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec())
	repo.git("checkout", "-q", "--detach")
	got := pushArgs("origin", repo.spec("-r", "origin", "-b", "main"))
	if strings.Join(got, " ") != "push origin main" {
		t.Errorf("got %v", got)
	}
}

func TestRefspecEndToEnd(t *testing.T) {
	repo := newTestRepo(t)
	origin := repo.addOrigin()
	repo.write("a.txt", "base\n")
	autoCommit(repo.spec("-r", "origin", "-b", "main"))
	repo.git("checkout", "-q", "-b", "feature")
	repo.write("b.txt", "on feature\n")
	if got := autoCommit(repo.spec("-r", "origin", "-b", "main", "-m", "from feature")); got.Kind != Pushed {
		t.Fatalf("got %+v", got)
	}
	if origin.commitCount() != 2 {
		t.Error("the remote's main received the feature commit")
	}
	if origin.lastMessage() != "from feature" {
		t.Errorf("got %q", origin.lastMessage())
	}
}

func TestRebaseThenPush(t *testing.T) {
	repo := newTestRepo(t)
	origin := repo.addOrigin()
	repo.write("ours.txt", "base\n")
	autoCommit(repo.spec("-r", "origin", "-b", "main"))
	repo.trackOrigin()

	colleague := newCloneOf(t, origin)
	colleague.write("theirs.txt", "from the other machine\n")
	autoCommit(colleague.spec("-r", "origin", "-b", "main", "-m", "made elsewhere"))

	repo.write("ours.txt", "updated here\n")
	got := autoCommit(repo.spec("-r", "origin", "-b", "main", "-R", "-m", "made here"))
	if got.Kind != Pushed {
		t.Fatalf("got %+v", got)
	}
	if origin.commitCount() != 3 {
		t.Error("base, theirs, ours: one linear history")
	}
	if origin.lastMessage() != "made here" {
		t.Error("our commit was rebased on top")
	}
	if !strings.Contains(repo.git("log", "--pretty=%s"), "made elsewhere") {
		t.Error("their commit is now part of our local history")
	}
}

func TestMergeGuardSkipsMidMerge(t *testing.T) {
	repo := newTestRepo(t)
	repo.conflictedMerge()
	if !repo.midMerge() {
		t.Fatal("setup: repo should be mid-merge")
	}
	if got := autoCommit(repo.spec("-M")); got.Kind != SkippedMerge {
		t.Errorf("got %+v", got)
	}
	if !repo.midMerge() {
		t.Error("the merge is left exactly as it was")
	}
}

func TestWithoutMergeGuardTheMergeIsCommitted(t *testing.T) {
	repo := newTestRepo(t)
	repo.conflictedMerge()
	if got := autoCommit(repo.spec()); got.Kind != Committed {
		t.Errorf("got %+v", got)
	}
	if repo.midMerge() {
		t.Error("the commit concluded the merge, as gitwatch would")
	}
}

func TestSubdirectoryTargetCommitsOnlyTheSubtree(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("sub/inner.txt", "v1\n")
	autoCommit(repo.spec())
	repo.write("sub/inner.txt", "v2\n")
	repo.write("outer.txt", "left alone\n")
	spec, _ := parseRepoSpec([]string{filepath.Join(repo.path, "sub")})
	if got := autoCommit(spec); got.Kind != Committed {
		t.Fatalf("got %+v", got)
	}
	if pendingCount(repo.path, "") != 1 {
		t.Error("outer.txt stays uncommitted")
	}
	if repo.git("show", "--name-only", "--pretty=") != "sub/inner.txt" {
		t.Errorf("got %q", repo.git("show", "--name-only", "--pretty="))
	}
}

func TestFileTargetCommitsOnlyThatFile(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "v1\n")
	autoCommit(repo.spec())
	repo.write("a.txt", "v2\n")
	repo.write("b.txt", "left alone\n")
	spec, _ := parseRepoSpec([]string{filepath.Join(repo.path, "a.txt")})
	if got := autoCommit(spec); got.Kind != Committed {
		t.Fatalf("got %+v", got)
	}
	if pendingCount(repo.path, "") != 1 {
		t.Error("b.txt stays uncommitted")
	}
	if repo.git("show", "--name-only", "--pretty=") != "a.txt" {
		t.Errorf("got %q", repo.git("show", "--name-only", "--pretty="))
	}
}

func TestOutsideChangesOnlyReportClean(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("sub/inner.txt", "v1\n")
	autoCommit(repo.spec())
	repo.write("outer.txt", "elsewhere\n")
	before := repo.commitCount()
	spec, _ := parseRepoSpec([]string{filepath.Join(repo.path, "sub")})
	if got := autoCommit(spec); got.Kind != Clean {
		t.Errorf("got %+v", got)
	}
	if repo.commitCount() != before {
		t.Error("no commit should appear")
	}
}

func TestDetachedGitDir(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "base\n")
	autoCommit(repo.spec())
	gitDir := filepath.Join(t.TempDir(), "elsewhere.git")
	if err := os.Rename(filepath.Join(repo.path, ".git"), gitDir); err != nil {
		t.Fatal(err)
	}
	spec := repo.spec("-g", gitDir)
	if !isRepo(repo.path, gitDir) {
		t.Error("the daemon/CLI validation path must accept a -g repo")
	}
	repo.write("a.txt", "changed\n")
	if got := autoCommit(spec); got.Kind != Committed {
		t.Fatalf("got %+v", got)
	}
	if _, out := gitRun([]string{"rev-list", "--count", "HEAD"}, repo.path, gitDir); out != "2" {
		t.Errorf("commits = %s", out)
	}
	if got := autoCommit(spec); got.Kind != Clean {
		t.Error("and the change was fully committed")
	}
}

func TestFailingPreCommitHookReportsCommitFailed(t *testing.T) {
	repo := newTestRepo(t)
	repo.installFailingPreCommitHook("lint says no")
	repo.write("a.txt", "1")
	got := autoCommit(repo.spec())
	if got.Kind != CommitFailed {
		t.Fatalf("expected commitFailed, got %+v", got)
	}
	if !strings.Contains(got.Detail, "lint says no") {
		t.Errorf("hook output surfaces: got %q", got.Detail)
	}
}

// The push still runs after a failed commit (gitwatch parity), but its result
// must not mask the commit failure: the menu/status row has to say "commit
// failing", not "pushed".
func TestFailingCommitIsStillReportedWhenThePushSucceeds(t *testing.T) {
	repo := newTestRepo(t)
	origin := repo.addOrigin()
	repo.write("seed.txt", "1")
	autoCommit(repo.spec("-r", "origin", "-b", "main"))
	repo.installFailingPreCommitHook("lint says no")
	repo.write("a.txt", "2")
	got := autoCommit(repo.spec("-r", "origin", "-b", "main"))
	if got.Kind != CommitFailed {
		t.Fatalf("expected commitFailed, got %+v", got)
	}
	if !strings.Contains(got.Detail, "lint says no") {
		t.Errorf("hook output surfaces: got %q", got.Detail)
	}
	if origin.commitCount() != 1 || repo.commitCount() != 1 {
		t.Error("the blocked commit never happened, and the push had nothing new to send")
	}
}

func TestRebaseConflictIsReportedAndLeftInProgress(t *testing.T) {
	repo := newTestRepo(t)
	origin := repo.addOrigin()
	repo.write("shared.txt", "original\n")
	autoCommit(repo.spec("-r", "origin", "-b", "main"))
	repo.trackOrigin()

	colleague := newCloneOf(t, origin) // someone else pushes first
	colleague.write("shared.txt", "colleague's version\n")
	autoCommit(colleague.spec("-r", "origin", "-b", "main"))

	repo.write("shared.txt", "our conflicting version\n")
	got := autoCommit(repo.spec("-r", "origin", "-b", "main", "-R"))
	if got.Kind != RebaseConflict {
		t.Fatalf("expected rebaseConflict, got %+v", got)
	}
	// gitwatch parity: no abort. The conflicted rebase is left in progress
	// for the user to resolve; we only make it visible in status.
	if !repo.midRebase() {
		t.Error("the conflicted rebase is left in progress")
	}
}
