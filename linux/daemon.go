package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The headless daemon: one process, one flock-guarded pidfile, a watcher per
// configured repo, and a live reload whenever ~/.gitwatchd changes. Under
// systemd its stdout/stderr go to the journal; that is the log.

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
// for its whole life, so liveness checks can't be fooled by stale pids.
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
