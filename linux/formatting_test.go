package main

import (
	"testing"
	"time"
)

// What `status` shows, asserted as exact strings: the same rows the macOS
// menu renders, so status reads identically across machines.

func TestHealthyRowIsNameAndBranch(t *testing.T) {
	if got := rowTitle("notes", "main", false, 0, ""); got != "notes · main" {
		t.Errorf("got %q", got)
	}
}

func TestPendingEditsShowACount(t *testing.T) {
	if got := rowTitle("notes", "main", false, 3, ""); got != "notes · main · 3 pending changes" {
		t.Errorf("got %q", got)
	}
	if got := rowTitle("notes", "main", false, 1, ""); got != "notes · main · 1 pending change" {
		t.Errorf("got %q", got)
	}
}

func TestErrorLabelFlagsTheRow(t *testing.T) {
	if got := rowTitle("notes", "main", false, 0, "push failing"); got != "notes · main · ⚠ push failing" {
		t.Errorf("got %q", got)
	}
}

func TestOutcomeLabels(t *testing.T) {
	cases := map[OutcomeKind]string{
		PushFailed:     "push failing",
		RebaseConflict: "rebase conflict",
		CommitFailed:   "commit failing",
		Pushed:         "",
	}
	for kind, want := range cases {
		if got := (Outcome{Kind: kind, Detail: "x"}).ErrorLabel(); got != want {
			t.Errorf("kind %v: got %q, want %q", kind, got, want)
		}
	}
}

func TestConfigErrorRow(t *testing.T) {
	if got := configErrorRow("demo-repo", "repo not found"); got != "⚠ demo-repo · repo not found" {
		t.Errorf("got %q", got)
	}
	longLabel := ""
	for i := 0; i < 100; i++ {
		longLabel += "y"
	}
	if got := configErrorRow(longLabel, "not a git repo"); len([]rune(got)) != 48 {
		t.Errorf("rows stay fixed width; got %d runes", len([]rune(got)))
	}
}

func TestPausedBeatsEveryOtherTail(t *testing.T) {
	if got := rowTitle("notes", "main", true, 3, "push failing"); got != "notes · main · ⏸ paused" {
		t.Errorf("got %q", got)
	}
}

func TestErrorHeadlineCountsAttempts(t *testing.T) {
	if got := errorHeadline("push failing", 1); got != "⚠ push failing" {
		t.Errorf("got %q", got)
	}
	if got := errorHeadline("push failing", 4); got != "⚠ push failing (4 attempts)" {
		t.Errorf("got %q", got)
	}
}

func TestRetryLine(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	next := now.Add(180 * time.Second)
	if got := retryLine(now.Add(-120*time.Second), &next, now); got != "tried 2m ago · retrying in 3m" {
		t.Errorf("got %q", got)
	}
}

func TestRetryLineWithoutTimer(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if got := retryLine(now.Add(-30*time.Second), nil, now); got != "tried 30s ago · retries on next change" {
		t.Errorf("got %q", got)
	}
}

func TestTruncation(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	if got := truncated(long, 20); len([]rune(got)) != 20 {
		t.Errorf("got %d runes", len([]rune(got)))
	}
	if got := truncated("short", 20); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second,
		240 * time.Second, 300 * time.Second, 300 * time.Second}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w {
			t.Errorf("after %d failures: got %v, want %v", i+1, got, w)
		}
	}
}

func TestErrorSummaryPicksRejectionLine(t *testing.T) {
	out := `To /tmp/origin.git
 ! [rejected]        main -> main (fetch first)
error: failed to push some refs to '/tmp/origin.git'
hint: Updates were rejected because the remote contains work that you do not have`
	if got := errorSummary(out); got != "! [rejected]        main -> main (fetch first)" {
		t.Errorf("got %q", got)
	}
}

func TestErrorSummaryPicksFatalLine(t *testing.T) {
	out := "fatal: unable to access 'https://x/': Could not resolve host\nsome trailer"
	if got := errorSummary(out); got != "fatal: unable to access 'https://x/': Could not resolve host" {
		t.Errorf("got %q", got)
	}
}

func TestErrorSummaryFallsBackToLastLine(t *testing.T) {
	if got := errorSummary("lint says no"); got != "lint says no" {
		t.Errorf("got %q", got)
	}
}
