import Foundation
import Testing

// The daemon publishes error state to a file; the menu and `gitwatchd status`
// are both views over it. These tests pin the file round trip.

private func withTemporaryStateDir(_ body: () throws -> Void) rethrows {
    let dir = TestDirs.fresh("app-support")
    try! FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
    AppSupport.overrideDir = dir
    StateStore.shared.prune(keeping: [])
    defer { AppSupport.overrideDir = nil }
    try body()
}

@Suite("Daemon state file", .serialized)
struct DaemonStateFile {

    @Test("a published error round-trips to a reader")
    func roundTrip() {
        withTemporaryStateDir {
            let tried = Date(timeIntervalSince1970: 1_000_000)
            StateStore.shared.set("/tmp/x", RepoStatus(
                errorLabel: "push failing", detail: "connection refused",
                attempts: 3, lastAttempt: tried, nextRetry: tried.addingTimeInterval(120)))
            let state = StateStore.read()
            let err = state?.errors["/tmp/x"]
            #expect(err?.errorLabel == "push failing")
            #expect(err?.detail == "connection refused")
            #expect(err?.attempts == 3)
            #expect(err?.lastAttempt == tried)
            #expect(err?.nextRetry == tried.addingTimeInterval(120))
            #expect(state?.pid == ProcessInfo.processInfo.processIdentifier)
        }
    }

    @Test("clearing an error and pruning unwatched repos remove entries")
    func clearAndPrune() {
        withTemporaryStateDir {
            let now = Date(timeIntervalSince1970: 1_000_000)
            StateStore.shared.set("/tmp/a", RepoStatus(errorLabel: "push failing", detail: nil,
                                                       attempts: 1, lastAttempt: now, nextRetry: nil))
            StateStore.shared.set("/tmp/b", RepoStatus(errorLabel: "commit failing", detail: nil,
                                                       attempts: 1, lastAttempt: now, nextRetry: nil))
            StateStore.shared.set("/tmp/a", nil)
            #expect(StateStore.read()?.errors.keys.sorted() == ["/tmp/b"])
            StateStore.shared.prune(keeping: [])
            #expect(StateStore.read()?.errors.isEmpty == true)
            #expect(!StateStore.shared.hasErrors)
        }
    }
}
