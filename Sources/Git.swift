import Foundation

// Thin wrapper around git. No external deps: this plus FSEvents is the whole
// engine. Behavior mirrors gitwatch: debounced auto-commit, optional push to a
// remote/branch, optional pull --rebase, optional merge-commit guard.

/// What one auto-commit cycle (or push retry) accomplished. The git commands and
/// their order are gitwatch's; this only reports the result in a form the menu
/// and tests can inspect, instead of a bare status string.
enum CommitOutcome: Equatable {
    case clean                            // nothing to commit
    case skippedMerge                     // -M: merge in progress, cycle skipped
    case committed                        // committed; no remote configured
    case pushed                           // committed and pushed
    case commitFailed(detail: String)
    case rebaseConflict(detail: String)   // -R: pull --rebase hit a conflict
    case pushFailed(detail: String)

    /// Menu-row label when this outcome is an error state, nil when healthy.
    var errorLabel: String? {
        switch self {
        case .pushFailed:     return "push failing"
        case .rebaseConflict: return "rebase conflict"
        case .commitFailed:   return "commit failing"
        default:              return nil
        }
    }

    /// The captured git error line; nil for non-error outcomes.
    var detail: String? {
        switch self {
        case .commitFailed(let d), .rebaseConflict(let d), .pushFailed(let d): return d
        default: return nil
        }
    }
}

enum Git {
    @discardableResult
    static func run(_ args: [String], in dir: String, gitDir: String? = nil) -> (code: Int32, out: String) {
        let p = Process()
        // Use the same git + environment the user's terminal would (see GitRuntime),
        // so push auth works even when the daemon is launched at login.
        p.executableURL = URL(fileURLWithPath: GitRuntime.resolved.gitPath)
        p.environment = GitRuntime.resolved.env
        var full = args
        // -g repos get upstream's exact override on every call:
        // `git --work-tree $TARGETDIR --git-dir $GIT_DIR <cmd>`.
        if let gitDir { full = ["--work-tree", dir, "--git-dir", gitDir] + args }
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

    static func isRepo(_ dir: String, gitDir: String? = nil) -> Bool {
        run(["rev-parse", "--is-inside-work-tree"], in: dir, gitDir: gitDir).out == "true"
    }

    static func currentBranch(_ dir: String, gitDir: String? = nil) -> String {
        let r = run(["rev-parse", "--abbrev-ref", "HEAD"], in: dir, gitDir: gitDir)
        return r.code == 0 ? r.out : "?"
    }

    static func pendingCount(_ dir: String, gitDir: String? = nil) -> Int {
        let out = run(["status", "--porcelain"], in: dir, gitDir: gitDir).out
        return out.isEmpty ? 0 : out.split(separator: "\n").count
    }

    static func lastCommitSummary(_ dir: String, gitDir: String? = nil) -> String {
        let r = run(["log", "-1", "--pretty=%cr · %s"], in: dir, gitDir: gitDir)
        return r.code == 0 ? r.out : "no commits yet"
    }

    /// True if a state file/dir exists inside the repo's resolved .git dir.
    private static func gitStateExists(_ names: [String], in dir: String, gitDir: String?) -> Bool {
        let top = run(["rev-parse", "--git-dir"], in: dir, gitDir: gitDir)
        guard top.code == 0 else { return false }
        let gitDirPath = (top.out as NSString).isAbsolutePath
            ? top.out : (dir as NSString).appendingPathComponent(top.out)
        return names.contains {
            FileManager.default.fileExists(atPath: (gitDirPath as NSString).appendingPathComponent($0))
        }
    }

    /// gitwatch's is_merging: MERGE_HEAD only (a rebase does not count, upstream).
    private static func hasMergeInProgress(_ dir: String, gitDir: String?) -> Bool {
        gitStateExists(["MERGE_HEAD"], in: dir, gitDir: gitDir)
    }

    private static func hasRebaseInProgress(_ dir: String, gitDir: String?) -> Bool {
        gitStateExists(["rebase-merge", "rebase-apply"], in: dir, gitDir: gitDir)
    }

    /// Stage all, commit (honoring -m/-d/-M), then optionally pull --rebase (-R)
    /// and push (-r/-b). One gitwatch cycle.
    @discardableResult
    static func autoCommit(_ spec: RepoSpec) -> CommitOutcome {
        let dir = spec.workDir
        if spec.noMergeCommit && hasMergeInProgress(dir, gitDir: spec.gitDir) { return .skippedMerge }
        guard pendingCount(dir, gitDir: spec.gitDir) > 0 else { return .clean }

        // Upstream's GIT_ADD_ARGS: "--all ." scoped to the target directory,
        // or just the file for a file target.
        let addTarget = spec.isFileTarget ? spec.path : "."
        run(["add", "--all", addTarget], in: dir, gitDir: spec.gitDir)
        let msg = spec.message.replacingOccurrences(
            of: "%d", with: RepoSpecParser.formattedDate(spec.dateFormat))
        let commit = run(["commit", "-m", msg], in: dir, gitDir: spec.gitDir)
        guard commit.code == 0 else {
            // Repo-wide changes outside the watched subtree stage nothing.
            if commit.out.contains("nothing to commit")
                || commit.out.contains("nothing added to commit")
                || commit.out.contains("no changes added to commit") {
                return .clean
            }
            return .commitFailed(detail: errorSummary(commit.out))
        }
        guard spec.remote != nil else { return .committed }
        return push(spec)
    }

    /// The push stage of a cycle, exactly as gitwatch runs it. With -R, first
    /// `git pull --rebase <remote>` (no branch argument, exit code ignored, no
    /// abort: a conflict leaves the rebase in progress for the user to resolve,
    /// and -M is the only guard). Then the push, which upstream runs regardless
    /// of how the pull went. Split out from autoCommit so a failed push can be
    /// retried without re-running the commit stage.
    ///
    /// Everything below the git calls is reporting only: gitwatch ignores both
    /// results; we classify them for the menu.
    @discardableResult
    static func push(_ spec: RepoSpec) -> CommitOutcome {
        guard let remote = spec.remote else { return .committed }
        let dir = spec.workDir
        var pullFailure: String? = nil
        if spec.rebase {
            let pull = run(["pull", "--rebase", remote], in: dir, gitDir: spec.gitDir)
            if pull.code != 0 { pullFailure = errorSummary(pull.out) }
        }
        let p = run(pushArgs(remote: remote, spec: spec), in: dir, gitDir: spec.gitDir)

        if let pullFailure {
            // A conflict leaves a rebase in progress and needs the user;
            // anything else (offline, auth) is transient and worth retrying.
            return hasRebaseInProgress(dir, gitDir: spec.gitDir)
                ? .rebaseConflict(detail: pullFailure)
                : .pushFailed(detail: pullFailure)
        }
        return p.code == 0 ? .pushed : .pushFailed(detail: errorSummary(p.out))
    }

    /// gitwatch's push command: without -b, a bare `push <remote>` (git's
    /// push.default decides); with -b, push `<current>:<branch>`, or just
    /// `<branch>` from a detached HEAD. Internal so tests can pin the forms.
    static func pushArgs(remote: String, spec: RepoSpec) -> [String] {
        guard let branch = spec.branch else { return ["push", remote] }
        let head = run(["symbolic-ref", "HEAD"], in: spec.workDir, gitDir: spec.gitDir)
        guard head.code == 0 else { return ["push", remote, branch] }
        let current = head.out.replacingOccurrences(of: "refs/heads/", with: "")
        return ["push", remote, "\(current):\(branch)"]
    }

    /// The most informative line of a failed command's output: the first
    /// error/fatal/rejection line if there is one, else the last non-empty line.
    static func errorSummary(_ out: String) -> String {
        let lines = out.split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        if let hit = lines.first(where: { l in
            l.hasPrefix("error:") || l.hasPrefix("fatal:")
                || l.hasPrefix("! [rejected]") || l.hasPrefix("! [remote rejected]")
        }) { return hit }
        return lines.last ?? "unknown git error"
    }
}
