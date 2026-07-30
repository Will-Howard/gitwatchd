import Foundation
import Testing

// The daemon publishes error state into the SQLite state DB; the menu and
// `gitwatchd status` are both views over it.

private func withTemporaryStateDir(_ body: () throws -> Void) rethrows {
    AppSupport.overrideDir = TestDirs.fresh("app-support")
    defer { AppSupport.overrideDir = nil }
    try body()
}

@Suite("Daemon state (SQLite)", .serialized)
struct DaemonStateDB {

    @Test("a published error round-trips to a reader")
    func roundTrip() {
        withTemporaryStateDir {
            let tried = Date(timeIntervalSince1970: 1_000_000)
            StateStore.set("/tmp/x", RepoStatus(
                errorLabel: "push failing", detail: "connection refused",
                attempts: 3, lastAttempt: tried, nextRetry: tried.addingTimeInterval(120)))
            let err = StateStore.status(for: "/tmp/x")
            #expect(err?.errorLabel == "push failing")
            #expect(err?.detail == "connection refused")
            #expect(err?.attempts == 3)
            #expect(err?.lastAttempt == tried)
            #expect(err?.nextRetry == tried.addingTimeInterval(120))
            #expect(StateStore.errors().keys.sorted() == ["/tmp/x"])
        }
    }

    @Test("clearing an error and pruning unwatched repos remove entries")
    func clearAndPrune() {
        withTemporaryStateDir {
            let now = Date(timeIntervalSince1970: 1_000_000)
            StateStore.set("/tmp/a", RepoStatus(errorLabel: "push failing", detail: nil,
                                                attempts: 1, lastAttempt: now, nextRetry: nil))
            StateStore.set("/tmp/b", RepoStatus(errorLabel: "commit failing", detail: nil,
                                                attempts: 1, lastAttempt: now, nextRetry: nil))
            StateStore.set("/tmp/a", nil)
            #expect(StateStore.errors().keys.sorted() == ["/tmp/b"])
            StateStore.prune(keeping: [])
            #expect(StateStore.errors().isEmpty)
            #expect(!StateStore.hasErrors)
        }
    }

    @Test("plain settings share the same database")
    func settings() {
        withTemporaryStateDir {
            StateDB.set("launch-at-login", "off")
            #expect(StateDB.get("launch-at-login") == "off")
            StateDB.set("launch-at-login", "on")
            #expect(StateDB.get("launch-at-login") == "on")
        }
    }
}
