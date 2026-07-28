package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// A watched-repo specification, expressed in gitwatch's own flag vocabulary so
// that gitwatch users read our config/CLI with zero translation.
//
//   gitwatch  [-s secs] [-d fmt] [-r remote [-b branch]] [-R] [-m msg]
//             [-x pattern] [-M] [-g gitdir] [-e events] <target>
//
// Each config line is exactly the argument string you'd pass to gitwatch.
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
	Paused        bool    // --paused (gitwatchd extension, not gitwatch:
	//                       config-level so a pause survives restarts)
	Raw string // the config line this spec came from, for change detection

	targetIndex int // which arg was the target, so add can persist it resolved
}

func (s RepoSpec) Name() string { return filepath.Base(s.Path) }

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
			next() // inotify events: accepted, no-op (we watch a fixed gitwatch-like set)
		case "--paused":
			spec.Paused = true // gitwatchd extension (see RepoSpec)
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

// Render the current date exactly as upstream does: the -d value passes to
// date(1) verbatim (the user includes the leading +), and a format date(1)
// rejects yields an empty string.
func formattedDate(fmt string) string {
	code, out := runCommand("date", []string{fmt}, "")
	if code != 0 {
		return ""
	}
	return out
}
