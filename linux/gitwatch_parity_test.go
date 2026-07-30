package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Differential parity tests: the same scenario runs through upstream
// gitwatch.sh (the model) and through our engine, each on its own
// identically-built world, and the observable git state afterwards must be
// identical. Scenario setup uses only plain git commands, so neither
// implementation touches the world until the measured cycle.
//
// Determinism: gitwatch's -f performs one commit cycle before entering its
// watch loop, and GW_INW_BIN lets us point the watcher at a stub that exits
// immediately, so the script does exactly one cycle and terminates. Every
// scenario passes an explicit -m without %d, since default messages and
// timestamps intentionally differ.

// Everything a user could observe about a world after one cycle.
type repoState struct {
	commitCount       int
	lastMessage       string
	pendingChanges    int
	branch            string
	midMerge          bool
	midRebase         bool
	remoteCommitCount int // -1 when the scenario has no remote
	remoteLastMessage string
}

func (s repoState) String() string {
	return fmt.Sprintf("commits=%d last=%q pending=%d branch=%s midMerge=%v midRebase=%v remote(commits=%d last=%q)",
		s.commitCount, s.lastMessage, s.pendingChanges, s.branch,
		s.midMerge, s.midRebase, s.remoteCommitCount, s.remoteLastMessage)
}

func stateOf(repo *testRepo, remote *bareRemote) repoState {
	s := repoState{
		commitCount:       repo.commitCount(),
		lastMessage:       repo.lastMessage(),
		pendingChanges:    pendingCount(repo.path, ""),
		branch:            currentBranch(repo.path, ""),
		midMerge:          repo.midMerge(),
		midRebase:         repo.midRebase(),
		remoteCommitCount: -1,
	}
	if remote != nil {
		s.remoteCommitCount = remote.commitCount()
		s.remoteLastMessage = remote.lastMessage()
	}
	return s
}

// The upstream script, vendored once for the whole repo in oracle/; both
// implementations measure themselves against that same copy.
func gitwatchScript(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	script := filepath.Join(wd, "..", "oracle", "gitwatch.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("vendored gitwatch.sh not found at %s", script)
	}
	return script
}

// A watcher stub that exits immediately: gitwatch runs its -f startup
// commit, the watch pipe hits EOF, and the script terminates.
func stubWatcher(t *testing.T) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "inotifywait")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// Run upstream gitwatch for exactly one commit cycle on `target`.
func runOneCycle(t *testing.T, flags []string, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append([]string{gitwatchScript(t), "-f"}, flags...)
	args = append(args, target)
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Env = append(os.Environ(), "GW_INW_BIN="+stubWatcher(t))
	cmd.Run() // fire-and-forget, like gitwatch itself
}

// Build two identical worlds, run the model on one and our engine on the
// other with the same flags, and return both fingerprints.
func twins(t *testing.T, flags []string, withRemote bool, targetSuffix string,
	setup func(*testRepo, *bareRemote)) (model, ours repoState) {
	t.Helper()
	target := func(repo *testRepo) string {
		if targetSuffix != "" {
			return filepath.Join(repo.path, targetSuffix)
		}
		return repo.path
	}

	a := newTestRepo(t)
	var ra *bareRemote
	if withRemote {
		ra = a.addOrigin()
	}
	setup(a, ra)
	runOneCycle(t, flags, target(a))
	model = stateOf(a, ra)

	b := newTestRepo(t)
	var rb *bareRemote
	if withRemote {
		rb = b.addOrigin()
	}
	setup(b, rb)
	spec, errMsg := parseRepoSpec(append(flags, target(b)))
	if spec == nil {
		t.Fatal(errMsg)
	}
	autoCommit(spec)
	ours = stateOf(b, rb)
	return model, ours
}

func expectParity(t *testing.T, model, ours repoState) {
	t.Helper()
	if model != ours {
		t.Errorf("state diverged\n  gitwatch: %v\n  ours:     %v", model, ours)
	}
}

// Plain-git scenario builders (no engine involvement).
func seed(repo *testRepo) {
	repo.write("seed.txt", "seed\n")
	repo.git("add", "-A")
	repo.git("commit", "-q", "-m", "seed")
}

func seedAndPush(repo *testRepo) {
	seed(repo)
	repo.git("push", "-q", "origin", "main")
	repo.trackOrigin()
}

func colleaguePushes(t *testing.T, remote *bareRemote, file, message string) {
	colleague := newCloneOf(t, remote)
	colleague.write(file, "from the other machine\n")
	colleague.git("add", "-A")
	colleague.git("commit", "-q", "-m", message)
	colleague.git("push", "-q", "origin", "main")
}

func TestParityCleanRepo(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
	})
	expectParity(t, model, ours)
	if ours.commitCount != 1 {
		t.Errorf("neither side commits anything: %v", ours)
	}
}

func TestParityPlainCommit(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
		repo.write("notes.txt", "hello\n")
	})
	expectParity(t, model, ours)
	if ours.commitCount != 2 || ours.lastMessage != "cycle" || ours.pendingChanges != 0 {
		t.Errorf("same commit, same message, clean tree afterwards: %v", ours)
	}
}

// Sharp corner, kept for upstream parity: -d passes to date(1) raw and only
// the first %d is spliced. The year-only format keeps the spliced date
// identical across the two runs, so the fingerprints compare exactly.
func TestParitySharpCornerRawDateFormatAndFirstTokenOnly(t *testing.T) {
	model, ours := twins(t, []string{"-m", "at %d then %d", "-d", "+%Y"}, false, "",
		func(repo *testRepo, _ *bareRemote) {
			seed(repo)
			repo.write("notes.txt", "hello\n")
		})
	expectParity(t, model, ours)
	if !strings.HasPrefix(ours.lastMessage, "at 2") || !strings.HasSuffix(ours.lastMessage, " then %d") {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// These scenarios compare whole commit-message bodies against the oracle.

func TestParityListChangesEmbedsTheDiff(t *testing.T) {
	model, ours := twins(t, []string{"-l", "10"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
		repo.write("notes.txt", "hello\n")
		repo.git("add", "-A")
		repo.git("commit", "-q", "-m", "tracked")
		repo.write("notes.txt", "hello again\n")
	})
	expectParity(t, model, ours)
	if !strings.Contains(ours.lastMessage, "notes.txt:1: ") || !strings.Contains(ours.lastMessage, "\x1b[") {
		t.Errorf("both sides embed the coloured diff: %q", ours.lastMessage)
	}
}

func TestParityListChangesCutsLongLines(t *testing.T) {
	model, ours := twins(t, []string{"-l", "0"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.commit("long.txt", "short\n", "seed")
		repo.write("long.txt", strings.Repeat("x", 200)+"\n")
	})
	expectParity(t, model, ours)
	if strings.Contains(ours.lastMessage, strings.Repeat("x", 150)) {
		t.Errorf("both sides cut at 150 characters: %q", ours.lastMessage)
	}
}

func TestParityListChangesFallsBackToTheDiffstat(t *testing.T) {
	model, ours := twins(t, []string{"-l", "5"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.commit("n.txt", numberedLines("old", 10), "seed")
		repo.write("n.txt", numberedLines("new", 10))
	})
	expectParity(t, model, ours)
	if !strings.Contains(ours.lastMessage, "n.txt |") {
		t.Errorf("20 diff lines exceed -l 5, so both sides summarise: %q", ours.lastMessage)
	}
}

func TestParityListChangesWithOnlyNewFiles(t *testing.T) {
	model, ours := twins(t, []string{"-l", "10"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
		repo.write("fresh.txt", "hello\n")
	})
	expectParity(t, model, ours)
	if ours.lastMessage != "New files added: ?? fresh.txt" {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// -L's empty colour argument makes both sides run `git diff -U0 ""`, which
// this git rejects, so both degrade to the status summary.
func TestParityPlainListChangesDegrades(t *testing.T) {
	model, ours := twins(t, []string{"-L", "10"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.commit("a.txt", "alpha\n", "seed")
		repo.write("a.txt", "beta\n")
	})
	expectParity(t, model, ours)
	if ours.lastMessage != "New files added:  M a.txt" {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// "&&" is an argument to echo, not a shell operator.
func TestParityCommitCommandIsWordSplit(t *testing.T) {
	model, ours := twins(t, []string{"-c", "echo a && echo b"}, false, "",
		func(repo *testRepo, _ *bareRemote) {
			seed(repo)
			repo.write("notes.txt", "hello\n")
		})
	expectParity(t, model, ours)
	if ours.lastMessage != "a && echo b" {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// The trailing -f is upstream's `C:` bug showing through: -C swallows the next
// token, so feeding it a duplicate -f keeps both sides' effective flags equal.
func TestParityCommitCommandReadsChangedFilesFromStdin(t *testing.T) {
	model, ours := twins(t, []string{"-c", "cat", "-C", "-f"}, false, "",
		func(repo *testRepo, _ *bareRemote) {
			repo.commit("a.txt", "v1\n", "seed")
			repo.commit("b.txt", "v1\n", "seed b")
			repo.write("a.txt", "v2\n")
			repo.write("b.txt", "v2\n")
		})
	expectParity(t, model, ours)
	if ours.lastMessage != "a.txt\nb.txt" {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// Exit status is ignored; printing nothing leaves an empty -m on both sides.
func TestParityFailingCommitCommandCommitsNothing(t *testing.T) {
	model, ours := twins(t, []string{"-c", "false"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
		repo.write("notes.txt", "hello\n")
	})
	expectParity(t, model, ours)
	if ours.commitCount != 1 || ours.pendingChanges == 0 {
		t.Errorf("the change stays uncommitted on both sides: %v", ours)
	}
}

// -v turns on upstream's tracing and changes nothing about the repo.
func TestParityVerboseChangesNothing(t *testing.T) {
	model, ours := twins(t, []string{"-v", "-m", "cycle"}, false, "", func(repo *testRepo, _ *bareRemote) {
		seed(repo)
		repo.write("notes.txt", "hello\n")
	})
	expectParity(t, model, ours)
	if ours.commitCount != 2 || ours.lastMessage != "cycle" {
		t.Errorf("got %v", ours)
	}
}

func TestParityPushToRemote(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-r", "origin", "-b", "main"}, true, "",
		func(repo *testRepo, _ *bareRemote) {
			seedAndPush(repo)
			repo.write("notes.txt", "hello\n")
		})
	expectParity(t, model, ours)
	if ours.remoteCommitCount != 2 || ours.remoteLastMessage != "cycle" {
		t.Errorf("both push the commit: %v", ours)
	}
}

func TestParityUnreachableRemote(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-r", "origin", "-b", "main"}, true, "",
		func(repo *testRepo, _ *bareRemote) {
			seed(repo)
			repo.setOriginURL(filepath.Join(t.TempDir(), "gone.git"))
			repo.write("notes.txt", "hello\n")
		})
	expectParity(t, model, ours)
	if ours.commitCount != 2 {
		t.Error("fire-and-forget: the commit still happens")
	}
}

// Upstream runs the pull and the push after `git commit` whether or not the
// commit succeeded, so a repo whose commit a hook blocks still pushes what is
// already committed. The seed here is deliberately left unpushed, which is what
// lets the two worlds disagree if we ever short-circuit on a failed commit.
func TestParityFailedCommitStillPushes(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-r", "origin", "-b", "main"}, true, "",
		func(repo *testRepo, _ *bareRemote) {
			seed(repo) // committed locally, never pushed
			repo.installFailingPreCommitHook("lint says no")
			repo.write("notes.txt", "hello\n")
		})
	expectParity(t, model, ours)
	if ours.remoteCommitCount != 1 {
		t.Errorf("the already-committed seed reaches the remote on both sides: %v", ours)
	}
	if ours.commitCount != 1 || ours.pendingChanges == 0 {
		t.Errorf("the hook blocked the new commit on both sides: %v", ours)
	}
}

func TestParityMergeGuard(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-M"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.conflictedMerge()
	})
	expectParity(t, model, ours)
	if !ours.midMerge {
		t.Error("the merge is left untouched on both sides")
	}
}

func TestParityMergeCommittedWithoutGuard(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.conflictedMerge()
	})
	expectParity(t, model, ours)
	if ours.midMerge {
		t.Error("the cycle concluded the merge on both sides")
	}
}

func TestParityRebaseHappyPath(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-r", "origin", "-b", "main", "-R"}, true, "",
		func(repo *testRepo, remote *bareRemote) {
			seedAndPush(repo)
			colleaguePushes(t, remote, "theirs.txt", "made elsewhere")
			repo.write("ours.txt", "made here\n")
		})
	expectParity(t, model, ours)
	if ours.remoteCommitCount != 3 {
		t.Error("seed, theirs, ours: one linear history")
	}
	if ours.remoteLastMessage != "cycle" {
		t.Errorf("got %q", ours.remoteLastMessage)
	}
}

func TestParitySubdirectoryTarget(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle"}, false, "sub", func(repo *testRepo, _ *bareRemote) {
		repo.write("sub/inner.txt", "v1\n")
		repo.git("add", "-A")
		repo.git("commit", "-q", "-m", "seed")
		repo.write("sub/inner.txt", "v2\n")
		repo.write("outer.txt", "left alone\n")
	})
	expectParity(t, model, ours)
	if ours.commitCount != 2 || ours.pendingChanges != 1 {
		t.Errorf("outer.txt stays uncommitted on both sides: %v", ours)
	}
}

func TestParityFileTarget(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle"}, false, "a.txt", func(repo *testRepo, _ *bareRemote) {
		repo.write("a.txt", "v1\n")
		repo.git("add", "-A")
		repo.git("commit", "-q", "-m", "seed")
		repo.write("a.txt", "v2\n")
		repo.write("b.txt", "left alone\n")
	})
	expectParity(t, model, ours)
	if ours.commitCount != 2 || ours.pendingChanges != 1 {
		t.Errorf("b.txt stays uncommitted on both sides: %v", ours)
	}
}

func TestParityRebaseConflictLeftInProgress(t *testing.T) {
	model, ours := twins(t, []string{"-m", "cycle", "-r", "origin", "-b", "main", "-R"}, true, "",
		func(repo *testRepo, remote *bareRemote) {
			seedAndPush(repo)
			colleaguePushes(t, remote, "seed.txt", "conflicting edit")
			repo.write("seed.txt", "our conflicting edit\n")
		})
	expectParity(t, model, ours)
	if !ours.midRebase {
		t.Error("both sides stop mid-rebase; -M is the only guard")
	}
}

// Pinned upstream bug: the unanchored header regexes run before the content one,
// so a changed line whose text contains "--- x" is eaten and corrupts the path.
func TestParityListChangesEatsContentLinesResemblingHeaders(t *testing.T) {
	model, ours := twins(t, []string{"-l", "10"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.commit("a.txt", "alpha\n", "seed")
		repo.write("a.txt", "alpha\n--- section two\nbeta\n")
	})
	expectParity(t, model, ours)
	if strings.Contains(ours.lastMessage, "section") || !strings.Contains(ours.lastMessage, "beta") {
		t.Errorf("got %q", ours.lastMessage)
	}
}

// Pinned upstream bug: the 150-char cut counts colour escapes and can land inside
// one, leaving a dangling ESC in the commit message.
func TestParityListChangesCutCanSplitAnEscape(t *testing.T) {
	model, ours := twins(t, []string{"-l", "10"}, false, "", func(repo *testRepo, _ *bareRemote) {
		repo.commit("a.txt", "short\n", "seed")
		// 135 content chars put char 150 inside this git's trailing \x1b[m.
		repo.write("a.txt", strings.Repeat("x", 135)+"\n")
	})
	expectParity(t, model, ours)
	if !strings.HasSuffix(ours.lastMessage, "\x1b") && !strings.HasSuffix(ours.lastMessage, "\x1b[") {
		t.Errorf("expected a dangling escape, got %q", ours.lastMessage)
	}
}

// Pinned upstream quirk: the -l diff is repo-wide even for a file target, so the
// message can describe changes the commit does not include.
func TestParityListChangesOnAFileTargetEmbedsTheWholeRepoDiff(t *testing.T) {
	model, ours := twins(t, []string{"-l", "10"}, false, "a.txt", func(repo *testRepo, _ *bareRemote) {
		repo.commit("a.txt", "v1\n", "seed")
		repo.commit("b.txt", "v1\n", "seed b")
		repo.write("a.txt", "v2\n")
		repo.write("b.txt", "v2\n")
	})
	expectParity(t, model, ours)
	if !strings.Contains(ours.lastMessage, "b.txt:") || ours.pendingChanges == 0 {
		t.Errorf("the message names b.txt yet b.txt stays uncommitted: %v", ours)
	}
}
