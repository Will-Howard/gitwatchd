package main

import (
	"testing"
)

// Pause is config-level state: a --paused token on the repo's line, so a
// paused repo stays paused across daemon and machine restarts. These tests
// pin the parsing and the pure line-rewrite pause/resume use.

func TestPausedParsesAlongsideGitwatchFlags(t *testing.T) {
	spec, errMsg := parseRepoSpec([]string{"--paused", "-s", "2", "/tmp/x"})
	if spec == nil {
		t.Fatal(errMsg)
	}
	if !spec.Paused || spec.Settle != 2 {
		t.Errorf("got %+v", spec)
	}
}

func TestPauseRewritesAndResumeRestores(t *testing.T) {
	line := "-s 2 -r origin -b main -R /tmp/x"
	pausedLine, ok := togglingPaused(line, "/tmp/x", true)
	if !ok || pausedLine != "--paused -s 2 -r origin -b main -R /tmp/x" {
		t.Fatalf("got %q", pausedLine)
	}
	resumed, ok := togglingPaused(pausedLine, "/tmp/x", false)
	if !ok || resumed != line {
		t.Errorf("got %q", resumed)
	}
}

func TestQuotedMessagesSurviveTheRewrite(t *testing.T) {
	got, ok := togglingPaused(`-m "two words" /tmp/x`, "/tmp/x", true)
	if !ok || got != `--paused -m "two words" /tmp/x` {
		t.Errorf("got %q", got)
	}
}

func TestEmbeddedQuotesSurviveTheRewrite(t *testing.T) {
	got, ok := togglingPaused(`-m 'say "hi"' /tmp/x`, "/tmp/x", true)
	if !ok || got != `--paused -m 'say "hi"' /tmp/x` {
		t.Errorf("got %q", got)
	}
	got, ok = togglingPaused(`-m "don't" /tmp/x`, "/tmp/x", true)
	if !ok || got != `--paused -m "don't" /tmp/x` {
		t.Errorf("got %q", got)
	}
}

func TestOtherLinesAreLeftAlone(t *testing.T) {
	if _, ok := togglingPaused("-s 2 /tmp/other", "/tmp/x", true); ok {
		t.Error("a line for another repo must not be rewritten")
	}
}

func TestNoOpToggleChangesNothing(t *testing.T) {
	if _, ok := togglingPaused("--paused /tmp/x", "/tmp/x", true); ok {
		t.Error("already paused: nothing to do")
	}
	if _, ok := togglingPaused("/tmp/x", "/tmp/x", false); ok {
		t.Error("already watching: nothing to do")
	}
}

func TestTokenizeRespectsQuotes(t *testing.T) {
	got := tokenize(`-m "two words" -x '\.log$' /tmp/x`)
	want := []string{"-m", "two words", "-x", `\.log$`, "/tmp/x"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
