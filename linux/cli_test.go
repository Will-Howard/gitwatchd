package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CLI contract tests run the real cliRun against a scratch config directory
// (never the user's ~/.gitwatchd) with daemon-spawning disabled.

func withTemporaryConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GITWATCHD_NO_SPAWN", "1")
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

func TestAddResolvesARelativeTargetForTheDaemon(t *testing.T) {
	withTemporaryConfig(t)
	repo := newTestRepo(t)
	t.Chdir(repo.path)
	if cliRun([]string{"-s", "5", "."}) != 0 {
		t.Fatal("add failed")
	}
	// The daemon reads the config from a different working directory, so
	// the stored target must be absolute; the flags stay verbatim.
	if got := configRawLines()[0]; got != "-s 5 "+repo.path {
		t.Errorf("got %q", got)
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

// Autostart onboarding: what a daemon start does about autostart, given the
// recorded wish, where the binary lives and whether systemd is here.

func TestAutostartDecisionTable(t *testing.T) {
	installed := autostartConditions{installedBinary: true, systemdPresent: true}
	cases := []struct {
		what       string
		conditions autostartConditions
		want       autostartAction
	}{
		{"a development copy is never onboarded",
			autostartConditions{systemdPresent: true}, autostartLeaveAlone},
		{"a development copy is left alone even with a wish on record",
			autostartConditions{recorded: true, wantsOn: true, systemdPresent: true}, autostartLeaveAlone},
		{"the first installed run turns autostart on",
			installed, autostartEnableFirstRun},
		{"a wish that is already satisfied needs nothing",
			autostartConditions{installedBinary: true, systemdPresent: true,
				recorded: true, wantsOn: true, unitEnabled: true}, autostartLeaveAlone},
		{"a wanted unit that went missing is reinstated",
			autostartConditions{installedBinary: true, systemdPresent: true,
				recorded: true, wantsOn: true}, autostartReinstate},
		{"an opt-out is never overridden",
			autostartConditions{installedBinary: true, systemdPresent: true, recorded: true},
			autostartLeaveAlone},
		{"the first installed run without systemd says so",
			autostartConditions{installedBinary: true}, autostartReportUnavailable},
		{"without systemd it says so once, not on every start",
			autostartConditions{installedBinary: true, recorded: true, wantsOn: true},
			autostartLeaveAlone},
		{"an opt-out without systemd stays quiet",
			autostartConditions{installedBinary: true, recorded: true}, autostartLeaveAlone},
	}
	for _, c := range cases {
		if got := autostartActionFor(c.conditions); got != c.want {
			t.Errorf("%s: action %d, want %d (%+v)", c.what, got, c.want, c.conditions)
		}
	}
}

func TestAutostartWishIsRecordedAsLaunchAtLogin(t *testing.T) {
	withTemporaryConfig(t)
	if _, recorded := launchAtLogin(); recorded {
		t.Error("nothing is on record until the user or a first run says so")
	}
	setLaunchAtLogin(true)
	if on, recorded := launchAtLogin(); !on || !recorded {
		t.Errorf("after recording on: on=%v recorded=%v", on, recorded)
	}
	setLaunchAtLogin(false)
	if on, recorded := launchAtLogin(); on || !recorded {
		t.Errorf("after recording off: on=%v recorded=%v", on, recorded)
	}
	raw, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"launch-at-login": "off"`) {
		t.Errorf("the setting must be readable under its own key:\n%s", raw)
	}
}

func TestOnlyAnInstalledBinaryIsOnboarded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{".local/bin", "bin", "build"} {
		os.MkdirAll(filepath.Join(home, dir), 0o755)
	}
	for _, dir := range []string{".local/bin", "bin"} {
		if !isInstalledBinary(filepath.Join(home, dir, "gitwatchd")) {
			t.Errorf("%s is one of the install destinations", dir)
		}
	}
	if isInstalledBinary(filepath.Join(home, "build", "gitwatchd")) {
		t.Error("a build directory holds a development copy")
	}
}

// The help text states flag defaults and exclusion behaviour as facts;
// these tests pin them so the help can't silently drift from the code.

func TestDefaultsForBarePath(t *testing.T) {
	spec, errMsg := parseRepoSpec([]string{"/tmp/x"})
	if spec == nil {
		t.Fatal(errMsg)
	}
	if spec.Settle != 2 { // -s "Default: 2"
		t.Errorf("settle = %v", spec.Settle)
	}
	if spec.Remote != "" { // -r "Default: no push"
		t.Errorf("remote = %q", spec.Remote)
	}
	if spec.Message != "gitwatchd auto-commit (%d)" {
		t.Errorf("message = %q", spec.Message)
	}
	if spec.DateFormat != "+%Y-%m-%d %H:%M:%S" { // upstream's exact default, leading + included
		t.Errorf("dateFormat = %q", spec.DateFormat)
	}
	if spec.Branch != "" || spec.Rebase || spec.Exclude != "" || spec.NoMergeCommit || spec.Paused {
		t.Errorf("unexpected non-default: %+v", spec)
	}
	if spec.CommitOnStart {
		t.Error("-f is opt-in: a deliberate manual state is not flushed")
	}
	if spec.CommitCommand != "" || spec.PipeChangedFiles { // -c "Overrides -m and -d"; -C off
		t.Errorf("unexpected non-default: %+v", spec)
	}
	if spec.ListChanges != -1 || !spec.ListChangesColor {
		t.Error("-l/-L off by default; the -m message is used")
	}
}

func TestPIsAnAliasOfR(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-p", "origin", "/tmp/x"})
	if spec == nil || spec.Remote != "origin" {
		t.Errorf("got %+v", spec)
	}
}

func TestSettleRejectsBadValues(t *testing.T) {
	if spec, _ := parseRepoSpec([]string{"-s", "-1", "/tmp/x"}); spec != nil {
		t.Error("-s -1 should be rejected")
	}
	if spec, _ := parseRepoSpec([]string{"-s", "soon", "/tmp/x"}); spec != nil {
		t.Error("-s soon should be rejected")
	}
	if _, msg := parseRepoSpec([]string{"-s", "-1", "/tmp/x"}); msg != "-s needs a number of seconds, 0 or more" {
		t.Errorf("wrong message: %q", msg)
	}
	spec, _ := parseRepoSpec([]string{"-s", "0", "/tmp/x"})
	if spec == nil || spec.Settle != 0 {
		t.Error("-s 0 is valid")
	}
}

func TestVerboseIsAcceptedAndIgnored(t *testing.T) {
	spec, msg := parseRepoSpec([]string{"-v", "/tmp/x"})
	if spec == nil || spec.Path != "/tmp/x" {
		t.Errorf("-v is accepted for gitwatch parity, then ignored: %s", msg)
	}
}

func TestListChangesFlagsSetTheCapAndColour(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-l", "10", "/tmp/x"})
	if spec.ListChanges != 10 || !spec.ListChangesColor {
		t.Errorf("got %+v", spec)
	}
	spec, _ = parseRepoSpec([]string{"-L", "10", "/tmp/x"})
	if spec.ListChanges != 10 || spec.ListChangesColor {
		t.Errorf("-L is -l without colour, got %+v", spec)
	}
	spec, _ = parseRepoSpec([]string{"-L", "5", "-l", "3", "/tmp/x"})
	if spec.ListChanges != 3 {
		t.Errorf("the line cap itself is last-wins, got %+v", spec)
	}
	if spec.ListChangesColor {
		t.Error("upstream's -l never restores colour once -L appeared")
	}
}

func TestListChangesRejectsBadLineCounts(t *testing.T) {
	if spec, _ := parseRepoSpec([]string{"-l", "many", "/tmp/x"}); spec != nil {
		t.Error("-l many should be rejected")
	}
	if spec, _ := parseRepoSpec([]string{"-L", "-1", "/tmp/x"}); spec != nil {
		t.Error("-L -1 should be rejected")
	}
	if spec, _ := parseRepoSpec([]string{"-l", "0", "/tmp/x"}); spec == nil || spec.ListChanges != 0 {
		t.Error("-l 0 is valid and means unlimited")
	}
}

func TestCommitCommandIsStoredVerbatim(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-c", "echo a b", "-C", "/tmp/x"})
	if spec == nil || spec.CommitCommand != "echo a b" || !spec.PipeChangedFiles {
		t.Errorf("got %+v", spec)
	}
}

// Deliberate divergence from upstream, matching macOS: gitwatch's getopts
// declares -C as taking an argument, so there `-c cmd -C <target>` loses the
// target. Treating -C as the boolean its body implies keeps every documented
// invocation working.
func TestPipeFlagConsumesNoArgument(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-C", "-r", "origin", "/tmp/x"})
	if spec == nil || !spec.PipeChangedFiles || spec.Remote != "origin" || spec.Path != "/tmp/x" {
		t.Errorf("got %+v", spec)
	}
}

func TestExcludeMatchesAnywhereInPath(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-x", `\.log$`, "/tmp/x"})
	if !spec.Excludes("/tmp/x/deep/dir/debug.log") {
		t.Error("should match a nested .log file")
	}
	if spec.Excludes("/tmp/x/log.txt") {
		t.Error("should not match log.txt")
	}
}

func TestExcludeDirectoryPattern(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-x", "build/", "/tmp/x"})
	if !spec.Excludes("/tmp/x/build/out.o") {
		t.Error("should exclude the build subtree")
	}
	if spec.Excludes("/tmp/x/src/out.o") {
		t.Error("should not exclude src")
	}
}

func TestLastExcludeWins(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"-x", `\.log$`, "-x", `\.tmp$`, "/tmp/x"})
	if !spec.Excludes("/tmp/x/b.tmp") {
		t.Error("the last -x should apply")
	}
	if spec.Excludes("/tmp/x/a.log") {
		t.Error("the first -x should be forgotten, as upstream")
	}
}

func TestInvalidExcludeIsAParseError(t *testing.T) {
	if spec, _ := parseRepoSpec([]string{"-x", "*.log", "/tmp/x"}); spec != nil {
		t.Error("an invalid regex must be a parse error, not a silent no-op")
	}
}

func TestNoExcludeByDefault(t *testing.T) {
	spec, _ := parseRepoSpec([]string{"/tmp/x"})
	if spec.Excludes("/tmp/x/anything.log") {
		t.Error("nothing is excluded without -x")
	}
}

// The help is the contract; these hold it to the implementation. The
// canonical lists below ARE the documented surface: adding a flag or command
// to only one side of the contract fails one of these tests.

var contractValueFlags = map[string]string{
	"-s": "2", "-r": "origin", "-b": "main", "-m": "msg",
	"-d": "+%Y", "-x": `\.log$`, "-g": "/tmp/gd",
	"-l": "10", "-L": "10", "-c": "echo hi",
}

var contractBoolFlags = []string{"-R", "-M", "-f", "-C", "--paused"}
var contractCommands = []string{"add", "rm", "pause", "resume", "status",
	"start", "stop", "autostart", "config", "help", "version"}

func TestDocumentedFlagsParse(t *testing.T) {
	for flag, value := range contractValueFlags {
		if spec, msg := parseRepoSpec([]string{flag, value, "/tmp/x"}); spec == nil {
			t.Errorf("%s is documented but doesn't parse: %s", flag, msg)
		}
	}
	for _, flag := range contractBoolFlags {
		if spec, msg := parseRepoSpec([]string{flag, "/tmp/x"}); spec == nil {
			t.Errorf("%s is documented but doesn't parse: %s", flag, msg)
		}
	}
}

func TestContractAppearsInHelp(t *testing.T) {
	for flag := range contractValueFlags {
		if !strings.Contains(usageText, flag+" <") {
			t.Errorf("help is missing %s with its <metavar>", flag)
		}
	}
	for _, flag := range contractBoolFlags {
		if !strings.Contains(usageText, flag) {
			t.Errorf("help is missing %s", flag)
		}
	}
	for _, command := range contractCommands {
		if !strings.Contains(usageText, command) {
			t.Errorf("help is missing the %s command", command)
		}
	}
}

func TestNoPhantomFlagsInHelp(t *testing.T) {
	known := map[string]bool{}
	for flag := range contractValueFlags {
		known[flag] = true
	}
	for _, flag := range contractBoolFlags {
		known[flag] = true
	}
	for _, line := range strings.Split(usageText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			continue
		}
		flag := strings.Fields(line)[0]
		if !known[flag] {
			t.Errorf("help documents %s, which the contract list doesn't know", flag)
		}
	}
}

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
	cases := map[CommitOutcome]string{
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
		240 * time.Second, 480 * time.Second, 960 * time.Second,
		30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w {
			t.Errorf("after %d failures: got %v, want %v", i+1, got, w)
		}
	}
	// A day-long outage: the doubling must saturate at the cap, never overflow.
	for _, n := range []int{30, 288, 100000} {
		if got := backoffDelay(n); got != backoffCap {
			t.Errorf("after %d failures: got %v, want the %v cap", n, got, backoffCap)
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
