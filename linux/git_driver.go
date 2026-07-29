package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Thin wrapper around git.

// What one auto-commit cycle (or push retry) accomplished.
type CommitOutcome int

const (
	Clean          CommitOutcome = iota // nothing to commit
	SkippedMerge                        // -M: merge in progress, cycle skipped
	Committed                           // committed; no remote configured
	Pushed                              // committed and pushed
	CommitFailed                        // Detail carries the git error line
	RebaseConflict                      // -R: pull --rebase hit a conflict
	PushFailed
)

type Outcome struct {
	Kind   CommitOutcome
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

// $() capture: stdout only, trailing newlines stripped, leading whitespace kept.
func gitCapture(args []string, dir string, gitDir string) string {
	return strings.TrimRight(string(gitCaptureRaw(args, dir, gitDir)), "\n")
}

func gitCaptureRaw(args []string, dir string, gitDir string) []byte {
	full := args
	if gitDir != "" {
		full = append([]string{"--work-tree", dir, "--git-dir", gitDir}, args...)
	}
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, _ := cmd.Output() // stdout even when git failed, as command substitution keeps
	return out
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

var (
	removedFileHeader = regexp.MustCompile(`--- (?:a/)?([^ \t\x1b]+)`)
	addedFileHeader   = regexp.MustCompile(`\+\+\+ (?:b/)?([^ \t\x1b]+)`)
	hunkHeader        = regexp.MustCompile(`@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)
	changedLine       = regexp.MustCompile(`^(?:\x1b\[[0-9;]+m)*([ +-])`)
)

// Bash's ${s:0:n}: bytes that are not valid UTF-8 count as one character each.
func cutToChars(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// Upstream's diff-lines filter: hunk lines become "path:line: content".
func diffLines(diff string) string {
	path, line, previousPath := "", "", ""
	var out []string
	for _, reply := range strings.Split(diff, "\n") {
		if m := removedFileHeader.FindStringSubmatch(reply); m != nil {
			previousPath = m[1]
		} else if m := addedFileHeader.FindStringSubmatch(reply); m != nil {
			path = m[1]
		} else if m := hunkHeader.FindStringSubmatch(reply); m != nil {
			line = m[1]
		} else if m := changedLine.FindStringSubmatch(reply); m != nil {
			reply = cutToChars(reply, 150)
			if path == "/dev/null" {
				out = append(out, "File "+previousPath+" deleted or moved.")
				continue
			}
			out = append(out, path+":"+line+": "+reply)
			if m[1] != "-" {
				n, _ := strconv.Atoi(line)
				line = strconv.Itoa(n + 1)
			}
		}
	}
	return strings.Join(out, "\n")
}

// -L's empty colour argument trips git > 2.39: the known bug the help text documents.
func listChangesMessage(spec *RepoSpec) string {
	dir := spec.WorkDir()
	colorArg := ""
	if spec.ListChangesColor {
		colorArg = "--color=always"
	}
	msg := diffLines(gitCapture([]string{"diff", "-U0", colorArg}, dir, spec.GitDir))
	length := 0
	if spec.ListChanges >= 1 && msg != "" {
		length = len(strings.Split(msg, "\n"))
	}
	if length <= spec.ListChanges {
		if msg != "" {
			return msg
		}
		return "New files added: " + gitCapture([]string{"status", "-s"}, dir, spec.GitDir)
	}
	var stat []string
	for _, l := range strings.Split(gitCapture([]string{"diff", "--stat"}, dir, spec.GitDir), "\n") {
		if strings.Contains(l, "|") {
			stat = append(stat, l)
		}
	}
	return strings.Join(stat, "\n")
}

// Word-split into an argv, no shell; stdout is the message, exit status ignored.
func commitCommandOutput(spec *RepoSpec) string {
	argv := strings.FieldsFunc(spec.CommitCommand, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' // bash's default IFS
	})
	if len(argv) == 0 {
		return ""
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.WorkDir()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr // upstream leaves the command's stderr on the terminal
	if spec.PipeChangedFiles {
		cmd.Stdin = bytes.NewReader(gitCaptureRaw([]string{"diff", "--name-only"}, spec.WorkDir(), spec.GitDir))
	}
	cmd.Run()
	// $() drops NUL bytes and trailing newlines.
	out := bytes.ReplaceAll(stdout.Bytes(), []byte{0}, nil)
	return strings.TrimRight(string(out), "\n")
}

// Stage all, commit (honoring -m/-d/-l/-L/-c/-C/-M), then optionally pull
// --rebase (-R) and push (-r/-b). One gitwatch cycle.
func autoCommit(spec *RepoSpec) Outcome {
	dir := spec.WorkDir()
	if spec.NoMergeCommit && hasMergeInProgress(dir, spec.GitDir) {
		return Outcome{Kind: SkippedMerge}
	}
	if pendingCount(dir, spec.GitDir) == 0 {
		return Outcome{Kind: Clean}
	}

	// 1. Message before add, so -l/-L and -c/-C see the unstaged tree; each overrides the last.
	msg := strings.Replace(spec.Message, "%d", formattedDate(spec.DateFormat), 1)
	if spec.ListChanges >= 0 {
		msg = listChangesMessage(spec)
	}
	if spec.CommitCommand != "" {
		msg = commitCommandOutput(spec)
	}

	// 2. Stage upstream's GIT_ADD_ARGS: "--all ." scoped to the target
	// directory, or just the file for a file target.
	addTarget := "."
	if spec.IsFileTarget() {
		addTarget = spec.Path
	}
	gitRun([]string{"add", "--all", addTarget}, dir, spec.GitDir)

	// 3. Commit. commitOutcome is reporting only; it never gates step 4.
	code, out := gitRun([]string{"commit", "-m", msg}, dir, spec.GitDir)
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

	// 4. Pull (-R) and push regardless of the commit result (gitwatch
	// parity), so a repo whose commit a hook blocks still pushes what is
	// already committed. Without a remote, skip push() entirely: its
	// no-remote return would mask a commit failure.
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
