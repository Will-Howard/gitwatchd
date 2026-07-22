package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A throwaway git repo for the engine to run against: real git, real commits,
// so tests assert both the reported outcome and the repo state left behind.
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

// Wire up an "origin" this repo pushes to (a real bare repo).
func (r *testRepo) addOrigin() *bareRemote {
	remote := newBareRemote(r.t)
	r.git("remote", "add", "origin", remote.path)
	return remote
}

// An "origin" pointing at a URL that does not exist (an unreachable remote).
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

// A bare repo standing in for the server side (GitHub etc).
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

// The built gitwatchd binary, for tests that exercise the real daemon.
var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gitwatchd-bin")
	if err == nil {
		bin := filepath.Join(dir, "gitwatchd")
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Stderr = os.Stderr
		if build.Run() == nil {
			testBinary = bin
		}
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// Run the built binary with an isolated HOME/config/state and return its
// exit code and combined output.
func runCLI(env []string, args ...string) (int, string) {
	cmd := exec.Command(testBinary, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		code = -1
	}
	return code, strings.TrimSpace(string(out))
}

func isolatedEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"GITWATCHD_CONFIG="+filepath.Join(home, ".gitwatchd"),
		"GITWATCHD_STATE_DIR="+filepath.Join(home, "state"),
		"GITWATCHD_NO_SPAWN=1")
}
