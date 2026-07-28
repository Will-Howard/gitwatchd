import Foundation
import Testing

// -l/-L message generation. -l embeds the diff: git diff -U0 --color=always
// over the working tree (BEFORE git add), each hunk line rewritten as
// "path:line: content" cut to 150 raw characters, "File <path> deleted or
// moved." for a deletion, "New files added: <git status -s>" for an empty
// diff, and the `git diff --stat | grep '|'` fallback past <lines> lines.
//
// -L is pinned bug-for-bug: `git diff -U0 ""` fails on git 2.39+, the diff
// comes back empty, and every -L commit degrades to the status summary. The
// -L tests below assert that degradation, not the documented diff-as-message.

@Suite("List changes (-l/-L)")
struct ListChanges {

    // MARK: - -l: the genuine diff-as-message pipeline

    @Test("-l embeds ANSI colour codes around the path:line: structure")
    func colouredMessage() {
        let repo = TestRepo()
        repo.write("a.txt", "alpha\n")
        Git.autoCommit(repo.spec())
        repo.write("a.txt", "beta\n")
        #expect(Git.autoCommit(repo.spec("-l", "10", "-m", "unused")) == .committed)
        // Exact bytes vary with git version and colour config, so this asserts
        // structure plus the presence of escapes; the parity suite pins exact
        // bytes differentially against the same git.
        #expect(repo.lastMessageBody.contains("\u{1B}["),
                "-l keeps git's colour codes, got: \(repo.lastMessageBody)")
        #expect(repo.lastMessageBody.contains("a.txt:1: "),
                "hunk lines still carry path:line:, got: \(repo.lastMessageBody)")
    }

    @Test("each embedded diff line is cut at 150 raw characters, escape codes included")
    func truncation() {
        let repo = TestRepo()
        repo.write("long.txt", "short\n")
        Git.autoCommit(repo.spec())
        repo.write("long.txt", String(repeating: "x", count: 200) + "\n")
        Git.autoCommit(repo.spec("-l", "0"))
        #expect(repo.lastMessageBody.contains(String(repeating: "x", count: 100)),
                "the long line is embedded, got: \(repo.lastMessageBody)")
        #expect(!repo.lastMessageBody.contains(String(repeating: "x", count: 150)),
                "cut at 150 chars of raw diff line (colour codes count)")
        for line in repo.lastMessageBody.split(separator: "\n") {
            #expect(line.count <= "long.txt:1: ".count + 150, "over-long line: \(line)")
        }
    }

    @Test("-l 0 means unlimited: a long diff is embedded whole")
    func zeroIsUnlimited() {
        let repo = TestRepo()
        repo.write("n.txt", (1...10).map { "old\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec())
        repo.write("n.txt", (1...10).map { "new\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec("-l", "0"))
        let lines = repo.lastMessageBody.split(separator: "\n")
        #expect(lines.count == 20, "10 removals + 10 additions, got: \(repo.lastMessageBody)")
        #expect(lines.first?.hasPrefix("n.txt:1: ") == true
                    && lines.first?.contains("-old1") == true,
                "removals keep the hunk's start line, got: \(lines.first ?? "")")
        #expect(lines.last?.hasPrefix("n.txt:10: ") == true
                    && lines.last?.contains("new10") == true,
                "additions advance the line number, got: \(lines.last ?? "")")
    }

    @Test("a diff of exactly <lines> lines still embeds; the cap is 'more than'")
    func exactCapEmbeds() {
        let repo = TestRepo()
        repo.write("n.txt", (1...10).map { "old\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec())
        repo.write("n.txt", (1...10).map { "new\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec("-l", "20"))
        #expect(repo.lastMessageBody.split(separator: "\n").count == 20,
                "20 diff lines fit in -l 20, got: \(repo.lastMessageBody)")
        #expect(repo.lastMessageBody.contains("n.txt:1: "))
    }

    @Test("a diff longer than <lines> falls back to the diffstat summary, never coloured")
    func statFallback() {
        let repo = TestRepo()
        repo.write("n.txt", (1...10).map { "old\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec())
        repo.write("n.txt", (1...10).map { "new\($0)" }.joined(separator: "\n") + "\n")
        Git.autoCommit(repo.spec("-l", "5"))
        #expect(repo.lastMessageBody.contains("n.txt |"),
                "diffstat summary expected, got: \(repo.lastMessageBody)")
        #expect(!repo.lastMessageBody.contains("n.txt:1:"), "no embedded diff lines")
        #expect(!repo.lastMessageBody.contains("\u{1B}"),
                "upstream's stat command takes no colour flag")
    }

    @Test("-l with only untracked additions: the message lists them as new files")
    func newFilesOnly() {
        let repo = TestRepo()
        repo.write("seed.txt", "seed\n")
        Git.autoCommit(repo.spec())
        repo.write("fresh.txt", "hello\n")
        Git.autoCommit(repo.spec("-l", "10"))
        #expect(repo.lastMessageBody == "New files added: ?? fresh.txt",
                "status is taken before git add, got: \(repo.lastMessageBody)")
    }

    @Test("-l with a non-UTF-8 byte still embeds the diff, not the New-files fallback")
    func nonUTF8StillEmbeds() {
        let repo = TestRepo()
        let file = repo.path + "/x.txt"
        try! Data("alpha\n".utf8).write(to: URL(fileURLWithPath: file))
        Git.autoCommit(repo.spec())
        // 0x00E9 (Latin-1 e-acute) is not valid UTF-8 on its own.
        try! Data([0x62, 0xE9, 0x74, 0x61, 0x0A]).write(to: URL(fileURLWithPath: file))
        Git.autoCommit(repo.spec("-l", "10"))
        #expect(repo.lastMessageBody.contains("x.txt:1: "),
                "a non-UTF-8 byte must not empty the diff, got: \(repo.lastMessageBody)")
        #expect(!repo.lastMessageBody.hasPrefix("New files added:"),
                "must embed the diff, not degrade, got: \(repo.lastMessageBody)")
        #expect(repo.lastMessageBody.contains("\u{FFFD}"),
                "the invalid byte lands as U+FFFD, got: \(repo.lastMessageBody)")
    }

    @Test("-l reports a deleted file by name, not as a diff")
    func deletedFile() {
        let repo = TestRepo()
        repo.write("doomed.txt", "contents\n")
        repo.write("keep.txt", "kept\n")
        Git.autoCommit(repo.spec())
        try! FileManager.default.removeItem(atPath: repo.path + "/doomed.txt")
        Git.autoCommit(repo.spec("-l", "10"))
        #expect(repo.lastMessageBody == "File doomed.txt deleted or moved.",
                "got: \(repo.lastMessageBody)")
    }

    // MARK: - -L: upstream's broken variant, pinned bug-for-bug

    @Test("-L, pinned bug-for-bug: a plain edit commits as the status summary, not the diff")
    func degradedEditMessage() {
        // Empty colour arg -> `git diff -U0 ""` fails on git 2.39+ -> empty
        // diff message -> the New-files branch. Not the documented behaviour;
        // strict upstream parity keeps the bug.
        let repo = TestRepo()
        repo.write("a.txt", "alpha\n")
        Git.autoCommit(repo.spec())
        repo.write("a.txt", "beta\n")
        #expect(Git.autoCommit(repo.spec("-L", "10", "-m", "unused")) == .committed)
        #expect(repo.lastMessageBody == "New files added:  M a.txt",
                "upstream's degraded -L output, got: \(repo.lastMessageBody)")
    }

    @Test("-L, pinned bug-for-bug: the line cap and diffstat fallback never apply")
    func degradedIgnoresCap() {
        // The empty diff message always satisfies the length gate, so no -L
        // value (small cap or 0 = unlimited) ever embeds a diff or a diffstat.
        for cap in ["5", "0"] {
            let repo = TestRepo()
            repo.write("n.txt", (1...10).map { "old\($0)" }.joined(separator: "\n") + "\n")
            Git.autoCommit(repo.spec())
            repo.write("n.txt", (1...10).map { "new\($0)" }.joined(separator: "\n") + "\n")
            Git.autoCommit(repo.spec("-L", cap))
            #expect(repo.lastMessageBody == "New files added:  M n.txt",
                    "-L \(cap): upstream's degraded output, got: \(repo.lastMessageBody)")
        }
    }

    @Test("-L, pinned bug-for-bug: a deletion also becomes the status summary")
    func degradedDeletion() {
        let repo = TestRepo()
        repo.write("doomed.txt", "contents\n")
        repo.write("keep.txt", "kept\n")
        Git.autoCommit(repo.spec())
        try! FileManager.default.removeItem(atPath: repo.path + "/doomed.txt")
        Git.autoCommit(repo.spec("-L", "10"))
        #expect(repo.lastMessageBody == "New files added:  D doomed.txt",
                "upstream's degraded -L output, got: \(repo.lastMessageBody)")
    }

    @Test("-L with only untracked additions: New files added, where the bug and the intent coincide")
    func degradedNewFiles() {
        let repo = TestRepo()
        repo.write("seed.txt", "seed\n")
        Git.autoCommit(repo.spec())
        repo.write("fresh.txt", "hello\n")
        Git.autoCommit(repo.spec("-L", "10"))
        #expect(repo.lastMessageBody == "New files added: ?? fresh.txt",
                "got: \(repo.lastMessageBody)")
    }
}
