package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

func TestDaemonEndToEnd(t *testing.T) {
	if testBinary == "" {
		t.Fatal("test binary did not build")
	}
	home := t.TempDir()
	env := isolatedEnv(home)
	t.Cleanup(func() { killDaemonIfRunning(home) })

	repo := newTestRepo(t)
	if code, out := runCLI(env, "add", "-s", "0", repo.path); code != 0 {
		t.Fatalf("add failed: %s", out)
	}

	if code, out := runCLI(env, "start"); code != 0 || !strings.Contains(out, "✓ daemon started") {
		t.Fatalf("start: code=%d out=%s", code, out)
	}
	if code, out := runCLI(env, "start"); code != 0 || !strings.Contains(out, "daemon already running") {
		t.Fatalf("second start: code=%d out=%s", code, out)
	}
	_, out := runCLI(env, "status")
	if !strings.Contains(out, "daemon:  running") {
		t.Fatalf("status should show the daemon running:\n%s", out)
	}

	repo.write("notes.txt", "hello\n")
	waitFor(t, 15*time.Second, "the daemon's auto-commit", func() bool { return repo.commitCount() == 1 })
	if !strings.HasPrefix(repo.lastMessage(), "gitwatchd auto-commit") {
		t.Errorf("got message %q", repo.lastMessage())
	}
	time.Sleep(1 * time.Second)
	if repo.commitCount() != 1 {
		t.Error("the daemon retriggered on its own commit")
	}

	_, out = runCLI(env, "status")
	if !strings.Contains(out, "repo · main") {
		t.Errorf("status should show the watched row:\n%s", out)
	}

	second := newTestRepo(t)
	if code, out := runCLI(env, "add", "-s", "0", second.path); code != 0 {
		t.Fatalf("second add failed: %s", out)
	}
	waitForDaemonPickup(t, second, func() { second.write("more.txt", "hi\n") })

	if code, out := runCLI(env, "pause", repo.path); code != 0 {
		t.Fatalf("pause failed: %s", out)
	}
	time.Sleep(700 * time.Millisecond) // let the reload land
	repo.write("while-paused.txt", "quiet\n")
	time.Sleep(1200 * time.Millisecond)
	if repo.commitCount() != 1 {
		t.Error("a paused repo must not commit")
	}
	if code, out := runCLI(env, "resume", repo.path); code != 0 {
		t.Fatalf("resume failed: %s", out)
	}
	waitFor(t, 15*time.Second, "the resume catch-up commit", func() bool { return repo.commitCount() == 2 })

	if code, out := runCLI(env, "stop"); code != 0 || !strings.Contains(out, "✓ daemon stopped") {
		t.Fatalf("stop: code=%d out=%s", code, out)
	}
	_, out = runCLI(env, "status")
	if !strings.Contains(out, "daemon:  not running") {
		t.Errorf("status should show the daemon stopped:\n%s", out)
	}
}

// The config reload debounces for 300ms; retry the first write until the
// daemon demonstrably watches the new repo.
func waitForDaemonPickup(t *testing.T, repo *testRepo, change func()) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	change()
	for time.Now().Before(deadline) {
		if repo.commitCount() >= 1 {
			return
		}
		time.Sleep(500 * time.Millisecond)
		change()
	}
	t.Fatal("the daemon never picked up the newly added repo")
}

// The one production path the in-process CLI tests cannot reach: `add`
// finding no daemon and spawning one off its own binary (in-process,
// os.Executable() is the go-test binary). Runs without GITWATCHD_NO_SPAWN.
func TestAddSpawnsTheDaemon(t *testing.T) {
	if testBinary == "" {
		t.Fatal("test binary did not build")
	}
	home := t.TempDir()
	var env []string
	for _, e := range isolatedEnv(home) {
		if !strings.HasPrefix(e, "GITWATCHD_NO_SPAWN=") {
			env = append(env, e)
		}
	}
	t.Cleanup(func() { killDaemonIfRunning(home) })

	repo := newTestRepo(t)
	if code, out := runCLI(env, "add", "-s", "0", repo.path); code != 0 {
		t.Fatalf("add failed: %s", out)
	}
	waitFor(t, 15*time.Second, "the daemon add spawned", func() bool {
		_, out := runCLI(env, "status")
		return strings.Contains(out, "daemon:  running")
	})
	pid := daemonPidIn(home)
	if pid <= 0 {
		t.Fatal("the spawned daemon wrote no pidfile")
	}

	if code, out := runCLI(env, "stop"); code != 0 || !strings.Contains(out, "✓ daemon stopped") {
		t.Fatalf("stop: code=%d out=%s", code, out)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("the spawned daemon (pid %d) is still alive after stop", pid)
	}
}

func TestAutostartDegradesClearlyWithoutSystemd(t *testing.T) {
	if testBinary == "" {
		t.Fatal("test binary did not build")
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		t.Skip("this host runs systemd; the degrade path is exercised in plain containers")
	}
	home := t.TempDir()
	env := isolatedEnv(home)
	// A PATH with no systemctl.
	bindir := filepath.Join(home, "bin")
	os.MkdirAll(bindir, 0o755)
	for _, tool := range []string{"git", "date", "sh", "bash"} {
		if p, err := lookPathIn(os.Getenv("PATH"), tool); err == nil {
			os.Symlink(p, filepath.Join(bindir, tool))
		}
	}
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + bindir
		}
	}
	code, out := runCLI(env, "autostart", "on")
	if code == 0 {
		t.Error("autostart on must fail without systemd")
	}
	if !strings.Contains(out, "systemd not found") || !strings.Contains(out, "gitwatchd start") {
		t.Errorf("the message must explain and point to `gitwatchd start`:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd")); err == nil {
		t.Error("no unit may be written when systemd is absent")
	}
	if entries, _ := os.ReadDir(home); len(entries) > 2 { // bin/ and nothing else unexpected
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("no dotfiles may be touched, found %v", names)
	}
}

func lookPathIn(path, tool string) (string, error) {
	for _, dir := range strings.Split(path, ":") {
		candidate := filepath.Join(dir, tool)
		if info, err := os.Stat(candidate); err == nil && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func daemonPidIn(home string) int {
	raw, err := os.ReadFile(filepath.Join(home, "state", "gitwatchd.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

func killDaemonIfRunning(home string) {
	if pid := daemonPidIn(home); pid > 0 {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

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
	time.Sleep(500 * time.Millisecond) // let the new subtree's watches land
	repo.write("fresh/deep/inner.txt", "made inside a new directory\n")
	waitFor(t, 10*time.Second, "a commit from inside the new subtree", func() bool {
		return repo.commitCount() == 1 && pendingCount(repo.path, "") == 0
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
	w.recordWatchFailure("/some/dir", syscall.ENOSPC)
	w.publishStatus()
	if published == nil || published.ErrorLabel != "watch failing" {
		t.Fatalf("got %+v", published)
	}
	if !strings.Contains(published.Detail, "max_user_watches") {
		t.Errorf("the fix should be named: %q", published.Detail)
	}
}
