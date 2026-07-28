package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type ConfigError struct {
	Label    string // repo name, or the offending line for parse errors
	Reason   string // short fixed vocabulary, e.g. "repo not found"
	Detail   string // full path or config line, for status
	RepoPath string // set when there is a path to re-check for healing
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
# ` + "`gitwatchd help`" + ` explains each flag. Examples:
#   ~/code/my-notes
#   -s 5 -r origin -b main ~/code/blog
# (from a terminal, ` + "`gitwatchd .`" + ` adds the current repo here for you)
`
	os.WriteFile(configPath(), []byte(template), 0o644)
}

// Non-comment, non-empty raw lines.
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

// Parsed specs (invalid lines are skipped; surface them via configLineErrors).
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

// Config lines that don't parse into a spec at all, with the reason.
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

// One pass over the config: the watchable specs, plus every entry that
// can't be watched (unparseable line, missing path, not a git repo).
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

// Append a repo line (raw gitwatch args). Creates the file if needed.
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

// True if `spec` is the repo the user means by `needle`: full path,
// tilde path, or folder name.
func configMatches(spec *RepoSpec, needle string) bool {
	return spec.Path == expandTilde(needle) || spec.Name() == needle || spec.Path == needle
}

// Remove matching lines; returns how many were removed.
func configRemove(needle string) int {
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
		if spec != nil && configMatches(spec, needle) {
			removed++
			continue
		}
		kept = append(kept, rawLine)
	}
	os.WriteFile(configPath(), []byte(strings.Join(kept, "\n")), 0o644)
	return removed
}

// Flip the --paused token on config lines matching `needle` (by full
// path or repo name, like remove). Pause lives in the config, not daemon
// state, so it survives daemon and machine restarts. Returns the number
// of lines changed.
func configSetPaused(needle string, paused bool) int {
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
		if spec == nil || !configMatches(spec, needle) {
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

// The pure rewrite behind configSetPaused: if `line` watches `path` and its
// paused state differs, return the line with --paused added (in front)
// or removed; else ok=false for "leave this line alone".
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

// The tokenizer has no escape syntax: a value with both quote kinds
// cannot round-trip.
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
			// Inside quotes: the matching quote closes; everything else is literal.
			if ch == openQuote {
				openQuote = 0
			} else {
				current.WriteRune(ch)
			}
		case ch == '"' || ch == '\'':
			openQuote = ch // start a quoted section
		case unicode.IsSpace(ch):
			finishArg() // whitespace separates arguments
		default:
			current.WriteRune(ch)
		}
	}
	finishArg()
	return args
}
