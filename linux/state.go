package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// A repo's current error, as published by the daemon. Watchers write it, and
// `gitwatchd status` is a view over it. Stored as one JSON file rewritten
// atomically; the daemon is the only writer.
type RepoStatus struct {
	ErrorLabel  string `json:"errorLabel"`
	Detail      string `json:"detail,omitempty"`
	Attempts    int    `json:"attempts"`
	LastAttempt int64  `json:"lastAttempt"`
	NextRetry   *int64 `json:"nextRetry,omitempty"`
}

func stateDir() string {
	if d := os.Getenv("GITWATCHD_STATE_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".local", "state")
	}
	return filepath.Join(base, "gitwatchd")
}

func statePath() string   { return filepath.Join(stateDir(), "state.json") }
func pidfilePath() string { return filepath.Join(stateDir(), "gitwatchd.pid") }
func logfilePath() string { return filepath.Join(stateDir(), "daemon.log") }

var stateMu sync.Mutex

func stateErrors() map[string]RepoStatus {
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return map[string]RepoStatus{}
	}
	var out map[string]RepoStatus
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]RepoStatus{}
	}
	return out
}

func stateSet(repoPath string, status *RepoStatus) {
	stateMu.Lock()
	defer stateMu.Unlock()
	errs := stateErrors()
	if status == nil {
		delete(errs, repoPath)
	} else {
		errs[repoPath] = *status
	}
	stateWrite(errs)
}

func statePrune(keep map[string]bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	errs := stateErrors()
	for path := range errs {
		if !keep[path] {
			delete(errs, path)
		}
	}
	stateWrite(errs)
}

func stateWrite(errs map[string]RepoStatus) {
	os.MkdirAll(stateDir(), 0o755)
	raw, err := json.Marshal(errs)
	if err != nil {
		return
	}
	tmp := statePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, statePath())
	}
}
