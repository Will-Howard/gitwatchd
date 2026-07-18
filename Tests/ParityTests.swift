import Foundation
import Testing

// Differential parity tests: the same scenario runs through upstream
// gitwatch.sh (the model) and through our engine, each on its own
// identically-built world, and the observable git state afterwards must be
// identical. Scenario setup uses only plain git commands, so neither
// implementation touches the world until the measured cycle.
//
// Determinism: gitwatch's -f performs one commit cycle before entering its
// watch loop, and GW_INW_BIN lets us point the watcher at a stub that exits
// immediately, so the script does exactly one cycle and terminates. Every
// scenario passes an explicit -m without %d, since default messages and
// timestamps intentionally differ.

/// Everything a user could observe about a world after one cycle.
struct RepoState: Equatable, CustomStringConvertible {
    var commitCount: Int
    var lastMessage: String
    var pendingChanges: Int
    var branch: String
    var midMerge: Bool
    var midRebase: Bool
    var remoteCommitCount: Int      // -1 when the scenario has no remote
    var remoteLastMessage: String

    static func of(_ repo: TestRepo, remote: BareRemote?) -> RepoState {
        RepoState(commitCount: repo.commitCount,
                  lastMessage: repo.lastMessage,
                  pendingChanges: Git.pendingCount(repo.path),
                  branch: Git.currentBranch(repo.path),
                  midMerge: repo.midMerge,
                  midRebase: repo.midRebase,
                  remoteCommitCount: remote?.commitCount ?? -1,
                  remoteLastMessage: remote?.lastMessage ?? "")
    }

    var description: String {
        "commits=\(commitCount) last=\"\(lastMessage)\" pending=\(pendingChanges) "
            + "branch=\(branch) midMerge=\(midMerge) midRebase=\(midRebase) "
            + "remote(commits=\(remoteCommitCount) last=\"\(remoteLastMessage)\")"
    }
}

enum GitwatchReference {
    /// The vendored upstream script, resolved from this file's location so
    /// tests work regardless of the working directory.
    static let script = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent()
        .appendingPathComponent("Reference/gitwatch.sh").path

    /// A watcher stub that exits immediately: gitwatch runs its -f startup
    /// commit, the watch pipe hits EOF, and the script terminates.
    static let stubWatcher: String = {
        let dir = TestDirs.fresh("watch-stub")
        try! FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let stub = dir + "/fswatch"
        try! "#!/bin/sh\nexit 0\n".write(toFile: stub, atomically: true, encoding: .utf8)
        try! FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: stub)
        return stub
    }()

    /// Run upstream gitwatch for exactly one commit cycle on `target`.
    @discardableResult
    static func runOneCycle(flags: [String], target: String) -> (code: Int32, out: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/bash")
        p.arguments = [script, "-f"] + flags + [target]
        var env = ProcessInfo.processInfo.environment
        env["GW_INW_BIN"] = stubWatcher
        p.environment = env
        let pipe = Pipe(); p.standardOutput = pipe; p.standardError = pipe
        do { try p.run() } catch { return (-1, "\(error)") }
        DispatchQueue.global().asyncAfter(deadline: .now() + 20) {  // hang watchdog
            if p.isRunning { p.terminate() }
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, String(data: data, encoding: .utf8) ?? "")
    }
}

/// Build two identical worlds, run the model on one and our engine on the
/// other with the same flags, and return both fingerprints.
private func twins(flags: [String], remote: Bool, targetSuffix: String? = nil,
                   setup: (TestRepo, BareRemote?) -> Void) -> (model: RepoState, ours: RepoState) {
    func target(_ repo: TestRepo) -> String {
        targetSuffix.map { repo.path + "/" + $0 } ?? repo.path
    }
    let a = TestRepo()
    let ra = remote ? a.addOrigin() : nil
    setup(a, ra)
    GitwatchReference.runOneCycle(flags: flags, target: target(a))
    let model = RepoState.of(a, remote: ra)

    let b = TestRepo()
    let rb = remote ? b.addOrigin() : nil
    setup(b, rb)
    Git.autoCommit(RepoSpecParser.parse(flags + [target(b)]).spec!)
    let ours = RepoState.of(b, remote: rb)

    return (model, ours)
}

/// Plain-git scenario builders (no engine involvement).
private func seed(_ repo: TestRepo, _ file: String = "seed.txt") {
    repo.write(file, "seed\n")
    repo.git("add", "-A")
    repo.git("commit", "-q", "-m", "seed")
}

private func seedAndPush(_ repo: TestRepo) {
    seed(repo)
    repo.git("push", "-q", "origin", "main")
    repo.git("fetch", "-q", "origin")
    repo.git("branch", "-q", "--set-upstream-to=origin/main", "main")
}

private func colleaguePushes(_ remote: BareRemote, file: String, message: String) {
    let colleague = TestRepo(cloneOf: remote)
    colleague.write(file, "from the other machine\n")
    colleague.git("add", "-A")
    colleague.git("commit", "-q", "-m", message)
    colleague.git("push", "-q", "origin", "main")
}

private func conflictedMerge(_ repo: TestRepo) {
    repo.write("f.txt", "base\n")
    repo.git("add", "-A"); repo.git("commit", "-q", "-m", "base")
    repo.git("checkout", "-q", "-b", "side")
    repo.write("f.txt", "side\n")
    repo.git("add", "-A"); repo.git("commit", "-q", "-m", "side edit")
    repo.git("checkout", "-q", "main")
    repo.write("f.txt", "main\n")
    repo.git("add", "-A"); repo.git("commit", "-q", "-m", "main edit")
    repo.git("merge", "side")   // conflicts, leaving MERGE_HEAD
}

@Suite("Parity with upstream gitwatch (differential, model-based)")
struct GitwatchParity {

    @Test("a clean repo: neither side commits anything")
    func cleanRepo() {
        let r = twins(flags: ["-m", "cycle"], remote: false) { repo, _ in seed(repo) }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 1)
    }

    @Test("new changes: same commit, same message, clean tree afterwards")
    func plainCommit() {
        let r = twins(flags: ["-m", "cycle"], remote: false) { repo, _ in
            seed(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 2)
        #expect(r.ours.lastMessage == "cycle")
        #expect(r.ours.pendingChanges == 0)
    }

    @Test("with a remote: both push the commit")
    func pushToRemote() {
        let r = twins(flags: ["-m", "cycle", "-r", "origin", "-b", "main"], remote: true) { repo, _ in
            seedAndPush(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.remoteCommitCount == 2)
        #expect(r.ours.remoteLastMessage == "cycle")
    }

    @Test("an unreachable remote: the commit stays local on both, nothing crashes")
    func unreachableRemote() {
        let r = twins(flags: ["-m", "cycle", "-r", "origin", "-b", "main"], remote: true) { repo, _ in
            seed(repo)
            repo.setOriginURL(TestDirs.root + "/gone.git")
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 2, "fire-and-forget: the commit still happens")
    }

    @Test("-M: both skip the cycle while a merge is in progress")
    func mergeGuard() {
        let r = twins(flags: ["-m", "cycle", "-M"], remote: false) { repo, _ in
            conflictedMerge(repo)
        }
        #expect(r.ours == r.model)
        #expect(r.ours.midMerge, "the merge is left untouched on both sides")
    }

    @Test("without -M: both commit the conflicted merge, warts and all")
    func mergeCommitted() {
        let r = twins(flags: ["-m", "cycle"], remote: false) { repo, _ in
            conflictedMerge(repo)
        }
        #expect(r.ours == r.model)
        #expect(!r.ours.midMerge, "the cycle concluded the merge on both sides")
    }

    @Test("-R: both rebase in the remote's new commits and push on top")
    func rebaseHappyPath() {
        let r = twins(flags: ["-m", "cycle", "-r", "origin", "-b", "main", "-R"], remote: true) { repo, remote in
            seedAndPush(repo)
            colleaguePushes(remote!, file: "theirs.txt", message: "made elsewhere")
            repo.write("ours.txt", "made here\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.remoteCommitCount == 3, "seed, theirs, ours: one linear history")
        #expect(r.ours.remoteLastMessage == "cycle")
    }

    @Test("a subdirectory target: both commit only the subtree")
    func subdirectoryTarget() {
        let r = twins(flags: ["-m", "cycle"], remote: false, targetSuffix: "sub") { repo, _ in
            repo.write("sub/inner.txt", "v1\n")
            repo.git("add", "-A"); repo.git("commit", "-q", "-m", "seed")
            repo.write("sub/inner.txt", "v2\n")
            repo.write("outer.txt", "left alone\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 2)
        #expect(r.ours.pendingChanges == 1, "outer.txt stays uncommitted on both sides")
    }

    @Test("a file target: both commit only that file")
    func fileTarget() {
        let r = twins(flags: ["-m", "cycle"], remote: false, targetSuffix: "a.txt") { repo, _ in
            repo.write("a.txt", "v1\n")
            repo.git("add", "-A"); repo.git("commit", "-q", "-m", "seed")
            repo.write("a.txt", "v2\n")
            repo.write("b.txt", "left alone\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 2)
        #expect(r.ours.pendingChanges == 1, "b.txt stays uncommitted on both sides")
    }

    @Test("-R conflict: both leave the rebase in progress for the user")
    func rebaseConflict() {
        let r = twins(flags: ["-m", "cycle", "-r", "origin", "-b", "main", "-R"], remote: true) { repo, remote in
            seedAndPush(repo)
            colleaguePushes(remote!, file: "seed.txt", message: "conflicting edit")
            repo.write("seed.txt", "our conflicting edit\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.midRebase, "both sides stop mid-rebase; -M is the only guard")
    }
}
