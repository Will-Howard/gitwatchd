package main

import (
	"fmt"
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

// %B, not %s: -l/-L and -c messages are multi-line.
func (r *testRepo) lastMessage() string { return r.git("log", "-1", "--pretty=%B") }

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
	_, out := gitRun([]string{"log", "-1", "--pretty=%B", "main"}, b.path, "")
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

func numberedLines(prefix string, n int) string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("%s%d", prefix, i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestListChangesEmbedsTheColouredDiff(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "alpha\n")
	autoCommit(repo.spec())
	repo.write("a.txt", "beta\n")
	if got := autoCommit(repo.spec("-l", "10", "-m", "unused")); got.Kind != Committed {
		t.Fatalf("got %+v", got)
	}
	// Exact bytes vary with git version and colour config, so this asserts
	// structure plus the presence of escapes; the parity suite pins the exact
	// bytes differentially against the same git.
	if !strings.Contains(repo.lastMessage(), "\x1b[") {
		t.Errorf("-l keeps git's colour codes, got: %q", repo.lastMessage())
	}
	if !strings.Contains(repo.lastMessage(), "a.txt:1: ") {
		t.Errorf("hunk lines carry path:line:, got: %q", repo.lastMessage())
	}
}

func TestListChangesCutsEachLineAt150Chars(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("long.txt", "short\n")
	autoCommit(repo.spec())
	repo.write("long.txt", strings.Repeat("x", 200)+"\n")
	autoCommit(repo.spec("-l", "0"))
	if !strings.Contains(repo.lastMessage(), strings.Repeat("x", 100)) {
		t.Errorf("the long line is embedded, got: %q", repo.lastMessage())
	}
	if strings.Contains(repo.lastMessage(), strings.Repeat("x", 150)) {
		t.Error("cut at 150 chars of raw diff line (colour codes count)")
	}
	for _, line := range strings.Split(repo.lastMessage(), "\n") {
		if len([]rune(line)) > len("long.txt:1: ")+150 {
			t.Errorf("over-long line: %q", line)
		}
	}
}

func TestListChangesZeroMeansUnlimited(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("n.txt", numberedLines("old", 10))
	autoCommit(repo.spec())
	repo.write("n.txt", numberedLines("new", 10))
	autoCommit(repo.spec("-l", "0"))
	lines := strings.Split(repo.lastMessage(), "\n")
	if len(lines) != 20 {
		t.Fatalf("10 removals + 10 additions, got: %q", repo.lastMessage())
	}
	if !strings.HasPrefix(lines[0], "n.txt:1: ") || !strings.Contains(lines[0], "-old1") {
		t.Errorf("removals keep the hunk's start line, got: %q", lines[0])
	}
	last := lines[19]
	if !strings.HasPrefix(last, "n.txt:10: ") || !strings.Contains(last, "new10") {
		t.Errorf("additions advance the line number, got: %q", last)
	}
}

func TestListChangesAtExactlyTheCapStillEmbeds(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("n.txt", numberedLines("old", 10))
	autoCommit(repo.spec())
	repo.write("n.txt", numberedLines("new", 10))
	autoCommit(repo.spec("-l", "20")) // the cap is "more than", not "at least"
	if n := len(strings.Split(repo.lastMessage(), "\n")); n != 20 {
		t.Errorf("20 diff lines fit in -l 20, got %d: %q", n, repo.lastMessage())
	}
}

func TestListChangesBeyondTheCapFallsBackToTheDiffstat(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("n.txt", numberedLines("old", 10))
	autoCommit(repo.spec())
	repo.write("n.txt", numberedLines("new", 10))
	autoCommit(repo.spec("-l", "5"))
	if !strings.Contains(repo.lastMessage(), "n.txt |") {
		t.Errorf("diffstat summary expected, got: %q", repo.lastMessage())
	}
	if strings.Contains(repo.lastMessage(), "n.txt:1:") {
		t.Error("no embedded diff lines")
	}
	if strings.Contains(repo.lastMessage(), "\x1b") {
		t.Error("upstream's stat command takes no colour flag")
	}
}

func TestListChangesWithOnlyNewFilesListsThem(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("seed.txt", "seed\n")
	autoCommit(repo.spec())
	repo.write("fresh.txt", "hello\n")
	autoCommit(repo.spec("-l", "10"))
	if repo.lastMessage() != "New files added: ?? fresh.txt" {
		t.Errorf("status is taken before git add, got: %q", repo.lastMessage())
	}
}

func TestListChangesReportsADeletedFile(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("doomed.txt", "contents\n")
	repo.write("keep.txt", "kept\n")
	autoCommit(repo.spec())
	os.Remove(filepath.Join(repo.path, "doomed.txt"))
	autoCommit(repo.spec("-l", "10"))
	if repo.lastMessage() != "File doomed.txt deleted or moved." {
		t.Errorf("got: %q", repo.lastMessage())
	}
}

// Upstream hands git the raw bytes it read; git transcodes them into the object,
// so assert on the built message, not the stored one.
func TestListChangesEmbedsANonUTF8Diff(t *testing.T) {
	repo := newTestRepo(t)
	file := filepath.Join(repo.path, "x.txt")
	os.WriteFile(file, []byte("alpha\n"), 0o644)
	autoCommit(repo.spec())
	os.WriteFile(file, []byte{0x62, 0xE9, 0x74, 0x61, 0x0A}, 0o644) // Latin-1 e-acute
	msg := listChangesMessage(repo.spec("-l", "10"))
	if !strings.Contains(msg, "x.txt:1: ") {
		t.Errorf("a non-UTF-8 byte must not empty the diff, got: %q", msg)
	}
	if !strings.Contains(msg, "\xe9") {
		t.Errorf("the invalid byte survives verbatim, got: %q", msg)
	}
}

// Pinned bug-for-bug: git > 2.39 rejects the empty colour argument as a pathspec,
// so every -L commit degrades to the status summary.

func TestPlainListChangesDegradesToTheStatusSummary(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "alpha\n")
	autoCommit(repo.spec())
	repo.write("a.txt", "beta\n")
	if got := autoCommit(repo.spec("-L", "10", "-m", "unused")); got.Kind != Committed {
		t.Fatalf("got %+v", got)
	}
	if repo.lastMessage() != "New files added:  M a.txt" {
		t.Errorf("upstream's degraded -L output, got: %q", repo.lastMessage())
	}
}

func TestPlainListChangesNeverAppliesTheCap(t *testing.T) {
	// The empty diff message always satisfies the length gate, so no -L value
	// (small cap or 0 = unlimited) ever embeds a diff or a diffstat.
	for _, cap := range []string{"5", "0"} {
		repo := newTestRepo(t)
		repo.write("n.txt", numberedLines("old", 10))
		autoCommit(repo.spec())
		repo.write("n.txt", numberedLines("new", 10))
		autoCommit(repo.spec("-L", cap))
		if repo.lastMessage() != "New files added:  M n.txt" {
			t.Errorf("-L %s: upstream's degraded output, got: %q", cap, repo.lastMessage())
		}
	}
}

// -c/-C: the command's stdout is the message, overriding -m, -d and -l/-L.

func TestCommitCommandOutputBecomesTheMessage(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec("-c", "echo release notes", "-m", "unused"))
	if repo.lastMessage() != "release notes" {
		t.Errorf("got %q", repo.lastMessage())
	}
}

func TestCommitCommandIsWordSplitNotShellInterpreted(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	autoCommit(repo.spec("-c", "echo a && echo b"))
	if repo.lastMessage() != "a && echo b" {
		t.Errorf("got %q", repo.lastMessage())
	}
}

func TestCommitCommandOverridesListChanges(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "alpha\n")
	autoCommit(repo.spec())
	repo.write("a.txt", "beta\n")
	autoCommit(repo.spec("-l", "10", "-m", "unused", "-c", "echo from the command"))
	if repo.lastMessage() != "from the command" {
		t.Errorf("-c is applied last, got %q", repo.lastMessage())
	}
}

// Exit status is ignored; printing nothing leaves an empty -m, which git refuses.
func TestFailingCommitCommandAbortsTheCommit(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "1")
	got := autoCommit(repo.spec("-c", "false", "-m", "unused"))
	if got.Kind != CommitFailed {
		t.Fatalf("expected commitFailed, got %+v", got)
	}
	if repo.commitCount() != 0 {
		t.Error("no commit is made with an empty message")
	}
}

func TestPipeChangedFilesFeedsTheCommandStdin(t *testing.T) {
	repo := newTestRepo(t)
	repo.write("a.txt", "v1\n")
	repo.write("b.txt", "v1\n")
	autoCommit(repo.spec())
	repo.write("a.txt", "v2\n")
	repo.write("b.txt", "v2\n")
	autoCommit(repo.spec("-c", "cat", "-C"))
	if repo.lastMessage() != "a.txt\nb.txt" {
		t.Errorf("`git diff --name-only` of the unstaged tree, got %q", repo.lastMessage())
	}
	repo.write("a.txt", "v3\n")
	if got := autoCommit(repo.spec("-c", "cat")); got.Kind != CommitFailed {
		t.Errorf("without -C the command reads /dev/null and prints nothing: %+v", got)
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

// Matching macOS, not upstream: upstream runs the -c command on every cycle,
// even with nothing to commit; we short-circuit first.
func TestCommitCommandDoesNotRunOnACleanCycle(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("a.txt", "v1\n", "seed")
	marker := filepath.Join(t.TempDir(), "ran")
	spec, errMsg := parseRepoSpec([]string{"-c", "touch " + marker, repo.path})
	if spec == nil {
		t.Fatal(errMsg)
	}
	if got := autoCommit(spec); got.Kind != Clean {
		t.Fatalf("got %v", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the -c command ran on a clean cycle")
	}
}
