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

// Where the daemon keeps its runtime files. GITWATCHD_STATE_DIR overrides the
// lot, which is how the tests get a daemon of their own.
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

// The autostart wish gets its own file: state.json is the daemon's error
// channel for `status`, written whole on every change.
func autostartWishPath() string { return filepath.Join(stateDir(), "autostart") }

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

// The state file: one JSON object of RepoStatus keyed by repo path. The daemon
// is the only writer, every gitwatchd process a reader.
type stateFile struct {
	mu sync.Mutex // one whole-file rewrite at a time
}

var daemonState stateFile

// Takes no lock: writes land by rename, so a reader always sees one whole
// version of the file.
func (s *stateFile) statuses() map[string]RepoStatus {
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

// Record one repo's error state, or clear it when status is nil.
func (s *stateFile) setStatus(repoPath string, status *RepoStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := s.statuses()
	if status == nil {
		delete(statuses, repoPath)
	} else {
		statuses[repoPath] = *status
	}
	s.write(statuses)
}

// Forget the repos that have left the config.
func (s *stateFile) keepOnly(watched map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := s.statuses()
	for path := range statuses {
		if !watched[path] {
			delete(statuses, path)
		}
	}
	s.write(statuses)
}

func (s *stateFile) write(statuses map[string]RepoStatus) {
	os.MkdirAll(stateDir(), 0o755)
	raw, err := json.Marshal(statuses)
	if err != nil {
		return
	}
	// Write beside the file and rename over it, so no reader ever sees a
	// half-written state.
	tmp := statePath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		os.Rename(tmp, statePath())
	}
}

// The running daemon: one watcher per watchable repo in the config, kept in
// step with the file as it is edited.
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

// Bring the watchers in line with the config: stop the ones whose repo left,
// was paused or had its flags edited, and start one for every watchable repo
// that has none.
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
	daemonState.keepOnly(watched)

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
		w, err := newRepoWatcher(s, daemonState.setStatus, d.logf)
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

// Watch the config file's directory and report every change to the file
// itself (editors typically replace the file, so watching the path breaks).
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

// Read from an inotify fd until it closes, calling onEvent for every event.
// Each event is a fixed-size header followed by Len bytes of NUL-padded name,
// and the read buffer has to be big enough to hold a whole one.
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

// Ask the receiver to run once more. A wakeup already queued covers whatever
// just happened (the receiver reads current state), so a full channel is a
// success, not a reason to block.
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

// Recursive inotify watcher for one repo, with a debounce (gitwatch's -s) and
// .git-churn filtering so our own commits don't retrigger the watcher. Also
// honors gitwatch's -x exclude patterns.
//
// On top of the gitwatch cycle it keeps an additive reliability layer: per-repo
// error state and automatic push retries with backoff. The layer never changes
// what a commit cycle does; it only re-runs the push stage of one that failed.
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

	// Called after every cycle with the repo path and its error state
	// (nil while healthy); the daemon publishes it for `status`.
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
	d := time.Duration(float64(backoffFirst) * math.Pow(2, float64(afterFailures-1)))
	if d > backoffCap {
		return backoffCap
	}
	return d
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

// Hand this repo's error state (nil while healthy) to the state file, where
// `gitwatchd status` reads it.
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
