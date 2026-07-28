package main

import (
	"strings"
	"testing"
)

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
}
var contractBoolFlags = []string{"-R", "-M", "-f", "--paused"}
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
