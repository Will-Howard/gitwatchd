package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Recursive inotify watcher for one repo, with a debounce (gitwatch's -s) and
// .git-churn filtering so our own commits don't retrigger the watcher. Also
// honors gitwatch's -x exclude patterns.
//
// On top of the gitwatch cycle it keeps an additive reliability layer: per-repo
// error state and automatic push retries with backoff. The layer never changes
// what a commit cycle does; it only re-runs the push stage of one that failed.

// The event set gitwatch passes to inotifywait:
// close_write,move,move_self,delete,create,modify.
const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_MOVE_SELF | syscall.IN_DELETE | syscall.IN_DELETE_SELF |
	syscall.IN_CREATE | syscall.IN_MODIFY

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
		w.addRecursive(path) // a new subtree starts being watched immediately
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
