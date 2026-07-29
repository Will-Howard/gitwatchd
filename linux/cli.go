package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// Keep in step with the macOS CLI (macos/Sources/CLI.swift) and Info.plist:
// the two implementations ship as one product and report one version.
const version = "0.2.0"

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
  -d <fmt>      Format string for that timestamp (see ` + "`man date`" + `).
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

type ConfigError struct {
	Label    string // repo name, or the offending line for parse errors
	Reason   string // short fixed vocabulary, e.g. "repo not found"
	Detail   string // full path or config line, for status
	RepoPath string // set when there is a path to re-check for healing
}

// A watched-repo specification, expressed in gitwatch's flag vocabulary.
//
//	gitwatch  [-s secs] [-d fmt] [-r remote [-b branch]] [-R] [-m msg]
//	          [-x pattern] [-M] [-g gitdir] [-e events] <target>
//
// Each config line is exactly the argument string you'd pass to gitwatch (modulo path resolution).
type RepoSpec struct {
	Path          string
	Settle        float64 // -s  debounce seconds
	DateFormat    string  // -d
	Remote        string  // -r
	Branch        string  // -b
	Rebase        bool    // -R  pull --rebase before push
	Message       string  // -m  (%d -> date)
	Exclude       string  // -x  regex; last one wins, as upstream
	NoMergeCommit bool    // -M
	CommitOnStart bool    // -f  commit pending changes when watching starts
	GitDir        string  // -g  --git-dir
	Paused        bool    // --paused (gitwatchd extension, not gitwatch)
	Raw           string  // the config line this spec came from, for change detection
	targetIndex   int     // which arg was the target, so add can persist it resolved
}

func (s RepoSpec) Name() string {
	return filepath.Base(s.Path)
}

func (s RepoSpec) IsFileTarget() bool {
	info, err := os.Stat(s.Path)
	return err != nil || !info.IsDir()
}

// Where git commands run: the target itself, or its parent for a file
// target (upstream's TARGETDIR).
func (s RepoSpec) WorkDir() string {
	if s.IsFileTarget() {
		return filepath.Dir(s.Path)
	}
	return s.Path
}

func (s RepoSpec) Excludes(fullPath string) bool {
	if s.Exclude == "" {
		return false
	}
	re, err := regexp.Compile(s.Exclude)
	if err != nil {
		return false
	}
	return re.MatchString(fullPath)
}

func main() {
	os.Exit(cliRun(os.Args[1:]))
}

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
	nameOrPath := args[0]
	n := configRemove(nameOrPath)
	if n == 0 {
		warn("no watched repo matches " + nameOrPath)
		return 1
	}
	ensureDaemonRunning()
	entries := "entries"
	if n == 1 {
		entries = "entry"
	}
	fmt.Printf("✓ stopped watching %s (%d %s removed)\n", nameOrPath, n, entries)
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
	nameOrPath := args[0]
	n := configSetPaused(nameOrPath, paused)
	if n == 0 {
		known := false
		for _, s := range configSpecs() {
			if configMatches(s, nameOrPath) {
				known = true
			}
		}
		if known {
			state := "watching"
			if paused {
				state = "paused"
			}
			warn(nameOrPath + " is already " + state)
		} else {
			warn("no watched repo matches " + nameOrPath)
		}
		return 1
	}
	ensureDaemonRunning()
	if paused {
		fmt.Printf("⏸ paused %s  (resume with: gitwatchd resume %s)\n", nameOrPath, nameOrPath)
	} else {
		fmt.Printf("✓ resumed %s; catching up on anything that changed meanwhile\n", nameOrPath)
	}
	return 0
}

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
		logs := logfilePath()
		if unitInstalled() && systemctlPresent() {
			logs = "journalctl --user -u gitwatchd"
		}
		warn("daemon did not come up; check " + logs)
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

// Best-effort: start the daemon if it isn't running (the running daemon
// live-reloads the config, so this is only for the cold case).
func ensureDaemonRunning() {
	if os.Getenv("GITWATCHD_NO_SPAWN") != "" || isDaemonRunning() {
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

// Config = a list of gitwatch-style argument lines, one repo per line.
// Hand-editable and CLI-writable; the CLI appends exactly what the user typed.

func configPath() string {
	if p := os.Getenv("GITWATCHD_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), ".gitwatchd")
}

func configEnsureExists() {
	if _, err := os.Stat(configPath()); err == nil {
		return
	}
	template := `# gitwatchd: one repo per line.
#   [-s secs] [-r remote [-b branch]] [-R] [-m msg] [-x pattern] [-M] [--paused] <path>
# 'gitwatchd help' explains each flag. Examples:
#   ~/code/my-notes
#   -s 5 -r origin -b main ~/code/blog
# (from a terminal, 'gitwatchd .' adds the current repo here for you)
`
	os.WriteFile(configPath(), []byte(template), 0o644)
}

func configRawLines() []string {
	text, err := os.ReadFile(configPath())
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(text), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines
}

func configSpecs() []*RepoSpec {
	var specs []*RepoSpec
	for _, line := range configRawLines() {
		if spec, _ := parseRepoSpec(tokenize(line)); spec != nil {
			spec.Raw = line
			specs = append(specs, spec)
		}
	}
	return specs
}

func configLineErrors() []ConfigError {
	var errs []ConfigError
	for _, line := range configRawLines() {
		if spec, msg := parseRepoSpec(tokenize(line)); spec == nil {
			if msg == "" {
				msg = "unparseable line"
			}
			errs = append(errs, ConfigError{Label: line, Reason: msg, Detail: line})
		}
	}
	return errs
}

func configLoad() ([]*RepoSpec, []ConfigError) {
	errs := configLineErrors()
	var watchable []*RepoSpec
	for _, spec := range configSpecs() {
		reason := ""
		if _, err := os.Stat(spec.Path); err != nil {
			reason = "repo not found"
		} else if !isRepo(spec.WorkDir(), spec.GitDir) {
			reason = "not a git repo"
		} else {
			watchable = append(watchable, spec)
			continue
		}
		errs = append(errs, ConfigError{Label: spec.Name(), Reason: reason,
			Detail: spec.Path, RepoPath: spec.Path})
	}
	return watchable, errs
}

func configAppend(line string) {
	configEnsureExists()
	raw, _ := os.ReadFile(configPath())
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += line + "\n"
	os.WriteFile(configPath(), []byte(text), 0o644)
}

func configMatches(spec *RepoSpec, nameOrPath string) bool {
	return spec.Path == expandTilde(nameOrPath) || spec.Name() == nameOrPath || spec.Path == nameOrPath
}

func configRemove(nameOrPath string) int {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return 0
	}
	removed := 0
	var kept []string
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			kept = append(kept, rawLine)
			continue
		}
		spec, _ := parseRepoSpec(tokenize(line))
		if spec != nil && configMatches(spec, nameOrPath) {
			removed++
			continue
		}
		kept = append(kept, rawLine)
	}
	os.WriteFile(configPath(), []byte(strings.Join(kept, "\n")), 0o644)
	return removed
}

// Flip the --paused token on config lines matching `nameOrPath` (by full
// path or repo name, like remove). Pause lives in the config, not daemon
// state, so it survives daemon and machine restarts. Returns the number
// of lines changed.
func configSetPaused(nameOrPath string, paused bool) int {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return 0
	}
	changed := 0
	var lines []string
	for _, rawLine := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, rawLine)
			continue
		}
		spec, _ := parseRepoSpec(tokenize(trimmed))
		if spec == nil || !configMatches(spec, nameOrPath) {
			lines = append(lines, rawLine)
			continue
		}
		rewritten, ok := togglingPaused(trimmed, spec.Path, paused)
		if !ok {
			lines = append(lines, rawLine)
			continue
		}
		changed++
		lines = append(lines, rewritten)
	}
	if changed > 0 {
		os.WriteFile(configPath(), []byte(strings.Join(lines, "\n")), 0o644)
	}
	return changed
}

func togglingPaused(line string, path string, paused bool) (string, bool) {
	tokens := tokenize(line)
	spec, _ := parseRepoSpec(tokens)
	if spec == nil || spec.Path != path || spec.Paused == paused {
		return "", false
	}
	var kept []string
	for _, t := range tokens {
		if t != "--paused" {
			kept = append(kept, t)
		}
	}
	if paused {
		kept = append([]string{"--paused"}, kept...)
	}
	quoted := make([]string, len(kept))
	for i, t := range kept {
		quoted[i] = quoteIfNeeded(t)
	}
	return strings.Join(quoted, " "), true
}

func quoteIfNeeded(s string) string {
	if strings.Contains(s, `"`) && !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	if strings.Contains(s, " ") || strings.Contains(s, `"`) || strings.Contains(s, "'") {
		return `"` + s + `"`
	}
	return s
}

// Split a config line into arguments on whitespace, respecting simple single
// or double quotes so that `-m "two words"` stays a single argument.
func tokenize(line string) []string {
	var args []string
	var current strings.Builder
	var openQuote rune // the quote char we're inside, 0 if none

	finishArg := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}

	for _, ch := range line {
		switch {
		case openQuote != 0:
			if ch == openQuote {
				openQuote = 0
			} else {
				current.WriteRune(ch)
			}
		case ch == '"' || ch == '\'':
			openQuote = ch
		case unicode.IsSpace(ch):
			finishArg()
		default:
			current.WriteRune(ch)
		}
	}
	finishArg()
	return args
}

// Parse a gitwatch-style argument list into a RepoSpec.
// Returns nil + an error message if there's no valid target.
func parseRepoSpec(args []string) (*RepoSpec, string) {
	spec := &RepoSpec{
		Settle:     2,
		DateFormat: "+%Y-%m-%d %H:%M:%S",
		Message:    "gitwatchd auto-commit (%d)",
	}
	target := ""
	haveTarget := false
	i := 0
	next := func() (string, bool) {
		i++
		if i < len(args) {
			return args[i], true
		}
		return "", false
	}

	for i < len(args) {
		a := args[i]
		switch a {
		case "-s":
			v, ok := next()
			d, err := strconv.ParseFloat(v, 64)
			if !ok || err != nil || d < 0 {
				return nil, "-s needs a number of seconds, 0 or more"
			}
			spec.Settle = d
		case "-d":
			if v, ok := next(); ok {
				spec.DateFormat = v
			}
		case "-r", "-p": // -p: upstream's alias of -r
			if v, ok := next(); ok {
				spec.Remote = v
			}
		case "-b":
			if v, ok := next(); ok {
				spec.Branch = v
			}
		case "-R":
			spec.Rebase = true
		case "-m":
			if v, ok := next(); ok {
				spec.Message = v
			}
		case "-x":
			v, ok := next()
			if !ok {
				return nil, "-x needs a valid regular expression"
			}
			if _, err := regexp.Compile(v); err != nil {
				return nil, "-x needs a valid regular expression"
			}
			spec.Exclude = v
		case "-M":
			spec.NoMergeCommit = true
		case "-f":
			spec.CommitOnStart = true
		case "-g":
			if v, ok := next(); ok {
				spec.GitDir = v
			}
		case "-e":
			next() // -e accepted for gitwatch compatibility; ignored
		case "--paused":
			spec.Paused = true
		default:
			if strings.HasPrefix(a, "-") {
				return nil, "unknown flag " + a
			}
			target = a // last bare arg wins as the target
			spec.targetIndex = i
			haveTarget = true
		}
		i++
	}

	if !haveTarget {
		return nil, "no target path given"
	}
	spec.Path = normalizePath(target)
	return spec, ""
}

func normalizePath(p string) string {
	p = expandTilde(p)
	if !filepath.IsAbs(p) {
		cwd, err := os.Getwd()
		if err == nil {
			p = filepath.Join(cwd, p)
		}
	}
	return filepath.Clean(p)
}

func expandTilde(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

// One status tail at most: paused wins over errors, errors over pending.
func rowTitle(name, branch string, paused bool, pending int, errorLabel string) string {
	base := name + " · " + branch
	if paused {
		return base + " · ⏸ paused"
	}
	if errorLabel != "" {
		return base + " · ⚠ " + errorLabel
	}
	if pending == 1 {
		return base + " · 1 pending change"
	}
	if pending > 1 {
		return fmt.Sprintf("%s · %d pending changes", base, pending)
	}
	return base
}

// Fixed-width row; the full path lives on the detail line.
func configErrorRow(label, reason string) string {
	return truncated("⚠ "+label+" · "+reason, 48)
}

func errorHeadline(label string, attempts int) string {
	if attempts > 1 {
		return fmt.Sprintf("⚠ %s (%d attempts)", label, attempts)
	}
	return "⚠ " + label
}

func retryLine(lastTried time.Time, nextRetry *time.Time, now time.Time) string {
	tried := "tried " + ago(now.Sub(lastTried).Seconds())
	if nextRetry == nil {
		return tried + " · retries on next change"
	}
	dt := nextRetry.Sub(now).Seconds()
	if dt <= 1 {
		return tried + " · retrying now"
	}
	return tried + " · retrying in " + span(dt)
}

// Autostart = a systemd user unit, the standard way for a per-user daemon to
// survive reboots and headless boots (with lingering). If systemd is absent,
// report this and do nothing (the user should e.g. add `gitwatchd start` to
// a startup script in this case).

func unitPath() string {
	return filepath.Join(homeDir(), ".config", "systemd", "user", "gitwatchd.service")
}

func systemctlPresent() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func unitInstalled() bool {
	_, err := os.Stat(unitPath())
	return err == nil
}

func systemctlUser(args ...string) (int, string) {
	return runCommand("systemctl", append([]string{"--user"}, args...), "")
}

func unitActive() bool {
	_, out := systemctlUser("is-active", "gitwatchd")
	return out == "active"
}

func autostartOn() int {
	if !systemctlPresent() {
		warn("systemd not found: autostart needs a systemd user session.\n" +
			"  run the daemon manually instead: gitwatchd start")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		warn("cannot resolve the gitwatchd binary path: " + err.Error())
		return 1
	}
	exe, _ = filepath.EvalSymlinks(exe)
	unit := fmt.Sprintf(`[Unit]
Description=gitwatchd: watch git repos and auto-commit changes

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		warn("cannot create " + filepath.Dir(unitPath()) + ": " + err.Error())
		return 1
	}
	if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
		warn("cannot write " + unitPath() + ": " + err.Error())
		return 1
	}
	systemctlUser("daemon-reload")
	// A directly spawned daemon holds the single-instance lock and would
	// make the unit fail; hand it over to systemd.
	if isDaemonRunning() && !unitActive() {
		if pid := daemonPid(); pid > 0 {
			syscall.Kill(pid, syscall.SIGTERM)
			for i := 0; i < 50 && isDaemonRunning(); i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	if code, out := systemctlUser("enable", "--now", "gitwatchd"); code != 0 {
		warn("systemctl --user enable --now gitwatchd failed: " + out)
		return 1
	}
	// Lingering keeps the user manager (and the daemon) alive without an
	// open session: headless boots, logged-out laptops.
	if code, out := runCommand("loginctl", []string{"enable-linger", os.Getenv("USER")}, ""); code != 0 {
		fmt.Println("✓ autostart: on (systemd user unit enabled)")
		fmt.Println("  note: loginctl enable-linger failed (" + strings.TrimSpace(out) + ")")
		fmt.Println("  without lingering the daemon stops when you log out")
		return 0
	}
	fmt.Println("✓ autostart: on (systemd user unit enabled, survives logout and reboot)")
	return 0
}

func autostartOff() int {
	if !systemctlPresent() {
		warn("systemd not found: nothing to turn off (autostart was never installed)")
		return 1
	}
	if !unitInstalled() {
		fmt.Println("autostart: already off")
		return 0
	}
	systemctlUser("disable", "--now", "gitwatchd")
	os.Remove(unitPath())
	systemctlUser("daemon-reload")
	fmt.Println("✓ autostart: off")
	return 0
}

func autostartStatus() int {
	if !systemctlPresent() {
		fmt.Println("autostart: unavailable (systemd not found); run the daemon with: gitwatchd start")
		return 0
	}
	if !unitInstalled() {
		fmt.Println("autostart: off")
		return 0
	}
	_, enabled := systemctlUser("is-enabled", "gitwatchd")
	state := "on"
	if enabled != "enabled" {
		state = "installed but " + enabled
	}
	if unitActive() {
		fmt.Printf("autostart: %s (daemon running)\n", state)
	} else {
		fmt.Printf("autostart: %s (daemon not running)\n", state)
	}
	return 0
}
