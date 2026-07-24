import Foundation
import Testing

/// The commit subject read without the whitespace-trimming the test helpers
/// apply, so a test can assert edge whitespace that would otherwise be erased.
private func rawCommitSubject(_ repo: TestRepo) -> String {
    let p = Process()
    p.executableURL = URL(fileURLWithPath: GitRuntime.resolved.gitPath)
    p.arguments = ["log", "-1", "--pretty=%s"]
    p.currentDirectoryURL = URL(fileURLWithPath: repo.path)
    let out = Pipe()
    p.standardOutput = out
    try! p.run()
    let d = out.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    var s = String(decoding: d, as: UTF8.self)
    while s.hasSuffix("\n") { s.removeLast() }
    return s
}

// Engine tests drive Git.autoCommit / Git.push against real throwaway git
// repos, asserting both the reported outcome and the repo state left behind
// (the gitwatch parity contract: same commands, same end state).

@Suite("One auto-commit cycle (gitwatch semantics)")
struct AutoCommitCycle {

    @Test("a clean repo does nothing and reports clean")
    func cleanRepo() {
        let repo = TestRepo()
        repo.write("seed.txt", "v1")
        Git.autoCommit(repo.spec())          // absorb the initial commit
        #expect(Git.autoCommit(repo.spec()) == .clean)
        #expect(repo.commitCount == 1, "no extra commit should appear")
    }

    @Test("changes commit locally when no remote is configured")
    func commitWithoutRemote() {
        let repo = TestRepo()
        repo.write("notes.txt", "hello")
        #expect(Git.autoCommit(repo.spec()) == .committed)
        #expect(repo.commitCount == 1)
        #expect(repo.lastMessage.hasPrefix("gitwatchd auto-commit"),
                "default message expected, got: \(repo.lastMessage)")
    }

    @Test("-m sets the commit message and %d expands to a date")
    func customMessage() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec("-m", "saved on %d", "-d", "+%Y"))
        #expect(repo.lastMessage.hasPrefix("saved on 2"),   // "saved on 2026"
                "got: \(repo.lastMessage)")
    }

    @Test("only the first %d is expanded, as upstream does")
    func firstDateTokenOnly() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec("-m", "%d then %d", "-d", "+%Y"))
        #expect(repo.lastMessage.hasSuffix(" then %d"), "got: \(repo.lastMessage)")
    }

    @Test("sharp corner, kept for upstream parity: -d goes to date(1) raw, so a format without a leading + splices an empty date")
    func rawDateFormatNeedsLeadingPlus() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec("-m", "at %d", "-d", "%Y"))
        #expect(repo.lastMessage == "at", "the empty splice leaves 'at '; git's message cleanup trims it")
    }

    @Test("with -r origin the commit is pushed to the remote")
    func pushToRemote() {
        let repo = TestRepo()
        let origin = repo.addOrigin()
        repo.write("a.txt", "1")
        #expect(Git.autoCommit(repo.spec("-r", "origin", "-b", "main")) == .pushed)
        #expect(origin.commitCount == 1, "the remote should have the commit")
    }
}

// -c runs a command and uses its stdout as the commit message; -C additionally
// pipes `git diff --name-only` into its stdin. Upstream executes the command
// with bare word-splitting, not a shell, and never checks whether it failed.
@Suite("Commit message command (-c / -C)")
struct CommitMessageCommand {

    @Test("-c: the command's stdout becomes the commit message, overriding -m and -d")
    func stdoutBecomesMessage() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        #expect(Git.autoCommit(repo.spec("-m", "fallback %d", "-d", "+%Y",
                                         "-c", "echo checkpoint")) == .committed)
        #expect(repo.lastMessage == "checkpoint", "got: \(repo.lastMessage)")
    }

    @Test("the command is an argv, not a shell line: operators ride along as plain words")
    func noShellOperators() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec("-c", "echo x && echo y"))
        #expect(repo.lastMessage == "x && echo y", "got: \(repo.lastMessage)")
    }

    @Test("the command runs in the repo's work dir")
    func runsInWorkDir() {
        let repo = TestRepo()
        repo.write("msg-src.txt", "from the work dir\n")
        Git.autoCommit(repo.spec("-c", "cat msg-src.txt"))
        #expect(repo.lastMessage == "from the work dir", "got: \(repo.lastMessage)")
    }

    @Test("-C: the changed file names arrive on the command's stdin")
    func pipesChangedFiles() {
        let repo = TestRepo()
        repo.write("tracked.txt", "v1\n")
        Git.autoCommit(repo.spec())
        repo.write("tracked.txt", "v2\n")
        Git.autoCommit(repo.spec("-c", "cat", "-C"))
        #expect(repo.lastMessage == "tracked.txt", "got: \(repo.lastMessage)")
    }

    @Test("-C feeds the name list raw: a leading-space filename is not edge-trimmed")
    func pipedNamesKeepLeadingSpace() {
        let repo = TestRepo()
        repo.write(" lead.txt", "v1\n")
        repo.git("add", "-A"); repo.git("commit", "-q", "-m", "seed")
        repo.write(" lead.txt", "v2\n")
        #expect(Git.autoCommit(repo.spec("-c", "cat", "-C")) == .committed)
        // The helpers trim; read the subject raw to prove the leading space survived.
        #expect(rawCommitSubject(repo) == " lead.txt",
                "got: \(rawCommitSubject(repo).debugDescription)")
    }

    @Test("-C without -c changes nothing")
    func pipeAloneIsInert() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        #expect(Git.autoCommit(repo.spec("-C", "-m", "plain")) == .committed)
        #expect(repo.lastMessage == "plain")
    }

    @Test("an empty -c falls back to the standard message")
    func emptyCommandFallsBack() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec("-c", "", "-m", "fallback"))
        #expect(repo.lastMessage == "fallback")
    }

    @Test("a failing command is fire-and-forget: its stdout is still the message")
    func failingCommandStillUsed() {
        let repo = TestRepo()
        repo.write("gen.sh", "#!/bin/sh\necho generated then failed\nexit 7\n")
        try! FileManager.default.setAttributes([.posixPermissions: 0o755],
                                               ofItemAtPath: repo.path + "/gen.sh")
        repo.write("a.txt", "1")
        #expect(Git.autoCommit(repo.spec("-c", "./gen.sh")) == .committed)
        #expect(repo.lastMessage == "generated then failed", "got: \(repo.lastMessage)")
    }

    @Test("-c output that is not valid UTF-8 still commits: lossy decode, not an empty-message abort")
    func nonUtf8OutputStillCommits() {
        let repo = TestRepo()
        repo.write("bad.sh", "#!/bin/sh\nprintf '\\377'\n")   // 0xFF: not valid UTF-8
        try! FileManager.default.setAttributes([.posixPermissions: 0o755],
                                               ofItemAtPath: repo.path + "/bad.sh")
        repo.write("a.txt", "1")
        #expect(Git.autoCommit(repo.spec("-c", "./bad.sh")) == .committed)
        #expect(repo.commitCount == 1, "the commit happened rather than aborting on an empty message")
        #expect(!repo.lastMessage.isEmpty, "lossy decode leaves U+FFFD, so the message is non-empty")
    }
}

@Suite("Push failures")
struct PushFailures {

    @Test("an unreachable remote reports pushFailed but the commit survives locally")
    func unreachableRemote() {
        let repo = TestRepo()
        repo.addOrigin(TestDirs.root + "/not-a-remote.git")
        repo.write("a.txt", "1")
        let outcome = Git.autoCommit(repo.spec("-r", "origin", "-b", "main"))
        guard case .pushFailed(let detail) = outcome else {
            Issue.record("expected pushFailed, got \(outcome)")
            return
        }
        #expect(!detail.isEmpty, "the git error is captured for the menu")
        #expect(repo.commitCount == 1, "gitwatch parity: commit stays, only the push failed")
    }

    @Test("Git.push retries the stranded commit once the remote is reachable again")
    func retryAfterOutage() {
        let repo = TestRepo()
        let origin = BareRemote()
        repo.addOrigin(TestDirs.root + "/offline.git")   // remote "down"
        repo.write("a.txt", "1")
        guard case .pushFailed = Git.autoCommit(repo.spec("-r", "origin", "-b", "main")) else {
            Issue.record("setup: expected the first push to fail")
            return
        }
        repo.setOriginURL(origin.path)                    // remote "back up"
        #expect(Git.push(repo.spec("-r", "origin", "-b", "main")) == .pushed)
        #expect(origin.commitCount == 1, "the earlier commit reached the remote")
    }

    @Test("-R against an unreachable remote is a transient push failure, not a conflict")
    func pullFailureWhileOffline() {
        let repo = TestRepo()
        repo.addOrigin(TestDirs.root + "/gone.git")
        repo.write("a.txt", "1")
        let outcome = Git.autoCommit(repo.spec("-r", "origin", "-b", "main", "-R"))
        guard case .pushFailed = outcome else {
            Issue.record("expected pushFailed, got \(outcome)")
            return
        }
        #expect(!repo.midRebase, "no rebase was ever started, so the retry loop may heal this")
    }

    @Test("this cycle's commit fails but an earlier commit still pushes; the failure is still surfaced")
    func commitFailsButPushProceeds() {
        let repo = TestRepo()
        let origin = repo.addOrigin()
        repo.write("seed.txt", "v1\n")
        Git.autoCommit(repo.spec("-r", "origin", "-b", "main"))       // seed committed and pushed
        repo.commit("stranded.txt", "local only\n", message: "stranded")  // local, never pushed
        repo.write("seed.txt", "changed\n")                          // dirty for this cycle
        let outcome = Git.autoCommit(repo.spec("-c", "true", "-r", "origin", "-b", "main"))
        guard case .commitFailed = outcome else {
            Issue.record("expected commitFailed, got \(outcome)")
            return
        }
        #expect(origin.commitCount == 2, "seed + stranded both reached the remote despite the aborted commit")
        #expect(origin.lastMessage == "stranded")
    }

    @Test("retrying with nothing left to push still reports pushed")
    func idempotentRetry() {
        let repo = TestRepo()
        repo.addOrigin()
        repo.write("a.txt", "1")
        let spec = repo.spec("-r", "origin", "-b", "main")
        Git.autoCommit(spec)
        #expect(Git.push(spec) == .pushed)   // "Everything up-to-date"
    }
}

@Suite("Push command forms (-b)")
struct PushForms {

    @Test("without -b, a plain `git push <remote>` lets git's config decide")
    func withoutBranch() {
        let spec = RepoSpecParser.parse(["-r", "origin", "/tmp/x"]).spec!
        #expect(Git.pushArgs(remote: "origin", spec: spec) == ["push", "origin"])
    }

    @Test("with -b, the current branch is pushed to it as <current>:<branch>")
    func withBranch() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec())
        repo.git("checkout", "-q", "-b", "feature")
        #expect(Git.pushArgs(remote: "origin", spec: repo.spec("-r", "origin", "-b", "main"))
                == ["push", "origin", "feature:main"])
    }

    @Test("from a detached HEAD, -b pushes the branch by name")
    func detachedHead() {
        let repo = TestRepo()
        repo.write("a.txt", "1")
        Git.autoCommit(repo.spec())
        repo.git("checkout", "-q", "--detach")
        #expect(Git.pushArgs(remote: "origin", spec: repo.spec("-r", "origin", "-b", "main"))
                == ["push", "origin", "main"])
    }

    @Test("end to end: a commit made on feature lands on the remote's main")
    func refspecEndToEnd() {
        let repo = TestRepo()
        let origin = repo.addOrigin()
        repo.write("a.txt", "base\n")
        Git.autoCommit(repo.spec("-r", "origin", "-b", "main"))
        repo.git("checkout", "-q", "-b", "feature")
        repo.write("b.txt", "on feature\n")
        #expect(Git.autoCommit(repo.spec("-r", "origin", "-b", "main", "-m", "from feature"))
                == .pushed)
        #expect(origin.commitCount == 2, "the remote's main received the feature commit")
        #expect(origin.lastMessage == "from feature")
    }
}

@Suite("Two-way sync (-R)")
struct TwoWaySync {

    @Test("commits made on another machine are pulled in and ours lands on top")
    func rebaseThenPush() {
        let repo = TestRepo()
        let origin = repo.addOrigin()
        repo.write("ours.txt", "base\n")
        Git.autoCommit(repo.spec("-r", "origin", "-b", "main"))
        repo.trackOrigin()

        let colleague = TestRepo(cloneOf: origin)
        colleague.write("theirs.txt", "from the other machine\n")
        Git.autoCommit(colleague.spec("-r", "origin", "-b", "main", "-m", "made elsewhere"))

        repo.write("ours.txt", "updated here\n")
        let outcome = Git.autoCommit(repo.spec("-r", "origin", "-b", "main", "-R", "-m", "made here"))
        #expect(outcome == .pushed)
        #expect(origin.commitCount == 3, "base, theirs, ours: one linear history")
        #expect(origin.lastMessage == "made here", "our commit was rebased on top")
        #expect(repo.git("log", "--pretty=%s").contains("made elsewhere"),
                "their commit is now part of our local history")
    }
}

@Suite("Merge guard (-M)")
struct MergeGuard {

    @Test("-M skips the cycle while a merge is in progress")
    func skipsMidMerge() {
        let repo = TestRepo.withConflictedMerge()
        #expect(repo.midMerge, "setup: repo should be mid-merge")
        #expect(Git.autoCommit(repo.spec("-M")) == .skippedMerge)
        #expect(repo.midMerge, "the merge is left exactly as it was")
    }

    @Test("without -M a mid-merge cycle commits the conflicted merge")
    func commitsMidMergeWithoutFlag() {
        let repo = TestRepo.withConflictedMerge()
        #expect(Git.autoCommit(repo.spec()) == .committed)
        #expect(!repo.midMerge, "the commit concluded the merge, as gitwatch would")
    }
}

@Suite("Subdirectory and file targets")
struct Targets {

    @Test("watching a subdirectory commits only changes under it")
    func subdirectoryTarget() {
        let repo = TestRepo()
        repo.write("sub/inner.txt", "v1\n")
        Git.autoCommit(repo.spec())
        repo.write("sub/inner.txt", "v2\n")
        repo.write("outer.txt", "left alone\n")
        let spec = RepoSpecParser.parse([repo.path + "/sub"]).spec!
        #expect(Git.autoCommit(spec) == .committed)
        #expect(Git.pendingCount(repo.path) == 1, "outer.txt stays uncommitted")
        #expect(repo.git("show", "--name-only", "--pretty=") == "sub/inner.txt")
    }

    @Test("a file target commits only that file")
    func fileTarget() {
        let repo = TestRepo()
        repo.write("a.txt", "v1\n")
        Git.autoCommit(repo.spec())
        repo.write("a.txt", "v2\n")
        repo.write("b.txt", "left alone\n")
        let spec = RepoSpecParser.parse([repo.path + "/a.txt"]).spec!
        #expect(Git.autoCommit(spec) == .committed)
        #expect(Git.pendingCount(repo.path) == 1, "b.txt stays uncommitted")
        #expect(repo.git("show", "--name-only", "--pretty=") == "a.txt")
    }

    @Test("changes only outside the watched subtree report clean, not a failure")
    func outsideChangesOnly() {
        let repo = TestRepo()
        repo.write("sub/inner.txt", "v1\n")
        Git.autoCommit(repo.spec())
        repo.write("outer.txt", "elsewhere\n")
        let before = repo.commitCount
        #expect(Git.autoCommit(RepoSpecParser.parse([repo.path + "/sub"]).spec!) == .clean)
        #expect(repo.commitCount == before)
    }
}

@Suite("Detached git dir (-g)")
struct DetachedGitDir {

    @Test("a worktree whose .git lives elsewhere still commits and validates")
    func separateGitDir() {
        let repo = TestRepo()
        repo.write("a.txt", "base\n")
        Git.autoCommit(repo.spec())
        let gitDir = TestDirs.fresh("elsewhere.git")
        try! FileManager.default.moveItem(atPath: repo.path + "/.git", toPath: gitDir)

        let spec = repo.spec("-g", gitDir)
        #expect(Git.isRepo(repo.path, gitDir: gitDir),
                "the daemon/CLI validation path must accept a -g repo")
        repo.write("a.txt", "changed\n")
        #expect(Git.autoCommit(spec) == .committed)
        #expect(Git.run(["rev-list", "--count", "HEAD"], in: repo.path, gitDir: gitDir).out == "2")
        #expect(Git.autoCommit(spec) == .clean, "and the change was fully committed")
    }
}

@Suite("Commit and rebase failures")
struct CommitAndRebaseFailures {

    @Test("a failing pre-commit hook reports commitFailed with the hook's complaint")
    func failingHook() {
        let repo = TestRepo()
        repo.installFailingPreCommitHook(printing: "lint says no")
        repo.write("a.txt", "1")
        let outcome = Git.autoCommit(repo.spec())
        guard case .commitFailed(let detail) = outcome else {
            Issue.record("expected commitFailed, got \(outcome)")
            return
        }
        #expect(detail.contains("lint says no"), "hook output surfaces: got \(detail)")
    }

    @Test("-R reports rebaseConflict when the remote diverged incompatibly")
    func rebaseConflict() {
        let repo = TestRepo()
        let origin = repo.addOrigin()
        repo.write("shared.txt", "original\n")
        Git.autoCommit(repo.spec("-r", "origin", "-b", "main"))
        repo.trackOrigin()

        let colleague = TestRepo(cloneOf: origin)         // someone else pushes first
        colleague.write("shared.txt", "colleague's version\n")
        Git.autoCommit(colleague.spec("-r", "origin", "-b", "main"))

        repo.write("shared.txt", "our conflicting version\n")
        let outcome = Git.autoCommit(repo.spec("-r", "origin", "-b", "main", "-R"))
        guard case .rebaseConflict = outcome else {
            Issue.record("expected rebaseConflict, got \(outcome)")
            return
        }
        // gitwatch parity: no abort. The conflicted rebase is left in progress
        // for the user to resolve; we only make it visible in the menu.
        #expect(repo.midRebase, "the conflicted rebase is left in progress")
    }
}
