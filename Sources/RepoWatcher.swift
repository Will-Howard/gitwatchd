import Foundation
import CoreServices

// Native FSEvents watcher for one repo, with a debounce (gitwatch's -s) and
// .git-churn filtering so our own commits don't retrigger the watcher. Also
// honors gitwatch's -x exclude patterns. No fswatch, no Homebrew, no binary.
final class RepoWatcher {
    let spec: RepoSpec
    var path: String { spec.path }
    private let onActivity: (RepoWatcher, String) -> Void

    private var stream: FSEventStreamRef?
    private var pending: DispatchWorkItem?
    private let queue = DispatchQueue(label: "gitwatchd.watch")

    var paused = false

    init(spec: RepoSpec, onActivity: @escaping (RepoWatcher, String) -> Void) {
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
                if me.isExcluded(p) { continue }
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

    private func isExcluded(_ fullPath: String) -> Bool {
        guard !spec.exclude.isEmpty else { return false }
        let name = (fullPath as NSString).lastPathComponent
        for pattern in spec.exclude {
            if fnmatch(pattern, name, 0) == 0 || fnmatch(pattern, fullPath, 0) == 0 {
                return true
            }
        }
        return false
    }

    private func schedule() {
        guard !paused else { return }
        pending?.cancel()
        let work = DispatchWorkItem { [weak self] in
            guard let self, !self.paused else { return }
            self.onActivity(self, Git.autoCommit(self.spec))
        }
        pending = work
        queue.asyncAfter(deadline: .now() + spec.settle, execute: work)
    }

    func flushNow() {
        queue.async { [weak self] in
            guard let self else { return }
            self.onActivity(self, Git.autoCommit(self.spec))
        }
    }

    func stop() {
        guard let stream else { return }
        FSEventStreamStop(stream)
        FSEventStreamInvalidate(stream)
        FSEventStreamRelease(stream)
        self.stream = nil
    }
}
