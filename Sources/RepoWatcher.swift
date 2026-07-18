import Foundation
import CoreServices

// Native FSEvents watcher for one repo, with a debounce (gitwatch's -s) and
// .git-churn filtering so our own commits don't retrigger the watcher. Also
// honors gitwatch's -x exclude patterns. No fswatch, no Homebrew, no binary.
//
// On top of the gitwatch cycle it keeps an additive reliability layer: per-repo
// error state and automatic push retries with backoff. The layer never changes
// what a commit cycle does; it only re-runs the push stage of one that failed.
final class RepoWatcher {
    let spec: RepoSpec
    var path: String { spec.path }
    private let onActivity: (RepoWatcher, CommitOutcome) -> Void

    private var stream: FSEventStreamRef?
    private var pending: DispatchWorkItem?
    private var retryWork: DispatchWorkItem?
    private let queue = DispatchQueue(label: "gitwatchd.watch")

    // Pause is config-level state (--paused on the repo's line) so it survives
    // restarts; the watcher just mirrors its spec, and toggling goes through
    // Config.setPaused + reload.
    var paused: Bool { spec.paused }

    /// Error state surfaced in the menu; nil while healthy.
    struct RepoError {
        var outcome: CommitOutcome   // one of the failure cases
        var lastAttempt: Date
        var attempts: Int            // consecutive failures
        var nextRetry: Date?         // nil: no auto retry, waits for a change
    }
    private(set) var lastError: RepoError?

    /// Only a failed push is worth retrying when the network returns; conflicts
    /// and commit failures need something to change first.
    var wantsNetworkRetry: Bool {
        if case .pushFailed = lastError?.outcome { return !paused }
        return false
    }

    init(spec: RepoSpec, onActivity: @escaping (RepoWatcher, CommitOutcome) -> Void) {
        self.spec = spec
        self.onActivity = onActivity
    }

    func start() {
        var ctx = FSEventStreamContext(
            version: 0,
            info: Unmanaged.passUnretained(self).toOpaque(),
            retain: nil, release: nil, copyDescription: nil)

        let callback: FSEventStreamCallback = { _, info, count, paths, _, _ in
            let me = Unmanaged<RepoWatcher>.fromOpaque(info!).takeUnretainedValue()
            let cPaths = paths.assumingMemoryBound(to: UnsafePointer<CChar>.self)
            for i in 0..<count {
                let p = String(cString: cPaths[i])
                if p.contains("/.git/") || p.hasSuffix("/.git") { continue }
                if me.spec.excludes(p) { continue }
                me.schedule()
                break
            }
        }

        stream = FSEventStreamCreate(
            kCFAllocatorDefault, callback, &ctx,
            [path] as CFArray,
            FSEventStreamEventId(kFSEventStreamEventIdSinceNow),
            0.3, // kernel-side coalescing
            UInt32(kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer))

        guard let stream else { return }
        FSEventStreamSetDispatchQueue(stream, queue)
        FSEventStreamStart(stream)
    }

    private func schedule() {
        guard !paused else { return }
        pending?.cancel()
        let work = DispatchWorkItem { [weak self] in
            guard let self, !self.paused else { return }
            self.runCycle()
        }
        pending = work
        queue.asyncAfter(deadline: .now() + spec.settle, execute: work)
    }

    func flushNow() {
        queue.async { [weak self] in self?.runCycle() }
    }

    /// Immediate retry, from the menu's Retry Now, resuming after a pause, or a
    /// network-regained signal. No-op while healthy.
    func retryNow() {
        queue.async { [weak self] in
            guard let self, let err = self.lastError, !self.paused else { return }
            if case .commitFailed = err.outcome {
                self.runCycle()   // the commit never happened; run a full cycle
            } else {
                self.runRetry()   // the commit exists; redo the push stage only
            }
        }
    }

    // MARK: - Cycle + retry (watcher queue only)

    private func runCycle() {
        report(Git.autoCommit(spec))
    }

    private func runRetry() {
        guard lastError != nil else { return }
        report(Git.push(spec))
    }

    /// Fold an outcome into the error state, schedule the next automatic retry,
    /// and tell the UI. A .clean pass says nothing about an earlier failed push
    /// (that commit is still unpushed), so it leaves the error and its retry
    /// timer alone.
    private func report(_ outcome: CommitOutcome) {
        switch outcome {
        case .committed, .pushed:
            retryWork?.cancel()
            lastError = nil
        case .clean, .skippedMerge:
            break
        case .pushFailed:
            retryWork?.cancel()
            let attempts = (lastError?.attempts ?? 0) + 1
            let delay = Backoff.delay(afterFailures: attempts)
            lastError = RepoError(outcome: outcome, lastAttempt: Date(),
                                  attempts: attempts,
                                  nextRetry: Date().addingTimeInterval(delay))
            let work = DispatchWorkItem { [weak self] in
                guard let self, !self.paused else { return }
                self.runRetry()
            }
            retryWork = work
            queue.asyncAfter(deadline: .now() + delay, execute: work)
        case .rebaseConflict, .commitFailed:
            retryWork?.cancel()
            let attempts = (lastError?.attempts ?? 0) + 1
            lastError = RepoError(outcome: outcome, lastAttempt: Date(),
                                  attempts: attempts, nextRetry: nil)
        }
        onActivity(self, outcome)
    }

    func stop() {
        pending?.cancel()
        retryWork?.cancel()
        guard let stream else { return }
        FSEventStreamStop(stream)
        FSEventStreamInvalidate(stream)
        FSEventStreamRelease(stream)
        self.stream = nil
    }
}
