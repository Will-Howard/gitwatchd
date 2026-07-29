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

// The event set gitwatch passes to inotifywait:
// close_write,move,move_self,delete,create,modify.
const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_MOVE_SELF | syscall.IN_DELETE | syscall.IN_DELETE_SELF |
	syscall.IN_CREATE | syscall.IN_MODIFY

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

// Recursive inotify watcher for one repo, with a debounce (gitwatch's -s) and
// .git-churn filtering so our own commits don't retrigger the watcher. Also
// honors gitwatch's -x exclude patterns.
//
// On top of the gitwatch cycle it keeps an additive reliability layer: per-repo
// error state and automatic push retries with backoff. The layer never changes
// what a commit cycle does; it only re-runs the push stage of one that failed.

type repoError struct {
	outcome     Outcome
	lastAttempt time.Time
	attempts    int        // consecutive failures
	nextRetry   *time.Time // nil: no auto retry, waits for a change
}

type repoWatcher struct {
	spec *RepoSpec

	fd  int
	wds map[int]string // watch descriptor -> directory path
	mu  sync.Mutex     // guards wds and watchErr

	events chan struct{}
	flush  chan struct{}
	stop   chan struct{}
	done   chan struct{}

	lastError *repoError
	watchErr  string // e.g. the inotify watch limit; surfaced, never fatal

	// Called after every cycle with the repo path and its error state
	// (nil while healthy); the daemon publishes it for `status`.
	onState func(path string, status *RepoStatus)
	logf    func(format string, args ...any)
}

func runDaemon() int {
	lock, err := acquireDaemonLock()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ daemon already running")
		return 1
	}
	defer lock.Close()

	logf := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}
	logf("gitwatchd %s: watching config %s", version, configPath())

	watchers := map[string]*repoWatcher{} // by repo path
	prevSpecs := map[string]*RepoSpec{}   // includes paused entries

	reload := func() {
		specs, errs := configLoad()
		for _, e := range errs {
			logf("config: %s: %s", e.Label, e.Reason)
		}

		desired := map[string]*RepoSpec{}
		for _, s := range specs {
			desired[s.Path] = s
		}

		keep := map[string]bool{}
		for path := range desired {
			keep[path] = true
		}
		statePrune(keep)

		for path, w := range watchers {
			s, wanted := desired[path]
			if wanted && !s.Paused && s.Raw == w.spec.Raw {
				continue
			}
			w.stopWatching()
			delete(watchers, path)
			logf("stopped watching %s", w.spec.Name())
		}

		for path, s := range desired {
			if s.Paused {
				continue
			}
			if _, running := watchers[path]; running {
				continue
			}
			w, err := newRepoWatcher(s, stateSet, logf)
			if err != nil {
				logf("could not watch %s: %v", s.Name(), err)
				continue
			}
			watchers[path] = w
			w.publish() // surface watch-registration problems immediately
			logf("watching %s (%s)", s.Name(), s.Path)

			prev, existed := prevSpecs[path]
			resumed := existed && prev.Paused
			if s.CommitOnStart || resumed {
				w.flushNow() // -f, or a resume catching up on what piled up
			}
		}

		prevSpecs = desired
	}

	reload()

	configEvents := make(chan struct{}, 1)
	go watchConfigFile(configEvents)
	go func() {
		for range configEvents {
			time.Sleep(300 * time.Millisecond) // let editors finish the save
			drain(configEvents)
			reload()
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	for _, w := range watchers {
		w.stopWatching()
	}
	logf("gitwatchd stopped")
	return 0
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// Watch the config file's directory and signal when the file changes
// (editors typically replace the file, so watching the path itself breaks).
func watchConfigFile(events chan struct{}) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return
	}
	dir := filepath.Dir(configPath())
	base := filepath.Base(configPath())
	if _, err := syscall.InotifyAddWatch(fd, dir, watchMask); err != nil {
		syscall.Close(fd)
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			return
		}
		if inotifyNamesContain(buf[:n], base) {
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}
}

func inotifyNamesContain(buf []byte, want string) bool {
	offset := 0
	for offset+syscall.SizeofInotifyEvent <= len(buf) {
		lenField := int(uint32frombytes(buf[offset+12 : offset+16]))
		name := ""
		if lenField > 0 {
			raw := buf[offset+syscall.SizeofInotifyEvent : offset+syscall.SizeofInotifyEvent+lenField]
			name = strings.TrimRight(string(raw), "\x00")
		}
		if name == want {
			return true
		}
		offset += syscall.SizeofInotifyEvent + lenField
	}
	return false
}

func uint32frombytes(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
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

func newRepoWatcher(spec *RepoSpec, onState func(string, *RepoStatus),
	logf func(string, ...any)) (*repoWatcher, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &repoWatcher{
		spec:    spec,
		fd:      fd,
		wds:     map[int]string{},
		events:  make(chan struct{}, 64),
		flush:   make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		onState: onState,
		logf:    logf,
	}
	if spec.IsFileTarget() {
		// Watch the parent directory and filter to the file's name, so
		// atomic saves (write temp + rename) keep triggering.
		w.addWatchTracked(filepath.Dir(spec.Path))
	} else {
		w.addRecursive(spec.Path)
	}
	go w.readLoop()
	go w.run()
	return w, nil
}

// Register a watch, surfacing inotify limit exhaustion as repo state
// instead of crashing (fire-and-forget, like every other runtime failure).
func (w *repoWatcher) addWatchTracked(dir string) {
	wd, err := syscall.InotifyAddWatch(w.fd, dir, watchMask)
	if err != nil {
		w.addWatchError(dir, err)
		return
	}
	w.mu.Lock()
	w.wds[wd] = dir
	w.mu.Unlock()
}

func (w *repoWatcher) addWatchError(dir string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == syscall.ENOSPC {
		w.watchErr = "inotify watch limit reached: raise fs.inotify.max_user_watches " +
			"(sudo sysctl fs.inotify.max_user_watches=524288)"
	} else if !os.IsNotExist(err) {
		w.watchErr = "watch failed for " + dir + ": " + err.Error()
	}
}

func (w *repoWatcher) addRecursive(root string) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		w.addWatchTracked(path)
		return nil
	})
}

func (w *repoWatcher) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.Read(w.fd, buf)
		if err != nil || n <= 0 {
			return // fd closed by Stop
		}
		offset := 0
		for offset+syscall.SizeofInotifyEvent <= n {
			raw := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			name := ""
			if raw.Len > 0 {
				b := buf[offset+syscall.SizeofInotifyEvent : offset+syscall.SizeofInotifyEvent+int(raw.Len)]
				name = string(bytes.TrimRight(b, "\x00"))
			}
			offset += syscall.SizeofInotifyEvent + int(raw.Len)
			w.handleEvent(int(raw.Wd), raw.Mask, name)
		}
	}
}

func (w *repoWatcher) handleEvent(wd int, mask uint32, name string) {
	if mask&syscall.IN_Q_OVERFLOW != 0 {
		w.signal() // events were dropped; a cycle will catch whatever changed
		return
	}
	w.mu.Lock()
	dir, known := w.wds[wd]
	if mask&syscall.IN_IGNORED != 0 {
		delete(w.wds, wd)
	}
	w.mu.Unlock()
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
		w.signal()
		return
	}

	if strings.Contains(path, "/.git/") || strings.HasSuffix(path, "/.git") {
		return
	}
	if w.spec.Excludes(path) {
		return
	}
	if mask&syscall.IN_ISDIR != 0 && mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
		w.addRecursive(path)
	}
	w.signal()
}

func (w *repoWatcher) signal() {
	select {
	case w.events <- struct{}{}:
	default:
	}
}

// Run one cycle soon regardless of file events (-f at start, resume catch-up).
func (w *repoWatcher) flushNow() {
	select {
	case w.flush <- struct{}{}:
	default:
	}
}

func (w *repoWatcher) run() {
	defer close(w.done)
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	settle := time.Duration(w.spec.Settle * float64(time.Second))
	for {
		select {
		case <-w.events:
			// Each change resets the settle timer, so a burst of writes
			// lands as one commit (gitwatch's sleep-and-kill loop).
			debounce.Reset(settle)
		case <-debounce.C:
			w.report(autoCommit(w.spec), retry)
		case <-w.flush:
			w.report(autoCommit(w.spec), retry)
		case <-retry.C:
			if w.lastError != nil {
				w.report(push(w.spec), retry)
			}
		case <-w.stop:
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
	w.publish()
	w.logOutcome(outcome)
}

func (w *repoWatcher) publish() {
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
	} else if err := w.watchError(); err != "" {
		status = &RepoStatus{ErrorLabel: "watch failing", Detail: err,
			Attempts: 1, LastAttempt: time.Now().Unix()}
	}
	w.onState(w.spec.Path, status)
}

func (w *repoWatcher) watchError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watchErr
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
	close(w.stop)
	syscall.Close(w.fd)
	<-w.done
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
