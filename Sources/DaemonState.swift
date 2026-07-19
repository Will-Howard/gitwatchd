import Foundation

enum AppSupport {
    /// Tests point this at a scratch directory; nil means the real location.
    static var overrideDir: String?

    static var dir: String {
        overrideDir ?? (NSHomeDirectory() as NSString)
            .appendingPathComponent("Library/Application Support/gitwatchd")
    }
}

/// A repo's current error, as published by the daemon.
struct RepoStatus: Codable, Equatable {
    var errorLabel: String
    var detail: String?
    var attempts: Int
    var lastAttempt: Date
    var nextRetry: Date?
}

struct DaemonState: Codable {
    var writtenAt: Date
    var pid: Int32
    var errors: [String: RepoStatus]   // repo path -> current error
}

/// The daemon's published state: watchers write into it, and the menu and
/// `gitwatchd status` are both views over it (the CLI via the file on disk).
final class StateStore {
    static let shared = StateStore()
    private let lock = NSLock()
    private var errors: [String: RepoStatus] = [:]

    static var file: String { (AppSupport.dir as NSString).appendingPathComponent("state.json") }

    func set(_ path: String, _ status: RepoStatus?) {
        lock.lock(); defer { lock.unlock() }
        errors[path] = status
        persist()
    }

    func prune(keeping paths: Set<String>) {
        lock.lock(); defer { lock.unlock() }
        errors = errors.filter { paths.contains($0.key) }
        persist()
    }

    func status(for path: String) -> RepoStatus? {
        lock.lock(); defer { lock.unlock() }
        return errors[path]
    }

    var hasErrors: Bool {
        lock.lock(); defer { lock.unlock() }
        return !errors.isEmpty
    }

    private func persist() {   // callers hold the lock
        let state = DaemonState(writtenAt: Date(),
                                pid: ProcessInfo.processInfo.processIdentifier,
                                errors: errors)
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .secondsSince1970
        encoder.outputFormatting = .sortedKeys
        try? FileManager.default.createDirectory(atPath: AppSupport.dir, withIntermediateDirectories: true)
        if let data = try? encoder.encode(state) {
            try? data.write(to: URL(fileURLWithPath: Self.file), options: .atomic)
        }
    }

    static func read() -> DaemonState? {
        guard let data = FileManager.default.contents(atPath: file) else { return nil }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .secondsSince1970
        return try? decoder.decode(DaemonState.self, from: data)
    }
}
