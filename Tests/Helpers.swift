import Foundation

/// All test repos live under one throwaway temp root, removed at exit.
enum TestDirs {
    static let root: String = {
        let p = NSTemporaryDirectory() + "gitwatchd-tests-" + UUID().uuidString
        try! FileManager.default.createDirectory(atPath: p, withIntermediateDirectories: true)
        return p
    }()
    // Tests run in parallel, so handing out directories must be thread-safe.
    private static let lock = NSLock()
    private static var counter = 0
    static func fresh(_ name: String) -> String {
        lock.lock(); defer { lock.unlock() }
        counter += 1
        return root + "/\(counter)-\(name)"
    }
    static func cleanup() { try? FileManager.default.removeItem(atPath: root) }
}

/// A throwaway git repo for the engine to run against: real git, real commits,
/// so tests assert both the reported outcome and the repo state left behind.
final class TestRepo {
    let path: String

    init() {
        path = TestDirs.fresh("repo")
        try! FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true)
        git("init", "-q", "-b", "main")
        configureUser()
    }

    /// A second working copy of a remote (a colleague's machine).
    init(cloneOf remote: BareRemote) {
        path = TestDirs.fresh("clone")
        Git.run(["clone", "-q", remote.path, path], in: TestDirs.root)
        configureUser()
    }

    private func configureUser() {
        git("config", "user.email", "tests@gitwatchd.local")
        git("config", "user.name", "gitwatchd tests")
        git("config", "commit.gpgsign", "false")
    }

    /// The spec `gitwatchd add <flags> <path>` would produce.
    func spec(_ flags: String...) -> RepoSpec {
        RepoSpecParser.parse(flags + [path]).spec!
    }

    @discardableResult
    func git(_ args: String...) -> String { Git.run(Array(args), in: path).out }

    func write(_ file: String, _ contents: String) {
        let full = path + "/" + file
        try? FileManager.default.createDirectory(
            atPath: (full as NSString).deletingLastPathComponent, withIntermediateDirectories: true)
        try! contents.write(toFile: full, atomically: true, encoding: .utf8)
    }

    var commitCount: Int { Int(git("rev-list", "--count", "HEAD")) ?? 0 }
    var lastMessage: String { git("log", "-1", "--pretty=%s") }
    var lastMessageBody: String { git("log", "-1", "--pretty=%B") }
    var midMerge: Bool { FileManager.default.fileExists(atPath: path + "/.git/MERGE_HEAD") }
    var midRebase: Bool {
        FileManager.default.fileExists(atPath: path + "/.git/rebase-merge")
            || FileManager.default.fileExists(atPath: path + "/.git/rebase-apply")
    }

    /// Wire up an "origin" this repo pushes to. A real bare repo by default;
    /// pass a bogus URL to simulate an unreachable remote.
    @discardableResult
    func addOrigin(_ url: String? = nil) -> BareRemote {
        let remote = BareRemote()
        git("remote", "add", "origin", url ?? remote.path)
        return remote
    }

    func setOriginURL(_ url: String) { git("remote", "set-url", "origin", url) }

    func installFailingPreCommitHook(printing message: String) {
        let hook = path + "/.git/hooks/pre-commit"
        try! "#!/bin/sh\necho '\(message)'\nexit 1\n"
            .write(toFile: hook, atomically: true, encoding: .utf8)
        try! FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: hook)
    }

    func commit(_ file: String, _ contents: String, message: String) {
        write(file, contents)
        git("add", "-A")
        git("commit", "-q", "-m", message)
    }

    /// Set main to track origin/main, as a cloned repo would. The branch-less
    /// `pull --rebase <remote>` needs it.
    func trackOrigin() {
        git("fetch", "-q", "origin")
        git("branch", "-q", "--set-upstream-to=origin/main", "main")
    }

    /// Stop this repo mid-merge on a real conflict. Plain git only, so tests
    /// never exercise the engine during their own setup.
    func conflictedMerge() {
        commit("f.txt", "base\n", message: "base")
        git("checkout", "-q", "-b", "side")
        commit("f.txt", "side\n", message: "side edit")
        git("checkout", "-q", "main")
        commit("f.txt", "main\n", message: "main edit")
        git("merge", "side")            // conflicts, leaving MERGE_HEAD behind
    }

    static func withConflictedMerge() -> TestRepo {
        let repo = TestRepo()
        repo.conflictedMerge()
        return repo
    }
}

/// A bare repo standing in for the server side (GitHub etc).
final class BareRemote {
    let path: String
    init() {
        path = TestDirs.fresh("remote.git")
        Git.run(["init", "-q", "--bare", "-b", "main", path], in: TestDirs.root)
    }
    var commitCount: Int {
        let r = Git.run(["rev-list", "--count", "main"], in: path)
        return r.code == 0 ? Int(r.out) ?? 0 : 0
    }
    var lastMessage: String { Git.run(["log", "-1", "--pretty=%s", "main"], in: path).out }
}
