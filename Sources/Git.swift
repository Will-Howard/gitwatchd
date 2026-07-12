import Foundation

// Thin wrapper around /usr/bin/git. No external deps — this plus FSEvents is the
// whole engine. Behavior mirrors gitwatch: debounced auto-commit, optional push
// to a remote/branch, optional pull --rebase, optional merge-commit guard.
enum Git {
    @discardableResult
    static func run(_ args: [String], in dir: String, gitDir: String? = nil) -> (code: Int32, out: String) {
        let p = Process()
        // Use the same git + environment the user's terminal would (see GitRuntime),
        // so push auth works even when the daemon is launched at login.
        p.executableURL = URL(fileURLWithPath: GitRuntime.resolved.gitPath)
        p.environment = GitRuntime.resolved.env
        var full = args
        if let gitDir { full = ["--git-dir", gitDir] + args }
        p.arguments = full
        p.currentDirectoryURL = URL(fileURLWithPath: dir)
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        do { try p.run() } catch { return (-1, "\(error)") }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        let out = String(data: data, encoding: .utf8) ?? ""
        return (p.terminationStatus, out.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    static func isRepo(_ dir: String) -> Bool {
        run(["rev-parse", "--is-inside-work-tree"], in: dir).out == "true"
    }

    static func currentBranch(_ dir: String) -> String {
        let r = run(["rev-parse", "--abbrev-ref", "HEAD"], in: dir)
        return r.code == 0 ? r.out : "?"
    }

    static func pendingCount(_ dir: String) -> Int {
        let out = run(["status", "--porcelain"], in: dir).out
        return out.isEmpty ? 0 : out.split(separator: "\n").count
    }

    static func lastCommitSummary(_ dir: String) -> String {
        let r = run(["log", "-1", "--pretty=%cr · %s"], in: dir)
        return r.code == 0 ? r.out : "no commits yet"
    }

    private static func hasMergeInProgress(_ dir: String) -> Bool {
        let top = run(["rev-parse", "--git-dir"], in: dir)
        guard top.code == 0 else { return false }
        let gitDirPath = (top.out as NSString).isAbsolutePath
            ? top.out : (dir as NSString).appendingPathComponent(top.out)
        return FileManager.default.fileExists(atPath: (gitDirPath as NSString).appendingPathComponent("MERGE_HEAD"))
    }

    /// Stage all, commit (honoring -m/-d/-M), then optionally pull --rebase (-R) and
    /// push (-r/-b). Returns a short human status.
    @discardableResult
    static func autoCommit(_ spec: RepoSpec) -> String {
        let dir = spec.path
        if spec.noMergeCommit && hasMergeInProgress(dir) { return "merge in progress — skipped" }
        guard pendingCount(dir) > 0 else { return "clean" }

        run(["add", "-A"], in: dir, gitDir: spec.gitDir)
        let msg = spec.message.replacingOccurrences(
            of: "%d", with: RepoSpecParser.formattedDate(spec.dateFormat))
        let commit = run(["commit", "-m", msg], in: dir, gitDir: spec.gitDir)
        guard commit.code == 0 else { return "commit failed" }

        guard let remote = spec.remote else { return "committed" }
        let branch = spec.branch ?? currentBranch(dir)
        if spec.rebase {
            let pull = run(["pull", "--rebase", remote, branch], in: dir, gitDir: spec.gitDir)
            if pull.code != 0 {
                run(["rebase", "--abort"], in: dir, gitDir: spec.gitDir) // non-destructive
                return "committed (rebase conflict — push paused)"
            }
        }
        let push = run(["push", remote, branch], in: dir, gitDir: spec.gitDir)
        return push.code == 0 ? "committed + pushed" : "committed (push failed)"
    }
}
