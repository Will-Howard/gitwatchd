import Foundation

// Thin wrapper around git. No external deps: this plus FSEvents is the whole
// engine. Behavior mirrors gitwatch: debounced auto-commit, optional push to a
// remote/branch, optional pull --rebase, optional merge-commit guard.

/// What one auto-commit cycle (or push retry) accomplished. The git commands
/// and their order are gitwatch's; this only reports the result.
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

/// Run a program and capture stdout+stderr, trimmed. `timeout` terminates a
/// hung child.
@discardableResult
func runProcess(_ exe: String, _ args: [String], env: [String: String]? = nil,
                cwd: String? = nil, timeout: TimeInterval? = nil) -> (code: Int32, out: String) {
    let p = Process()
    p.executableURL = URL(fileURLWithPath: exe)
    p.arguments = args
    if let env { p.environment = env }
    if let cwd { p.currentDirectoryURL = URL(fileURLWithPath: cwd) }
    let pipe = Pipe()
    p.standardOutput = pipe
    p.standardError = pipe
    do { try p.run() } catch { return (-1, "\(error)") }
    if let timeout {
        DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
            if p.isRunning { p.terminate() }
        }
    }
    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    let out = String(data: data, encoding: .utf8) ?? ""
    return (p.terminationStatus, out.trimmingCharacters(in: .whitespacesAndNewlines))
}

enum Git {
    /// A broken pipe from a -c/-C message command must never take the daemon
    /// down. Set once, the first time the engine runs.
    private static let sigpipeIgnored: Void = { signal(SIGPIPE, SIG_IGN) }()

    @discardableResult
    static func run(_ args: [String], in dir: String, gitDir: String? = nil) -> (code: Int32, out: String) {
        var full = args
        // -g repos get upstream's exact override on every call:
        // `git --work-tree $TARGETDIR --git-dir $GIT_DIR <cmd>`.
        if let gitDir { full = ["--work-tree", dir, "--git-dir", gitDir] + args }
        // The resolved git + environment match the user's terminal (see
        // GitRuntime), so push auth works when launched at login.
        return runProcess(GitRuntime.resolved.gitPath, full,
                          env: GitRuntime.resolved.env, cwd: dir)
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

    /// Upstream's `$(...)` capture: stdout only (stderr passes through to the
    /// terminal there; discarded here), trailing newlines stripped, leading
    /// whitespace kept (`git status -s` lines start with a space).
    private static func capture(_ args: [String], in dir: String, gitDir: String?) -> String {
        var full = args
        if let gitDir { full = ["--work-tree", dir, "--git-dir", gitDir] + args }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: GitRuntime.resolved.gitPath)
        p.arguments = full
        p.environment = GitRuntime.resolved.env
        p.currentDirectoryURL = URL(fileURLWithPath: dir)
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return "" }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        var out = String(decoding: data, as: UTF8.self)
        while out.hasSuffix("\n") { out.removeLast() }
        return out
    }

    private static let diffMinusFile = try! NSRegularExpression(pattern: "--- (a/)?([^ \\t\\x{1B}]+)")
    private static let diffPlusFile = try! NSRegularExpression(pattern: "\\+\\+\\+ (b/)?([^ \\t\\x{1B}]+)")
    private static let diffHunk = try! NSRegularExpression(
        pattern: "@@ -[0-9]+(,[0-9]+)? \\+([0-9]+)(,[0-9]+)? @@")
    private static let diffContent = try! NSRegularExpression(pattern: "^(\\x{1B}\\[[0-9;]+m)*([ +-])")

    private static func group2(_ re: NSRegularExpression, _ s: String) -> String? {
        guard let m = re.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)),
              let r = Range(m.range(at: 2), in: s) else { return nil }
        return String(s[r])
    }

    /// Port of upstream's diff-lines: rewrites `git diff -U0` hunk lines as
    /// "path:line: content". Its quirks are the contract: unanchored file/hunk
    /// matches, the removal line number never advancing, a bare 150-character
    /// cut that counts colour escapes, and "" for path/line before the first
    /// header.
    private static func diffLines(_ diff: String) -> String {
        var path = ""
        var line = ""
        var previousPath = ""
        var out: [String] = []
        for raw in diff.unicodeScalars.split(separator: "\n" as Unicode.Scalar,
                                             omittingEmptySubsequences: false) {
            var reply = String(String.UnicodeScalarView(raw))
            if let m = group2(diffMinusFile, reply) {
                previousPath = m
                continue
            } else if let m = group2(diffPlusFile, reply) {
                path = m
            } else if let m = group2(diffHunk, reply) {
                line = m
            } else if let sign = group2(diffContent, reply) {
                let scalars = reply.unicodeScalars
                if let cut = scalars.index(scalars.startIndex, offsetBy: 150,
                                           limitedBy: scalars.endIndex), cut != scalars.endIndex {
                    reply = String(scalars[..<cut])
                }
                if path == "/dev/null" {
                    out.append("File \(previousPath) deleted or moved.")
                    continue
                }
                out.append("\(path):\(line): \(reply)")
                if sign != "-" { line = String((Int(line) ?? 0) + 1) }
            }
        }
        return out.joined(separator: "\n")
    }

    /// Upstream's -l/-L message. The colour argument is passed verbatim, empty
    /// string included: -L makes the script run `git diff -U0 ""`, which
    /// modern git rejects, so the diff comes back empty and every -L commit
    /// falls into the New-files branch. Bug-for-bug: same argv, same outcome
    /// on whatever git is installed.
    private static func listChangesMessage(_ spec: RepoSpec) -> String {
        let dir = spec.workDir
        let colorArg = spec.listChangesColor ? "--color=always" : ""
        let msg = diffLines(capture(["diff", "-U0", colorArg], in: dir, gitDir: spec.gitDir))
        let length = spec.listChanges >= 1 && !msg.isEmpty
            ? msg.components(separatedBy: "\n").count : 0
        if length <= spec.listChanges {
            if !msg.isEmpty { return msg }
            return "New files added: " + capture(["status", "-s"], in: dir, gitDir: spec.gitDir)
        }
        return capture(["diff", "--stat"], in: dir, gitDir: spec.gitDir)
            .components(separatedBy: "\n")
            .filter { $0.contains("|") }
            .joined(separator: "\n")
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
        _ = sigpipeIgnored
        let dir = spec.workDir
        if spec.noMergeCommit && hasMergeInProgress(dir, gitDir: spec.gitDir) { return .skippedMerge }
        guard pendingCount(dir, gitDir: spec.gitDir) > 0 else { return .clean }

        // Upstream builds the message before git add (-C's `git diff --name-only`
        // must see the unstaged tree): the first %d splices, then -l/-L, then
        // -c/-C, which is upstream's precedence.
        var msg = spec.message
        if let r = msg.range(of: "%d") {
            msg.replaceSubrange(r, with: RepoSpecParser.formattedDate(spec.dateFormat))
        }
        if spec.listChanges >= 0 { msg = listChangesMessage(spec) }
        if let command = spec.commitCommand, !command.isEmpty {
            msg = commitCommandOutput(command, spec: spec)
        }
        // Upstream's GIT_ADD_ARGS: "--all ." scoped to the target directory,
        // or just the file for a file target.
        let addTarget = spec.isFileTarget ? spec.path : "."
        run(["add", "--all", addTarget], in: dir, gitDir: spec.gitDir)
        let commit = run(["commit", "-m", msg], in: dir, gitDir: spec.gitDir)

        // gitwatch runs the pull (-R) and push unconditionally after the
        // commit, whether or not it succeeded. commitOutcome is reporting only.
        let commitOutcome: CommitOutcome
        if commit.code == 0 {
            commitOutcome = .committed
        } else if commit.out.contains("nothing to commit")
            || commit.out.contains("nothing added to commit")
            || commit.out.contains("no changes added to commit") {
            commitOutcome = .clean
        } else {
            commitOutcome = .commitFailed(detail: errorSummary(commit.out))
        }

        // No remote: never call push(), whose no-remote return would mask a
        // commit failure.
        guard spec.remote != nil else { return commitOutcome }
        let pushed = push(spec)
        if case .commitFailed = commitOutcome { return commitOutcome }
        return pushed
    }

    /// The -c commit command, upstream's bare `$($COMMITCMD)`: word-split into
    /// an argv and exec'd directly (no shell, so operators like && are plain
    /// words), stdout captured with trailing newlines stripped as command
    /// substitution does, exit status ignored. With -C, the unstaged
    /// `git diff --name-only` output (stdout only) is fed to its stdin.
    private static func commitCommandOutput(_ command: String, spec: RepoSpec) -> String {
        let argv = command.split(whereSeparator: { $0 == " " || $0 == "\t" || $0 == "\n" })
            .map(String.init)
        guard !argv.isEmpty else { return "" }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        p.arguments = argv
        p.environment = GitRuntime.resolved.env
        p.currentDirectoryURL = URL(fileURLWithPath: spec.workDir)
        let out = Pipe()
        p.standardOutput = out
        var feedHandle: FileHandle? = nil
        var feedData = Data()
        if spec.pipeChangedFiles {
            feedData = diffNameOnlyStdout(spec)
            let pipe = Pipe()
            p.standardInput = pipe
            feedHandle = pipe.fileHandleForWriting
        } else {
            p.standardInput = FileHandle.nullDevice
        }
        do { try p.run() } catch { return "" }

        // Feed stdin from a background thread while we drain stdout here, so a
        // command that never reads (or one like cat with a payload past the
        // pipe buffer) can neither deadlock the repo queue nor, with SIGPIPE
        // ignored, crash on a broken pipe.
        let fed = DispatchSemaphore(value: 0)
        if let feedHandle {
            DispatchQueue.global().async {
                let fd = feedHandle.fileDescriptor
                feedData.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
                    guard let base = raw.baseAddress else { return }
                    var off = 0
                    while off < raw.count {
                        let n = write(fd, base + off, raw.count - off)
                        if n <= 0 { break }
                        off += n
                    }
                }
                try? feedHandle.close()
                fed.signal()
            }
        }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        if feedHandle != nil { fed.wait() }

        // bash command substitution drops NUL bytes and strips trailing
        // newlines. Lossy UTF-8 keeps a non-UTF-8 message non-empty (U+FFFD
        // where bytes were invalid) rather than decoding to "" and aborting.
        var bytes = [UInt8](data)
        bytes.removeAll { $0 == 0 }
        var msg = String(decoding: bytes, as: UTF8.self)
        while msg.hasSuffix("\n") { msg.removeLast() }
        return msg
    }

    /// `git diff --name-only` stdout ONLY (no stderr merge), raw and untrimmed,
    /// exactly the bytes upstream feeds via `< <($GIT diff --name-only)`. A
    /// stderr warning (common under core.autocrlf) must not become a filename,
    /// and leading/trailing spaces in names must survive.
    private static func diffNameOnlyStdout(_ spec: RepoSpec) -> Data {
        var full = ["diff", "--name-only"]
        if let gitDir = spec.gitDir { full = ["--work-tree", spec.workDir, "--git-dir", gitDir] + full }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: GitRuntime.resolved.gitPath)
        p.arguments = full
        p.environment = GitRuntime.resolved.env
        p.currentDirectoryURL = URL(fileURLWithPath: spec.workDir)
        let out = Pipe()
        p.standardOutput = out
        do { try p.run() } catch { return Data() }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return data
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
