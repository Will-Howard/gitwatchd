package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// End-to-end through the real binary: config written by the CLI, daemon
// started detached, inotify driving commits, live config reload, and a clean
// stop. One flow, because this is exactly the session a user has.

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

	// Live reload: adding a second repo while the daemon runs starts
	// watching it without a restart.
	second := newTestRepo(t)
	if code, out := runCLI(env, "add", "-s", "0", second.path); code != 0 {
		t.Fatalf("second add failed: %s", out)
	}
	waitForDaemonPickup(t, second, func() { second.write("more.txt", "hi\n") })

	// Pause stops commits; resume catches up on what piled up meanwhile.
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

func killDaemonIfRunning(home string) {
	raw, err := os.ReadFile(filepath.Join(home, "state", "gitwatchd.pid"))
	if err != nil {
		return
	}
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}
