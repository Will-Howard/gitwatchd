package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// GITWATCHD_STATE_DIR overrides, which is how tests get a daemon of their own.
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

func statePath() string     { return filepath.Join(stateDir(), "state.json") }
func stateLockPath() string { return filepath.Join(stateDir(), "state.lock") }
func pidfilePath() string   { return filepath.Join(stateDir(), "gitwatchd.pid") }
func logfilePath() string   { return filepath.Join(stateDir(), "daemon.log") }

// The event set gitwatch passes to inotifywait:
// close_write,move,move_self,delete,create,modify.
const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_MOVE_SELF | syscall.IN_DELETE | syscall.IN_DELETE_SELF |
	syscall.IN_CREATE | syscall.IN_MODIFY

// A repo's current error, as published by the daemon. Watchers write it, and
// `gitwatchd status` is a view over it.
type RepoStatus struct {
	ErrorLabel  string `json:"errorLabel"`
	Detail      string `json:"detail,omitempty"`
	Attempts    int    `json:"attempts"`
	LastAttempt int64  `json:"lastAttempt"`
	NextRetry   *int64 `json:"nextRetry,omitempty"`
}

// Everything gitwatchd persists; a healthy repo has no entry under "repos".
// The shape is the cross-platform contract (names match macos/Sources/StateDB.swift),
// pretty-printed in stable key order because people read and diff it.
type persistedState struct {
	LaunchAtLogin string                `json:"launch-at-login,omitempty"` // "on", "off", absent: never asked
	Repos         map[string]RepoStatus `json:"repos,omitempty"`
}

// Lock-free: writes land by rename. Absent or unreadable state reads as empty.
func readState() persistedState {
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return persistedState{}
	}
	var s persistedState
	if json.Unmarshal(raw, &s) != nil {
		return persistedState{}
	}
	return s
}

// Daemon and CLI both rewrite the whole file; the flock stops one from writing
// off a stale read. The lock is its own file, never state.json: renaming over a
// locked file leaves the holder pinning an unlinked inode while the next writer
// locks its replacement.
func updateState(change func(*persistedState)) {
	os.MkdirAll(stateDir(), 0o755)
	lock, err := os.OpenFile(stateLockPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	s := readState()
	change(&s)
	writeState(s)
}

func writeState(s persistedState) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	tmp := statePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, statePath())
	}
}

func repoStatuses() map[string]RepoStatus {
	statuses := readState().Repos
	if statuses == nil {
		return map[string]RepoStatus{}
	}
	return statuses
}

func setRepoStatus(repoPath string, status *RepoStatus) {
	updateState(func(s *persistedState) {
		if status == nil {
			delete(s.Repos, repoPath)
			return
		}
		if s.Repos == nil {
			s.Repos = map[string]RepoStatus{}
		}
		s.Repos[repoPath] = *status
	})
}

func keepOnlyRepos(watched map[string]bool) {
	updateState(func(s *persistedState) {
		for path := range s.Repos {
			if !watched[path] {
				delete(s.Repos, path)
			}
		}
	})
}

func launchAtLogin() (on bool, recorded bool) {
	value := readState().LaunchAtLogin
	return value != "off", value != ""
}

func setLaunchAtLogin(on bool) {
	value := "off"
	if on {
		value = "on"
	}
	updateState(func(s *persistedState) { s.LaunchAtLogin = value })
}

// One watcher per watchable repo, kept in step with the config file as it is edited.
type daemon struct {
	watchers      map[string]*repoWatcher // by repo path
	previousSpecs map[string]*RepoSpec    // the last config seen, paused entries included
	logf          func(format string, args ...any)
}

func runDaemon() int {
	lock, err := acquireDaemonLock()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ daemon already running")
		return 1
	}
	defer lock.Close()

	d := &daemon{
		watchers:      map[string]*repoWatcher{},
		previousSpecs: map[string]*RepoSpec{},
		logf: func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		},
	}
	d.logf("gitwatchd %s: watching config %s", version, configPath())
	if msg := reconcileAutostart(); msg != "" {
		d.logf("%s", msg)
	}
	d.reloadConfig()

	configChanged := make(chan struct{}, 1)
	go watchConfigFile(configChanged)
	go func() {
		for range configChanged {
			time.Sleep(300 * time.Millisecond) // let editors finish the save
			// Whatever else the save queued is covered by this one reload.
			select {
			case <-configChanged:
			default:
			}
			d.reloadConfig()
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	for _, w := range d.watchers {
		w.stopWatching()
	}
	d.logf("gitwatchd stopped")
	return 0
}

func (d *daemon) reloadConfig() {
	specs, errs := configLoad()
	for _, e := range errs {
		d.logf("config: %s: %s", e.Label, e.Reason)
	}

	desired := map[string]*RepoSpec{}
	watched := map[string]bool{}
	for _, s := range specs {
		desired[s.Path] = s
		watched[s.Path] = true
	}
	keepOnlyRepos(watched)

	for path, w := range d.watchers {
		s, wanted := desired[path]
		if wanted && !s.Paused && s.Raw == w.spec.Raw {
			continue
		}
		w.stopWatching()
		delete(d.watchers, path)
		d.logf("stopped watching %s", w.spec.Name())
	}

	for path, s := range desired {
		if s.Paused {
			continue
		}
		if _, running := d.watchers[path]; running {
			continue
		}
		w, err := newRepoWatcher(s, setRepoStatus, d.logf)
		if err != nil {
			d.logf("could not watch %s: %v", s.Name(), err)
			continue
		}
		d.watchers[path] = w
		w.publishStatus() // surface watch-registration problems immediately
		d.logf("watching %s (%s)", s.Name(), s.Path)

		prev, existed := d.previousSpecs[path]
		resumed := existed && prev.Paused
		if s.CommitOnStart || resumed {
			w.flushNow() // -f, or a resume catching up on what piled up
		}
	}

	d.previousSpecs = desired
}

// Watch the directory, not the file: editors replace the file on save.
func watchConfigFile(changed chan struct{}) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return
	}
	if _, err := syscall.InotifyAddWatch(fd, filepath.Dir(configPath()), watchMask); err != nil {
		syscall.Close(fd)
		return
	}
	configFile := filepath.Base(configPath())
	readInotifyEvents(fd, func(_ int, _ uint32, name string) {
		if name == configFile {
			queueWakeup(changed)
		}
	})
}

// Each event is a fixed header plus Len bytes of NUL-padded name; the buffer
// must be big enough to hold a whole one.
func readInotifyEvents(fd int, onEvent func(watchDescriptor int, mask uint32, name string)) {
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			return // the fd was closed
		}
		for offset := 0; offset+syscall.SizeofInotifyEvent <= n; {
			event := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			name := ""
			if event.Len > 0 {
				start := offset + syscall.SizeofInotifyEvent
				name = string(bytes.TrimRight(buf[start:start+int(event.Len)], "\x00"))
			}
			offset += syscall.SizeofInotifyEvent + int(event.Len)
			onEvent(int(event.Wd), event.Mask, name)
		}
	}
}

// A wakeup already queued covers this one: the receiver reads current state.
func queueWakeup(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Single-instance guard: the daemon holds an exclusive flock on the pidfile
func acquireDaemonLock() (*os.File, error) {
	os.MkdirAll(stateDir(), 0o755)
	f, err := os.OpenFile(pidfilePath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}

func isDaemonRunning() bool {
	f, err := os.Open(pidfilePath())
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true // the daemon holds the exclusive lock
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func daemonPid() int {
	raw, err := os.ReadFile(pidfilePath())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

type repoError struct {
	outcome     Outcome
	lastAttempt time.Time
	attempts    int        // consecutive failures
	nextRetry   *time.Time // nil: no auto retry, waits for a change
}

// Recursive inotify watcher for one repo: gitwatch's -s debounce and -x excludes,
// plus .git-churn filtering so our own commits don't retrigger. Its reliability
// layer (error state, push retries with backoff) never changes what a commit
// cycle does; it only re-runs the push stage of one that failed.
type repoWatcher struct {
	spec *RepoSpec

	inotifyFD            int
	dirByWatchDescriptor map[int]string
	watchMu              sync.Mutex // guards dirByWatchDescriptor and watchFailure

	fileChanges   chan struct{}
	flushRequests chan struct{}
	stopping      chan struct{}
	stopped       chan struct{}

	lastError    *repoError
	watchFailure string // e.g. the inotify watch limit; surfaced, never fatal

	// Called after every cycle; nil while healthy. The daemon publishes it for `status`.
	onStatus func(path string, status *RepoStatus)
	logf     func(format string, args ...any)
}

func newRepoWatcher(spec *RepoSpec, onStatus func(string, *RepoStatus),
	logf func(string, ...any)) (*repoWatcher, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &repoWatcher{
		spec:                 spec,
		inotifyFD:            fd,
		dirByWatchDescriptor: map[int]string{},
		fileChanges:          make(chan struct{}, 64),
		flushRequests:        make(chan struct{}, 1),
		stopping:             make(chan struct{}),
		stopped:              make(chan struct{}),
		onStatus:             onStatus,
		logf:                 logf,
	}
	if spec.IsFileTarget() {
		// Watch the parent directory and filter to the file's name, so
		// atomic saves (write temp + rename) keep triggering.
		w.watchDirectory(filepath.Dir(spec.Path))
	} else {
		w.watchTree(spec.Path)
	}
	go readInotifyEvents(fd, w.handleInotifyEvent)
	go w.runCycles()
	return w, nil
}

// Register a watch, surfacing inotify limit exhaustion as repo state
// instead of crashing (fire-and-forget, like every other runtime failure).
func (w *repoWatcher) watchDirectory(dir string) {
	wd, err := syscall.InotifyAddWatch(w.inotifyFD, dir, watchMask)
	if err != nil {
		w.recordWatchFailure(dir, err)
		return
	}
	w.watchMu.Lock()
	w.dirByWatchDescriptor[wd] = dir
	w.watchMu.Unlock()
}

func (w *repoWatcher) recordWatchFailure(dir string, err error) {
	w.watchMu.Lock()
	defer w.watchMu.Unlock()
	if err == syscall.ENOSPC {
		w.watchFailure = "inotify watch limit reached: raise fs.inotify.max_user_watches " +
			"(sudo sysctl fs.inotify.max_user_watches=524288)"
	} else if !os.IsNotExist(err) {
		w.watchFailure = "watch failed for " + dir + ": " + err.Error()
	}
}

// inotify watches one directory each, so a repo means a watch per directory.
func (w *repoWatcher) watchTree(root string) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		w.watchDirectory(path)
		return nil
	})
}

func (w *repoWatcher) handleInotifyEvent(watchDescriptor int, mask uint32, name string) {
	if mask&syscall.IN_Q_OVERFLOW != 0 {
		queueWakeup(w.fileChanges) // events were dropped; a cycle will catch whatever changed
		return
	}
	w.watchMu.Lock()
	dir, known := w.dirByWatchDescriptor[watchDescriptor]
	if mask&syscall.IN_IGNORED != 0 {
		delete(w.dirByWatchDescriptor, watchDescriptor)
	}
	w.watchMu.Unlock()
	if !known {
		return
	}

	path := dir
	if name != "" {
		path = filepath.Join(dir, name)
	}

	if w.spec.IsFileTarget() {
		// The watch sits on the parent dir; only the target file counts.
		if path != w.spec.Path {
			return
		}
		queueWakeup(w.fileChanges)
		return
	}

	if strings.Contains(path, "/.git/") || strings.HasSuffix(path, "/.git") {
		return
	}
	if w.spec.Excludes(path) {
		return
	}
	if mask&syscall.IN_ISDIR != 0 && mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
		w.watchTree(path)
	}
	queueWakeup(w.fileChanges)
}

// Run one cycle soon regardless of file events (-f at start, resume catch-up).
func (w *repoWatcher) flushNow() {
	queueWakeup(w.flushRequests)
}

func (w *repoWatcher) runCycles() {
	defer close(w.stopped)
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	settle := time.Duration(w.spec.Settle * float64(time.Second))
	for {
		select {
		case <-w.fileChanges:
			// Each change resets the settle timer, so a burst of writes
			// lands as one commit (gitwatch's sleep-and-kill loop).
			debounce.Reset(settle)
		case <-debounce.C:
			w.report(autoCommit(w.spec), retry)
		case <-w.flushRequests:
			w.report(autoCommit(w.spec), retry)
		case <-retry.C:
			if w.lastError != nil {
				w.report(push(w.spec), retry)
			}
		case <-w.stopping:
			debounce.Stop()
			retry.Stop()
			return
		}
	}
}

// Push retry backoff: 30s doubling to a 5 minute cap.
const (
	backoffFirst = 30 * time.Second
	backoffCap   = 300 * time.Second
)

func backoffDelay(afterFailures int) time.Duration {
	if afterFailures <= 1 {
		return backoffFirst
	}
	// Compare before converting: past ~30 failures the product overflows
	// time.Duration and the conversion result is negative.
	d := float64(backoffFirst) * math.Pow(2, float64(afterFailures-1))
	if d > float64(backoffCap) {
		return backoffCap
	}
	return time.Duration(d)
}

// Fold an outcome into the error state, schedule the next automatic retry,
// and publish. A Clean pass says nothing about an earlier failed push (that
// commit is still unpushed), so it leaves the error and its retry timer alone.
func (w *repoWatcher) report(outcome Outcome, retry *time.Timer) {
	switch outcome.Kind {
	case Committed, Pushed:
		retry.Stop()
		w.lastError = nil
	case Clean, SkippedMerge:
		// no change
	case PushFailed:
		attempts := 1
		if w.lastError != nil {
			attempts = w.lastError.attempts + 1
		}
		delay := backoffDelay(attempts)
		next := time.Now().Add(delay)
		w.lastError = &repoError{outcome: outcome, lastAttempt: time.Now(),
			attempts: attempts, nextRetry: &next}
		retry.Reset(delay)
	case RebaseConflict, CommitFailed:
		retry.Stop()
		attempts := 1
		if w.lastError != nil {
			attempts = w.lastError.attempts + 1
		}
		w.lastError = &repoError{outcome: outcome, lastAttempt: time.Now(),
			attempts: attempts}
	}
	w.publishStatus()
	w.logOutcome(outcome)
}

func (w *repoWatcher) publishStatus() {
	w.watchMu.Lock()
	watchFailure := w.watchFailure
	w.watchMu.Unlock()

	var status *RepoStatus
	if w.lastError != nil {
		status = &RepoStatus{
			ErrorLabel:  w.lastError.outcome.ErrorLabel(),
			Detail:      w.lastError.outcome.Detail,
			Attempts:    w.lastError.attempts,
			LastAttempt: w.lastError.lastAttempt.Unix(),
		}
		if status.ErrorLabel == "" {
			status.ErrorLabel = "failing"
		}
		if w.lastError.nextRetry != nil {
			ts := w.lastError.nextRetry.Unix()
			status.NextRetry = &ts
		}
	} else if watchFailure != "" {
		status = &RepoStatus{ErrorLabel: "watch failing", Detail: watchFailure,
			Attempts: 1, LastAttempt: time.Now().Unix()}
	}
	w.onStatus(w.spec.Path, status)
}

func (w *repoWatcher) logOutcome(outcome Outcome) {
	name := w.spec.Name()
	switch outcome.Kind {
	case Committed:
		w.logf("%s: committed", name)
	case Pushed:
		w.logf("%s: pushed to %s", name, w.spec.Remote)
	case SkippedMerge:
		w.logf("%s: merge in progress, commit skipped", name)
	case CommitFailed:
		w.logf("%s: commit failing: %s", name, outcome.Detail)
	case RebaseConflict:
		w.logf("%s: rebase conflict: %s", name, outcome.Detail)
	case PushFailed:
		w.logf("%s: push failing: %s", name, outcome.Detail)
	}
}

func (w *repoWatcher) stopWatching() {
	close(w.stopping)
	syscall.Close(w.inotifyFD) // ends the event reader's blocking read
	<-w.stopped
}
