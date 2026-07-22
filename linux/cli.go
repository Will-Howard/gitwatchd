package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// The `gitwatchd` command-line client. Writes the shared config file (so it
// works even when the daemon is down) and relies on the running daemon's live
// reload. `add` accepts gitwatch's own flags verbatim, so gitwatch users need
// no relearning.

const version = "0.1.0"

// Tests set this false so CLI calls don't spawn the real daemon.
var spawnsDaemon = true

func cliRun(args []string) int {
	if len(args) == 0 {
		os.Stdout.WriteString(usageText)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		os.Stdout.WriteString(usageText)
		return 0
	case "version", "--version":
		fmt.Println("gitwatchd " + version)
		return 0
	case "rm", "remove":
		return cliRemove(args[1:])
	case "pause":
		return cliSetPaused(args[1:], true)
	case "resume":
		return cliSetPaused(args[1:], false)
	case "status":
		return cliStatus()
	case "doctor":
		return cliDoctor()
	case "start":
		return cliStart()
	case "stop":
		return cliStop()
	case "config":
		return cliConfig(args[1:])
	case "autostart":
		return cliAutostart(args[1:])
	case "add":
		return cliAdd(args[1:])
	case "daemon":
		return runDaemon()
	default:
		// Bare form: `gitwatchd [flags] <target>`: implicit add.
		return cliAdd(args)
	}
}

func cliAdd(args []string) int {
	spec, errMsg := parseRepoSpec(args)
	if spec == nil {
		if errMsg == "" {
			errMsg = "could not parse arguments"
		}
		warn(errMsg)
		return 1
	}
	if _, err := os.Stat(spec.Path); err != nil {
		warn("path does not exist: " + spec.Path)
		return 1
	}
	if !isRepo(spec.WorkDir(), spec.GitDir) {
		warn("not inside a git repo: " + spec.Path + "\n  run: git init " + spec.WorkDir())
		return 1
	}
	for _, existing := range configSpecs() {
		if existing.Path == spec.Path {
			warn(fmt.Sprintf("already watching %s (%s)", spec.Name(), spec.Path))
			return 1
		}
	}

	// Persist what the user typed (gitwatch-style line), with the target
	// resolved to an absolute path: the daemon reads this file from a
	// different working directory, so `gitwatchd .` must not store ".".
	quoted := make([]string, len(args))
	for i, a := range args {
		if i == spec.targetIndex {
			a = spec.Path
		}
		quoted[i] = quoteIfNeeded(a)
	}
	configAppend(strings.Join(quoted, " "))
	ensureDaemonRunning()

	pushNote := "local only"
	if spec.Remote != "" {
		branch := spec.Branch
		if branch == "" {
			branch = currentBranch(spec.WorkDir(), spec.GitDir)
		}
		pushNote = "→ " + spec.Remote + "/" + branch
	}
	fmt.Printf("✓ watching  %s  (%s)  %s, settle %ds\n", spec.Name(), spec.Path, pushNote, int(spec.Settle))
	fmt.Println("  gitwatchd status   to see everything watched")
	return 0
}

func cliRemove(args []string) int {
	if len(args) == 0 {
		warn("usage: gitwatchd rm <name|path>")
		return 1
	}
	needle := args[0]
	n := configRemove(needle)
	if n == 0 {
		warn("no watched repo matches " + needle)
		return 1
	}
	ensureDaemonRunning()
	entries := "entries"
	if n == 1 {
		entries = "entry"
	}
	fmt.Printf("✓ stopped watching %s (%d %s removed)\n", needle, n, entries)
	return 0
}

func cliSetPaused(args []string, paused bool) int {
	verb := "resume"
	if paused {
		verb = "pause"
	}
	if len(args) == 0 {
		warn("usage: gitwatchd " + verb + " <name|path>")
		return 1
	}
	needle := args[0]
	n := configSetPaused(needle, paused)
	if n == 0 {
		known := false
		for _, s := range configSpecs() {
			if configMatches(s, needle) {
				known = true
			}
		}
		if known {
			state := "watching"
			if paused {
				state = "paused"
			}
			warn(needle + " is already " + state)
		} else {
			warn("no watched repo matches " + needle)
		}
		return 1
	}
	ensureDaemonRunning()
	if paused {
		fmt.Printf("⏸ paused %s  (resume with: gitwatchd resume %s)\n", needle, needle)
	} else {
		fmt.Printf("✓ resumed %s; catching up on anything that changed meanwhile\n", needle)
	}
	return 0
}

// The status rows, same strings as the macOS menu, plus the daemon's
// published error state when it is running.
func cliStatus() int {
	running := isDaemonRunning()
	state := "not running"
	if running {
		state = "running"
	}
	fmt.Println("daemon:  " + state)
	fmt.Println("config:  " + configPath())
	fmt.Println("")
	specs, errors := configLoad()
	if len(specs) == 0 && len(errors) == 0 {
		fmt.Println("No repos watched yet")
		return 0
	}
	daemonErrors := map[string]RepoStatus{}
	if running {
		daemonErrors = stateErrors()
	}
	repos := "repos"
	if len(specs) == 1 {
		repos = "repo"
	}
	fmt.Printf("Watching %d %s\n", len(specs), repos)
	fmt.Println("")
	now := time.Now()
	for _, s := range specs {
		branch := currentBranch(s.WorkDir(), s.GitDir)
		pending := pendingCount(s.WorkDir(), s.GitDir)
		errStatus, hasErr := daemonErrors[s.Path]
		label := ""
		if hasErr {
			label = errStatus.ErrorLabel
		}
		fmt.Println(rowTitle(s.Name(), branch, s.Paused, pending, label))
		fmt.Println("     " + lastCommitSummary(s.WorkDir(), s.GitDir))
		if hasErr {
			fmt.Println("     " + errorHeadline(errStatus.ErrorLabel, errStatus.Attempts))
			if errStatus.Detail != "" {
				fmt.Println("     " + truncated(errStatus.Detail, 60))
			}
			var next *time.Time
			if errStatus.NextRetry != nil {
				t := time.Unix(*errStatus.NextRetry, 0)
				next = &t
			}
			fmt.Println("     " + retryLine(time.Unix(errStatus.LastAttempt, 0), next, now))
		}
	}
	for _, e := range errors {
		fmt.Println(configErrorRow(e.Label, e.Reason))
		fmt.Println("     " + e.Detail)
	}
	return 0
}

// Undocumented diagnostic: the environment the daemon will use for git.
func cliDoctor() int {
	fmt.Println("What the daemon will use for git:")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Println("  git binary     not found in PATH")
		return 1
	}
	_, gitVersion := runCommand(gitPath, []string{"--version"}, "")
	fmt.Println("  git binary     " + gitPath)
	fmt.Println("  git --version  " + gitVersion)
	path := os.Getenv("PATH")
	if path == "" {
		path = "(unset)"
	}
	fmt.Println("  PATH           " + path)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		code, out := runCommand("ssh-add", []string{"-l"}, "")
		n := 0
		if code == 0 && out != "" {
			n = len(strings.Split(out, "\n"))
		}
		keys := "keys"
		if n == 1 {
			keys = "key"
		}
		fmt.Printf("  SSH_AUTH_SOCK  present · %d %s in agent\n", n, keys)
		if n == 0 {
			fmt.Println("                 ⚠ no keys loaded: SSH pushes may fail. Add: ssh-add ~/.ssh/id_ed25519")
		}
	} else {
		fmt.Println("  SSH_AUTH_SOCK  (unset) ⚠ SSH pushes will fail from the daemon unless keys are unencrypted")
	}
	return 0
}

func cliConfig(args []string) int {
	if len(args) == 0 || args[0] == "path" {
		fmt.Println(configPath())
		return 0
	}
	if args[0] == "edit" {
		return cliEditConfig()
	}
	warn("usage: gitwatchd config [path|edit]")
	return 1
}

// Open the config in the editor `git commit` would use: `git var GIT_EDITOR`
// resolves $GIT_EDITOR, core.editor, $VISUAL, $EDITOR, then vi. Execs the
// editor in place of the CLI so full-screen editors keep the terminal; the
// daemon live-reloads off the file change, so there is nothing to do after.
func cliEditConfig() int {
	configEnsureExists()
	cwd, _ := os.Getwd()
	code, out := gitRun([]string{"var", "GIT_EDITOR"}, cwd, "")
	editor := "vi"
	if code == 0 && out != "" {
		editor = out
	}
	// The editor value can be a command with flags (e.g. "code --wait"),
	// so hand it to the shell; the path rides in as $1, safely quoted.
	argv := []string{"sh", "-c", editor + " \"$1\"", "gitwatchd-edit", configPath()}
	err := syscall.Exec("/bin/sh", argv, os.Environ())
	warn(fmt.Sprintf("couldn't launch editor '%s': %v", editor, err))
	return 1
}

func cliAutostart(args []string) int {
	if len(args) == 0 {
		return autostartStatus()
	}
	switch args[0] {
	case "on":
		return autostartOn()
	case "off":
		return autostartOff()
	case "status":
		return autostartStatus()
	default:
		warn("usage: gitwatchd autostart [on|off|status]")
		return 1
	}
}

func cliStart() int {
	if isDaemonRunning() {
		fmt.Println("daemon already running")
		return 0
	}
	if unitInstalled() && systemctlPresent() {
		if code, out := systemctlUser("start", "gitwatchd"); code != 0 {
			warn("systemctl --user start gitwatchd failed: " + out)
			return 1
		}
	} else if err := spawnDaemon(); err != nil {
		warn("could not start the daemon: " + err.Error())
		return 1
	}
	for i := 0; i < 20 && !isDaemonRunning(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !isDaemonRunning() {
		warn("daemon did not come up; check " + daemonLogHint())
		return 1
	}
	fmt.Println("✓ daemon started")
	return 0
}

func cliStop() int {
	if !isDaemonRunning() {
		fmt.Println("daemon not running")
		return 0
	}
	if unitInstalled() && systemctlPresent() && unitActive() {
		systemctlUser("stop", "gitwatchd")
	} else if pid := daemonPid(); pid > 0 {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	// Wait for the exit so `gitwatchd stop && gitwatchd start` doesn't race
	// the old process.
	for i := 0; i < 50 && isDaemonRunning(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if isDaemonRunning() {
		warn("daemon did not exit; force with: pkill -x gitwatchd")
		return 1
	}
	fmt.Println("✓ daemon stopped")
	return 0
}

// Detached spawn for systems without systemd: its own session (setsid), output
// to a log file so the terminal can close.
func spawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.MkdirAll(stateDir(), 0o755)
	logFile, err := os.OpenFile(logfilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func daemonLogHint() string {
	if unitInstalled() && systemctlPresent() {
		return "journalctl --user -u gitwatchd"
	}
	return logfilePath()
}

// Best-effort: start the daemon if it isn't running (the running daemon
// live-reloads the config, so this is only for the cold case).
func ensureDaemonRunning() {
	if !spawnsDaemon || os.Getenv("GITWATCHD_NO_SPAWN") != "" || isDaemonRunning() {
		return
	}
	if unitInstalled() && systemctlPresent() {
		systemctlUser("start", "gitwatchd")
		return
	}
	spawnDaemon()
}

func warn(msg string) {
	fmt.Fprintln(os.Stderr, "✗ "+msg)
}

const usageText = `gitwatchd - daemon that watches git repos and auto-commits changes

USAGE
  gitwatchd [flags] <path>     watch a repo
  gitwatchd <command> [args]

EXAMPLES
  gitwatchd .                       watch the current repo, commit locally
  gitwatchd -r origin .             also push every commit to origin
  gitwatchd -r origin -b main -R .  two-way sync: fetch commits made on
                                    other machines and rebase yours on top
                                    before each push to origin/main
  gitwatchd status                  see everything being watched
  gitwatchd rm blog                 stop watching, by name or path

COMMANDS
  add [flags] <path>    watch a repo (bare ` + "`gitwatchd [flags] <path>`" + ` works too)
  rm <name|path>        stop watching a repo
  pause <name|path>     stop watching temporarily; the repo stays listed
  resume <name|path>    start watching again and commit what piled up
  status                everything being watched, in the terminal
  start, stop           start or stop the daemon
  autostart [on|off|status]
                        run the daemon at boot (installs a systemd user unit)
  config [path|edit]    print the config file path, or open it in your
                        editor (the one ` + "`git commit`" + ` uses)
  help                  show this help
  version               print the version

FLAGS (for add)
  -s <secs>     Wait <secs> after the last change before committing, so a
                batch of writes lands as one commit. Default: 2.
  -r <remote>   Push to <remote> after every commit. Default: no push.
  -b <branch>   Branch to push to. Without it, a plain ` + "`git push <remote>`" + `
                decides. Only meaningful together with -r.
  -R            Before each push, pull commits made elsewhere and rebase
                yours on top (` + "`git pull --rebase <remote>`" + `). Use with -r
                when more than one machine pushes to the same branch.
  -m <msg>      Commit message; %d becomes the timestamp.
                Default: "gitwatchd auto-commit (%d)".
  -d <fmt>      Format for that timestamp, passed to date(1) as is,
                so start it with "+" (see ` + "`man date`" + `).
                Default: "+%Y-%m-%d %H:%M:%S".
  -x <pattern>  Skip changes whose path matches this regular
                expression (e.g. '\.log$' or 'build/').
  -M            Skip committing while the repo has a merge in progress.
  -f            Commit anything already pending as soon as watching
                starts (daemon launch, or when the repo is added).
  -g <path>     Location of the .git directory, if elsewhere (--git-dir).
  --paused      Keep the repo in the config but don't watch it. This is
                what ` + "`gitwatchd pause`" + ` sets.

The daemon runs in the background and watches every repo listed in ~/.gitwatchd
`
