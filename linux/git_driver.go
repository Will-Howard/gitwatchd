package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Thin wrapper around git.

// What one auto-commit cycle (or push retry) accomplished. The git commands
// and their order are gitwatch's; this only reports the result.
type OutcomeKind int

const (
	Clean          OutcomeKind = iota // nothing to commit
	SkippedMerge                      // -M: merge in progress, cycle skipped
	Committed                         // committed; no remote configured
	Pushed                            // committed and pushed
	CommitFailed                      // Detail carries the git error line
	RebaseConflict                    // -R: pull --rebase hit a conflict
	PushFailed
)

type Outcome struct {
	Kind   OutcomeKind
	Detail string
}

// Menu-row label when this outcome is an error state, "" when healthy.
func (o Outcome) ErrorLabel() string {
	switch o.Kind {
	case PushFailed:
		return "push failing"
	case RebaseConflict:
		return "rebase conflict"
	case CommitFailed:
		return "commit failing"
	}
	return ""
}

func runCommand(exe string, args []string, cwd string) (int, string) {
	cmd := exec.Command(exe, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), text
		}
		return -1, err.Error()
	}
	return 0, text
}

func gitRun(args []string, dir string, gitDir string) (int, string) {
	full := args
	// -g repos get upstream's exact override on every call:
	// `git --work-tree $TARGETDIR --git-dir $GIT_DIR <cmd>`.
	if gitDir != "" {
		full = append([]string{"--work-tree", dir, "--git-dir", gitDir}, args...)
	}
	return runCommand("git", full, dir)
}

func isRepo(dir string, gitDir string) bool {
	_, out := gitRun([]string{"rev-parse", "--is-inside-work-tree"}, dir, gitDir)
	return out == "true"
}

func currentBranch(dir string, gitDir string) string {
	code, out := gitRun([]string{"rev-parse", "--abbrev-ref", "HEAD"}, dir, gitDir)
	if code != 0 {
		return "?"
	}
	return out
}

func pendingCount(dir string, gitDir string) int {
	_, out := gitRun([]string{"status", "--porcelain"}, dir, gitDir)
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func lastCommitSummary(dir string, gitDir string) string {
	code, out := gitRun([]string{"log", "-1", "--pretty=%cr · %s"}, dir, gitDir)
	if code != 0 {
		return "no commits yet"
	}
	return out
}

func gitStateExists(names []string, dir string, gitDir string) bool {
	code, top := gitRun([]string{"rev-parse", "--git-dir"}, dir, gitDir)
	if code != 0 {
		return false
	}
	gitDirPath := top
	if !filepath.IsAbs(gitDirPath) {
		gitDirPath = filepath.Join(dir, gitDirPath)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(gitDirPath, name)); err == nil {
			return true
		}
	}
	return false
}

// gitwatch's is_merging: MERGE_HEAD only (a rebase does not count, upstream).
func hasMergeInProgress(dir string, gitDir string) bool {
	return gitStateExists([]string{"MERGE_HEAD"}, dir, gitDir)
}

func hasRebaseInProgress(dir string, gitDir string) bool {
	return gitStateExists([]string{"rebase-merge", "rebase-apply"}, dir, gitDir)
}

// Stage all, commit (honoring -m/-d/-M), then optionally pull --rebase (-R)
// and push (-r/-b). One gitwatch cycle.
func autoCommit(spec *RepoSpec) Outcome {
	dir := spec.WorkDir()
	if spec.NoMergeCommit && hasMergeInProgress(dir, spec.GitDir) {
		return Outcome{Kind: SkippedMerge}
	}
	if pendingCount(dir, spec.GitDir) == 0 {
		return Outcome{Kind: Clean}
	}

	// Upstream builds the message before `git add`, and so do we: the -c/-C
	// message command (not ported yet) has to see the unstaged tree.
	// Upstream's ${COMMITMSG/\%d/...}: the date splices into the first %d only.
	msg := strings.Replace(spec.Message, "%d", formattedDate(spec.DateFormat), 1)

	// Upstream's GIT_ADD_ARGS: "--all ." scoped to the target directory,
	// or just the file for a file target.
	addTarget := "."
	if spec.IsFileTarget() {
		addTarget = spec.Path
	}
	gitRun([]string{"add", "--all", addTarget}, dir, spec.GitDir)
	code, out := gitRun([]string{"commit", "-m", msg}, dir, spec.GitDir)

	// gitwatch runs the pull (-R) and the push unconditionally after the
	// commit, whether or not it succeeded, so a repo whose commit a hook
	// blocks still pushes what is already committed. commitOutcome is
	// reporting only.
	commitOutcome := Outcome{Kind: Committed}
	if code != 0 {
		// Repo-wide changes outside the watched subtree stage nothing.
		if strings.Contains(out, "nothing to commit") ||
			strings.Contains(out, "nothing added to commit") ||
			strings.Contains(out, "no changes added to commit") {
			commitOutcome = Outcome{Kind: Clean}
		} else {
			commitOutcome = Outcome{Kind: CommitFailed, Detail: errorSummary(out)}
		}
	}

	// No remote: never call push(), whose no-remote return would mask a
	// commit failure.
	if spec.Remote == "" {
		return commitOutcome
	}
	pushed := push(spec)
	if commitOutcome.Kind == CommitFailed {
		return commitOutcome
	}
	return pushed
}

func push(spec *RepoSpec) Outcome {
	if spec.Remote == "" {
		return Outcome{Kind: Committed}
	}
	dir := spec.WorkDir()
	pullFailure := ""
	if spec.Rebase {
		code, out := gitRun([]string{"pull", "--rebase", spec.Remote}, dir, spec.GitDir)
		if code != 0 {
			pullFailure = errorSummary(out)
		}
	}
	code, out := gitRun(pushArgs(spec.Remote, spec), dir, spec.GitDir)

	if pullFailure != "" {
		// A conflict leaves a rebase in progress and needs the user;
		// anything else (offline, auth) is transient and worth retrying.
		if hasRebaseInProgress(dir, spec.GitDir) {
			return Outcome{Kind: RebaseConflict, Detail: pullFailure}
		}
		return Outcome{Kind: PushFailed, Detail: pullFailure}
	}
	if code != 0 {
		return Outcome{Kind: PushFailed, Detail: errorSummary(out)}
	}
	return Outcome{Kind: Pushed}
}

// gitwatch's push command: without -b, a bare `push <remote>` (git's
// push.default decides); with -b, push `<current>:<branch>`, or just
// `<branch>` from a detached HEAD.
func pushArgs(remote string, spec *RepoSpec) []string {
	if spec.Branch == "" {
		return []string{"push", remote}
	}
	code, head := gitRun([]string{"symbolic-ref", "HEAD"}, spec.WorkDir(), spec.GitDir)
	if code != 0 {
		return []string{"push", remote, spec.Branch}
	}
	current := strings.Replace(head, "refs/heads/", "", 1)
	return []string{"push", remote, current + ":" + spec.Branch}
}

func errorSummary(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "error:") || strings.HasPrefix(l, "fatal:") ||
			strings.HasPrefix(l, "! [rejected]") || strings.HasPrefix(l, "! [remote rejected]") {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return "unknown git error"
}

// The -d value passes to date(1) verbatim (the user includes the leading +), to
// match the upstream gitwatch
func formattedDate(fmt string) string {
	code, out := runCommand("date", []string{fmt}, "")
	if code != 0 {
		return ""
	}
	return out
}
