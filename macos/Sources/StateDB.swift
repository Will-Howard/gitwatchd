import Foundation
import SQLite3

enum AppSupport {
    /// Tests point this at a scratch directory; nil means the real location.
    static var overrideDir: String?

    static var dir: String {
        overrideDir ?? (NSHomeDirectory() as NSString)
            .appendingPathComponent("Library/Application Support/gitwatchd")
    }
}

/// Durable key-value state in a SQLite file. Connections are ephemeral (one
/// per call): the daemon and CLI are separate processes, and SQLite plus a
/// busy timeout arbitrates between them.
enum StateDB {
    static var path: String { (AppSupport.dir as NSString).appendingPathComponent("gitwatchd.db") }

    private static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    private static func withDB<T>(_ body: (OpaquePointer) -> T?) -> T? {
        try? FileManager.default.createDirectory(atPath: AppSupport.dir, withIntermediateDirectories: true)
        var db: OpaquePointer?
        guard sqlite3_open(path, &db) == SQLITE_OK, let db else {
            sqlite3_close(db)
            return nil
        }
        defer { sqlite3_close(db) }
        sqlite3_busy_timeout(db, 1000)
        sqlite3_exec(db, "CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY, value TEXT NOT NULL)",
                     nil, nil, nil)
        return body(db)
    }

    static func set(_ key: String, _ value: String?) {
        _ = withDB { db -> Bool? in
            var stmt: OpaquePointer?
            let sql = value == nil ? "DELETE FROM state WHERE key = ?1"
                                   : "REPLACE INTO state(key, value) VALUES(?1, ?2)"
            guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return nil }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, key, -1, transient)
            if let value { sqlite3_bind_text(stmt, 2, value, -1, transient) }
            sqlite3_step(stmt)
            return true
        }
    }

    static func get(_ key: String) -> String? {
        withDB { db in
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, "SELECT value FROM state WHERE key = ?1", -1, &stmt, nil) == SQLITE_OK
            else { return nil }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, key, -1, transient)
            guard sqlite3_step(stmt) == SQLITE_ROW, let value = sqlite3_column_text(stmt, 0) else { return nil }
            return String(cString: value)
        }
    }

    static func pairs(prefix: String) -> [String: String] {
        withDB { db in
            var stmt: OpaquePointer?
            guard sqlite3_prepare_v2(db, "SELECT key, value FROM state WHERE key GLOB ?1", -1, &stmt, nil) == SQLITE_OK
            else { return nil }
            defer { sqlite3_finalize(stmt) }
            sqlite3_bind_text(stmt, 1, prefix + "*", -1, transient)
            var out: [String: String] = [:]
            while sqlite3_step(stmt) == SQLITE_ROW {
                if let key = sqlite3_column_text(stmt, 0), let value = sqlite3_column_text(stmt, 1) {
                    out[String(cString: key)] = String(cString: value)
                }
            }
            return out
        } ?? [:]
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

/// The daemon's published per-repo error state: watchers write it, and the
/// menu and `gitwatchd status` are both views over it.
enum StateStore {
    private static let prefix = "error:"

    static func set(_ repoPath: String, _ status: RepoStatus?) {
        StateDB.set(prefix + repoPath, status.flatMap(encode))
    }

    static func status(for repoPath: String) -> RepoStatus? {
        StateDB.get(prefix + repoPath).flatMap(decode)
    }

    static func errors() -> [String: RepoStatus] {
        var out: [String: RepoStatus] = [:]
        for (key, value) in StateDB.pairs(prefix: prefix) {
            if let status = decode(value) { out[String(key.dropFirst(prefix.count))] = status }
        }
        return out
    }

    static var hasErrors: Bool { !StateDB.pairs(prefix: prefix).isEmpty }

    static func prune(keeping paths: Set<String>) {
        for key in StateDB.pairs(prefix: prefix).keys
        where !paths.contains(String(key.dropFirst(prefix.count))) {
            StateDB.set(key, nil)
        }
    }

    private static func encode(_ status: RepoStatus) -> String? {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .secondsSince1970
        return (try? encoder.encode(status)).map { String(decoding: $0, as: UTF8.self) }
    }

    private static func decode(_ value: String) -> RepoStatus? {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .secondsSince1970
        return try? decoder.decode(RepoStatus.self, from: Data(value.utf8))
    }
}
