package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI contract tests run the real cliRun against a scratch config directory
// (never the user's ~/.gitwatchd) with daemon-spawning disabled.

func withTemporaryConfig(t *testing.T) {
	t.Helper()
	spawnsDaemon = false
	t.Cleanup(func() { spawnsDaemon = true })
	dir := t.TempDir()
	t.Setenv("GITWATCHD_CONFIG", filepath.Join(dir, "gitwatchd"))
	t.Setenv("GITWATCHD_STATE_DIR", filepath.Join(dir, "state"))
}

func TestAddRefusesAMissingPath(t *testing.T) {
	withTemporaryConfig(t)
	if cliRun([]string{"add", filepath.Join(t.TempDir(), "no-such-dir")}) != 1 {
		t.Error("expected failure")
	}
	if len(configSpecs()) != 0 {
		t.Error("nothing should be written to the config")
	}
}

func TestAddRefusesANonRepo(t *testing.T) {
	withTemporaryConfig(t)
	dir := filepath.Join(t.TempDir(), "plain-folder")
	os.MkdirAll(dir, 0o755)
	if cliRun([]string{"add", dir}) != 1 {
		t.Error("expected failure")
	}
	if len(configSpecs()) != 0 {
		t.Error("nothing should be written to the config")
	}
}

func TestAddPersistsExactlyWhatWasTyped(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	if cliRun([]string{"add", "-s", "5", "-m", "two words", repo.path}) != 0 {
		t.Fatal("add failed")
	}
	lines := configRawLines()
	want := `-s 5 -m "two words" ` + repo.path
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("got %v, want [%q]", lines, want)
	}
	spec := configSpecs()[0]
	if spec.Settle != 5 || spec.Message != "two words" {
		t.Errorf("got %+v", spec)
	}
}

func TestDuplicateAddFailsAndLeavesOneEntry(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	if cliRun([]string{repo.path}) != 1 {
		t.Error("duplicate add should fail")
	}
	if cliRun([]string{"add", "-s", "5", repo.path}) != 1 {
		t.Error("different flags, same repo: still a duplicate")
	}
	if len(configSpecs()) != 1 {
		t.Errorf("got %d entries", len(configSpecs()))
	}
}

func TestBarePathIsAnImplicitAdd(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	if cliRun([]string{repo.path}) != 0 {
		t.Fatal("bare add failed")
	}
	specs := configSpecs()
	if len(specs) != 1 || specs[0].Path != repo.path {
		t.Errorf("got %+v", specs)
	}
}

func TestRmMatchesByName(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	if cliRun([]string{"rm", repo.spec().Name()}) != 0 {
		t.Error("rm by name failed")
	}
	if len(configSpecs()) != 0 {
		t.Error("the entry should be gone")
	}
}

func TestRmMatchesByFullPath(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	if cliRun([]string{"rm", repo.path}) != 0 {
		t.Error("rm by path failed")
	}
	if len(configSpecs()) != 0 {
		t.Error("the entry should be gone")
	}
}

func TestRmUnknownFailsAndLeavesConfigAlone(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	if cliRun([]string{"rm", "not-a-watched-repo"}) != 1 {
		t.Error("expected failure")
	}
	if len(configSpecs()) != 1 {
		t.Error("the config must be untouched")
	}
}

func TestPauseAndResumeFlipPausedInConfig(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{"-s", "5", repo.path})
	if cliRun([]string{"pause", repo.spec().Name()}) != 0 {
		t.Fatal("pause failed")
	}
	if !configSpecs()[0].Paused {
		t.Error("spec should be paused")
	}
	if got := configRawLines()[0]; got != "--paused -s 5 "+repo.path {
		t.Errorf("the rest of the line must survive untouched, got %q", got)
	}
	if cliRun([]string{"resume", repo.spec().Name()}) != 0 {
		t.Fatal("resume failed")
	}
	if configSpecs()[0].Paused {
		t.Error("spec should be resumed")
	}
	if got := configRawLines()[0]; got != "-s 5 "+repo.path {
		t.Errorf("got %q", got)
	}
}

func TestPauseByFullPath(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	if cliRun([]string{"pause", repo.path}) != 0 {
		t.Fatal("pause failed")
	}
	if !configSpecs()[0].Paused {
		t.Error("spec should be paused")
	}
}

func TestPausingTwiceFailsPolitely(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	cliRun([]string{"pause", repo.path})
	before := configRawLines()
	if cliRun([]string{"pause", repo.path}) != 1 {
		t.Error("expected failure")
	}
	after := configRawLines()
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Error("the config must be unchanged")
	}
}

func TestPauseUnknownFails(t *testing.T) {
	withTemporaryConfig(t)
	if cliRun([]string{"pause", "nothing-here"}) != 1 {
		t.Error("expected failure")
	}
}

func TestVersionDoesNotFallThroughToAdd(t *testing.T) {
	withTemporaryConfig(t)
	if cliRun([]string{"version"}) != 0 || cliRun([]string{"--version"}) != 0 {
		t.Error("version should succeed")
	}
	if len(configSpecs()) != 0 {
		t.Error("nothing should be added")
	}
}

func TestBrokenConfigEntriesSurfaceInLoadAndStatus(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	cliRun([]string{repo.path})
	configAppend(filepath.Join(t.TempDir(), "vanished-repo"))
	configAppend("-z bogus /tmp/x")
	specs, errs := configLoad()
	if len(specs) != 1 {
		t.Errorf("the healthy repo is unaffected, got %d", len(specs))
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errors", len(errs))
	}
	foundMissing, foundUnknown := false, false
	for _, e := range errs {
		if e.Reason == "repo not found" {
			foundMissing = true
		}
		if strings.Contains(e.Reason, "unknown flag") {
			foundUnknown = true
		}
	}
	if !foundMissing || !foundUnknown {
		t.Errorf("got %+v", errs)
	}
	if cliRun([]string{"status"}) != 0 {
		t.Error("status renders them rather than crashing or hiding them")
	}
}
