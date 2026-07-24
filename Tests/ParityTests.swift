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
    var lastMessageBody: String       // multi-line for -l/-L, else == lastMessage
    var pendingChanges: Int
    var branch: String
    var midMerge: Bool
    var midRebase: Bool
    var remoteCommitCount: Int      // -1 when the scenario has no remote
    var remoteLastMessage: String

    static func of(_ repo: TestRepo, remote: BareRemote?) -> RepoState {
        RepoState(commitCount: repo.commitCount,
                  lastMessage: repo.lastMessage,
                  lastMessageBody: repo.lastMessageBody,
                  pendingChanges: Git.pendingCount(repo.path),
                  branch: Git.currentBranch(repo.path),
                  midMerge: repo.midMerge,
                  midRebase: repo.midRebase,
                  remoteCommitCount: remote?.commitCount ?? -1,
                  remoteLastMessage: remote?.lastMessage ?? "")
    }

    var description: String {
        "commits=\(commitCount) last=\"\(lastMessage)\" body=\"\(lastMessageBody)\" pending=\(pendingChanges) "
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
        var env = ProcessInfo.processInfo.environment
        env["GW_INW_BIN"] = stubWatcher
        return runProcess("/bin/bash", [script, "-f"] + flags + [target], env: env, timeout: 20)
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
    repo.trackOrigin()
}

private func colleaguePushes(_ remote: BareRemote, file: String, message: String) {
    let colleague = TestRepo(cloneOf: remote)
    colleague.write(file, "from the other machine\n")
    colleague.git("add", "-A")
    colleague.git("commit", "-q", "-m", message)
    colleague.git("push", "-q", "origin", "main")
}

/// Commit many long-named files, then dirty them all, so `git diff --name-only`
/// emits well over a pipe buffer's worth of names (here ~140KB). Enough that a
/// command which does not drain its stdin either takes a broken pipe or blocks.
private func manyChangedFiles(_ repo: TestRepo, count: Int = 700) {
    let pad = String(repeating: "x", count: 200)
    for i in 0..<count { repo.write("f\(i)_\(pad).txt", "v1\n") }
    repo.git("add", "-A")
    repo.git("commit", "-q", "-m", "seed")
    for i in 0..<count { repo.write("f\(i)_\(pad).txt", "v2\n") }
}

/// Make `git diff --name-only` write a warning to stderr (LF/CRLF renormalize)
/// while listing one file on stdout, to prove only stdout feeds the command.
private func crlfWarningSetup(_ repo: TestRepo) {
    repo.git("config", "core.autocrlf", "true")
    repo.write("f.txt", "a\nb\n")
    repo.git("-c", "core.autocrlf=false", "add", "f.txt")
    repo.git("-c", "core.autocrlf=false", "commit", "-q", "-m", "seed")
    repo.write("f.txt", "a\nb\nc\n")
}

private func makeExecutable(_ repo: TestRepo, _ file: String, _ body: String) {
    repo.write(file, body)
    try! FileManager.default.setAttributes([.posixPermissions: 0o755],
                                           ofItemAtPath: repo.path + "/" + file)
}

@Suite("Parity with upstream gitwatch")
struct GitwatchParity {

    @Test("a clean repo: neither side commits anything")
    func cleanRepo() {
        let r = twins(flags: ["-m", "cycle"], remote: false) { repo, _ in seed(repo) }
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 1)
    }

    @Test("sharp corner, kept for upstream parity: -d passes to date(1) raw and only the first %d is spliced")
    func rawDateFormatAndFirstTokenOnly() {
        let r = twins(flags: ["-m", "at %d then %d", "-d", "+%Y"], remote: false) { repo, _ in
            seed(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage.hasPrefix("at 2"), "got: \(r.ours.lastMessage)")
        #expect(r.ours.lastMessage.hasSuffix(" then %d"), "got: \(r.ours.lastMessage)")
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

    @Test("-c: the command's stdout is the commit message on both sides")
    func messageCommand() {
        let r = twins(flags: ["-m", "fallback", "-c", "echo checkpoint"], remote: false) { repo, _ in
            seed(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.model.lastMessage == "checkpoint", "green today: the model's behaviour, pinned directly")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "checkpoint")
    }

    @Test("-c beats -l when both are given: the command's output is the message")
    func messageCommandBeatsListChanges() {
        let r = twins(flags: ["-m", "fallback", "-l", "0", "-c", "echo checkpoint"], remote: false) { repo, _ in
            seed(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.model.lastMessage == "checkpoint")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "checkpoint")
    }

    @Test("sharp corner, kept for upstream parity: -c word-splits an argv, shell operators are ordinary words")
    func messageCommandIsNotAShell() {
        let r = twins(flags: ["-m", "fallback", "-c", "echo x && echo y"], remote: false) { repo, _ in
            seed(repo)
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.model.lastMessage == "x && echo y", "green today: one echo of plain words, not two commands")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "x && echo y")
    }

    // The model's getopts declares C: and throws its argument away, so upstream
    // needs a throwaway token after -C; our -C takes none, and the extra
    // bareword is not the final target, so both sides ignore "unused".
    @Test("-c with -C: both pipe the changed file names into the command")
    func messageCommandReceivesDiffNames() {
        let r = twins(flags: ["-m", "fallback", "-c", "cat", "-C", "unused"], remote: false) { repo, _ in
            seed(repo)
            repo.write("seed.txt", "changed\n")
        }
        #expect(r.model.lastMessage == "seed.txt", "green today: the model pipes the diff names")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "seed.txt")
    }

    @Test("-C piping a huge name list to a command that never reads stdin: no crash, same result")
    func hugePipeNonDrainingCommand() {
        let r = twins(flags: ["-m", "fallback", "-c", "echo done", "-C", "unused"], remote: false) { repo, _ in
            manyChangedFiles(repo)
        }
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "done", "the command's own stdout is the message")
    }

    @Test("-C piping a huge name list to cat: no deadlock, same result")
    func hugePipeCat() {
        let r = twins(flags: ["-m", "fallback", "-c", "cat", "-C", "unused"], remote: false) { repo, _ in
            manyChangedFiles(repo)
        }
        #expect(r.ours == r.model)
    }

    @Test("-c output with a NUL byte: both drop the NUL and commit the rest")
    func nulByteInMessage() {
        let r = twins(flags: ["-m", "fallback", "-c", "./nul.sh"], remote: false) { repo, _ in
            seed(repo)
            makeExecutable(repo, "nul.sh", "#!/bin/sh\nprintf 'before\\0after'\n")
            repo.write("notes.txt", "hello\n")
        }
        #expect(r.model.lastMessage == "beforeafter", "green today: bash's substitution drops the NUL")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "beforeafter")
    }

    @Test("-C under core.autocrlf: git's stderr warning does not enter the message")
    func crlfWarningStaysOutOfMessage() {
        let r = twins(flags: ["-m", "fallback", "-c", "cat", "-C", "unused"], remote: false) { repo, _ in
            crlfWarningSetup(repo)
        }
        #expect(r.model.lastMessage == "f.txt", "green today: only stdout feeds the command")
        #expect(r.ours == r.model)
        #expect(r.ours.lastMessage == "f.txt")
        #expect(!r.ours.lastMessage.contains("warning"), "the LF/CRLF warning must not become the message")
    }

    @Test("push runs even when this cycle's commit aborts: an earlier stranded commit still reaches the remote")
    func pushAfterAbortedCommit() {
        let r = twins(flags: ["-m", "fallback", "-c", "true", "-r", "origin", "-b", "main"], remote: true) { repo, _ in
            seedAndPush(repo)
            repo.commit("stranded.txt", "local only\n", message: "stranded")
            repo.write("seed.txt", "changed\n")
        }
        #expect(r.ours == r.model)
        #expect(r.ours.remoteCommitCount == 2, "seed plus the stranded commit both on the remote")
        #expect(r.ours.remoteLastMessage == "stranded")
    }

    @Test("-C with only new files: nothing on stdin, the empty message aborts the commit on both")
    func emptyPipeAbortsCommit() {
        let r = twins(flags: ["-m", "fallback", "-c", "cat", "-C", "unused"], remote: false) { repo, _ in
            seed(repo)
            repo.write("new.txt", "hello\n")
        }
        #expect(r.model.commitCount == 1, "green today: the model's empty message aborts its commit")
        #expect(r.model.pendingChanges == 1, "green today: the model leaves the change staged")
        #expect(r.ours == r.model)
        #expect(r.ours.commitCount == 1, "fire-and-forget: no commit, no crash, on either side")
        #expect(r.ours.pendingChanges == 1, "the change stays staged for a later cycle")
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
            repo.conflictedMerge()
        }
        #expect(r.ours == r.model)
        #expect(r.ours.midMerge, "the merge is left untouched on both sides")
    }

    @Test("without -M: both commit the conflicted merge")
    func mergeCommitted() {
        let r = twins(flags: ["-m", "cycle"], remote: false) { repo, _ in
            repo.conflictedMerge()
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

    @Test("-l: both embed the coloured diff as the commit message, byte for byte")
    func listChangesColoured() {
        let r = twins(flags: ["-m", "cycle", "-l", "10"], remote: false) { repo, _ in
            seed(repo)
            repo.write("seed.txt", "changed\n")
        }
        #expect(r.model.lastMessageBody.contains("seed.txt:1: "),
                "model sanity: hunk lines become path:line:, got \(r.model.lastMessageBody)")
        #expect(r.model.lastMessageBody.contains("\u{1B}["),
                "model sanity: -l embeds raw ANSI colour codes")
        // Byte-exact comparison is fair here: both worlds run the same git, so
        // the colour bytes (which vary across git versions/config) cancel out.
        #expect(r.ours == r.model)
    }

    @Test("-l with CRLF content: both embed identical bytes, the trailing CR kept")
    func listChangesCRLF() {
        let r = twins(flags: ["-m", "cycle", "-l", "10"], remote: false) { repo, _ in
            repo.write("crlf.txt", "alpha\r\nbeta\r\ngamma\r\n")
            repo.git("add", "-A")
            repo.git("commit", "-q", "-m", "seed")
            repo.write("crlf.txt", "alpha\r\nCHANGED\r\ngamma\r\n")
        }
        #expect(r.model.lastMessageBody.contains("crlf.txt:2: "),
                "model sanity: CRLF lines still carry path:line:, got \(r.model.lastMessageBody)")
        #expect(r.model.lastMessageBody.contains("\r"),
                "model sanity: the diff-line content keeps the file's trailing CR")
        #expect(r.ours == r.model)
    }

    @Test("-L, pinned bug-for-bug: both degrade every message to the status summary")
    func listChangesPlainDegraded() {
        // Upstream -L blanks the colour variable, so the script runs
        // `git diff -U0 ""`; git 2.39+ rejects the empty argument, the diff
        // message comes back empty, and every -L commit falls through to
        // "New files added: <git status -s>" (status taken before git add).
        // NOT the documented diff-as-message behaviour: strict parity keeps
        // the bug, and the model assertion pins the real script's output
        // today, independent of our engine.
        let r = twins(flags: ["-m", "cycle", "-L", "10"], remote: false) { repo, _ in
            seed(repo)
            repo.write("seed.txt", "changed\n")
        }
        #expect(r.model.lastMessageBody == "New files added:  M seed.txt",
                "upstream degradation changed? got: \(r.model.lastMessageBody)")
        #expect(r.ours == r.model)
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
