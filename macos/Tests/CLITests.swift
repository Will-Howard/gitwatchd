import Foundation
import Testing

// CLI contract tests run the real CLI.run against a scratch config directory
// (never the user's ~/.gitwatchd) with daemon-launching disabled.
// Serialized: the config override is process-global.

/// Point the CLI at a scratch config file for the duration of one test.
private func withTemporaryConfig(_ body: () throws -> Void) rethrows {
    CLI.spawnsDaemon = false
    let dir = TestDirs.fresh("config")
    try! FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
    Config.overridePath = dir + "/gitwatchd"
    defer { Config.overridePath = nil }
    try body()
}

@Suite("CLI contract on an isolated config", .serialized)
struct CLIContract {

    @Test("add refuses a path that doesn't exist")
    func addMissingPath() {
        withTemporaryConfig {
            #expect(CLI.run(["add", TestDirs.root + "/no-such-dir"]) == 1)
            #expect(Config.specs().isEmpty, "nothing was written to the config")
        }
    }

    @Test("add refuses a directory that isn't a git repo")
    func addNonRepo() {
        withTemporaryConfig {
            let dir = TestDirs.fresh("plain-folder")
            try! FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            #expect(CLI.run(["add", dir]) == 1)
            #expect(Config.specs().isEmpty)
        }
    }

    @Test("add persists exactly what was typed, quotes and all")
    func addPersistsVerbatim() {
        withTemporaryConfig {
            let repo = TestRepo()
            #expect(CLI.run(["add", "-s", "5", "-m", "two words", repo.path]) == 0)
            #expect(Config.rawLines() == ["-s 5 -m \"two words\" \(repo.path)"])
            let spec = Config.specs()[0]
            #expect(spec.settle == 5)
            #expect(spec.message == "two words")
        }
    }

    @Test("`gitwatchd .` persists the repo it was run in, not the dot")
    func addResolvesRelativeTarget() {
        withTemporaryConfig {
            let repo = TestRepo()
            #expect(CLI.run(["."], invokedFrom: repo.path) == 0)
            #expect(Config.rawLines() == [repo.path])
            #expect(Config.specs()[0].path == repo.path,
                    "the daemon watches that repo, wherever it reads the config from")
        }
    }

    @Test("a tilde target expands and an absolute target survives unchanged")
    func targetResolutionForms() {
        let elsewhere = TestDirs.root
        #expect(RepoSpecParser.parse(["~/code"], invokedFrom: elsewhere).resolved
                == [(NSHomeDirectory() as NSString).appendingPathComponent("code")])
        #expect(RepoSpecParser.parse(["/tmp/x"], invokedFrom: elsewhere).resolved == ["/tmp/x"])
    }

    @Test("an invocation with no target is rejected as before, its args untouched")
    func addWithoutTarget() {
        withTemporaryConfig {
            #expect(CLI.run(["add", "-s", "5"], invokedFrom: TestDirs.root) == 1)
            #expect(Config.rawLines().isEmpty)
        }
        let parsed = RepoSpecParser.parse(["-s", "5"], invokedFrom: TestDirs.root)
        #expect(parsed.error == "no target path given")
        #expect(parsed.resolved == ["-s", "5"])
    }

    @Test("adding the same repo twice fails and leaves one entry")
    func duplicateAdd() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            #expect(CLI.run([repo.path]) == 1)
            #expect(CLI.run(["add", "-s", "5", repo.path]) == 1, "different flags, same repo")
            #expect(Config.specs().count == 1)
        }
    }

    @Test("a bare `gitwatchd <path>` is an implicit add")
    func bareAdd() {
        withTemporaryConfig {
            let repo = TestRepo()
            #expect(CLI.run([repo.path]) == 0)
            #expect(Config.specs().count == 1)
            #expect(Config.specs()[0].path == repo.path)
        }
    }

    @Test("rm matches by name (the repo's folder name)")
    func rmByName() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            #expect(CLI.run(["rm", repo.spec().name]) == 0)
            #expect(Config.specs().isEmpty)
        }
    }

    @Test("rm matches by full path")
    func rmByPath() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            #expect(CLI.run(["rm", repo.path]) == 0)
            #expect(Config.specs().isEmpty)
        }
    }

    @Test("rm of an unknown repo fails and leaves the config alone")
    func rmUnknown() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            #expect(CLI.run(["rm", "not-a-watched-repo"]) == 1)
            #expect(Config.specs().count == 1)
        }
    }

    @Test("pause and resume by name flip --paused in the config")
    func pauseResumeByName() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run(["-s", "5", repo.path])
            #expect(CLI.run(["pause", repo.spec().name]) == 0)
            #expect(Config.specs()[0].paused)
            #expect(Config.rawLines()[0] == "--paused -s 5 \(repo.path)",
                    "the rest of the line survives untouched")
            #expect(CLI.run(["resume", repo.spec().name]) == 0)
            #expect(!Config.specs()[0].paused)
            #expect(Config.rawLines()[0] == "-s 5 \(repo.path)")
        }
    }

    @Test("pause by full path works too")
    func pauseByPath() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            #expect(CLI.run(["pause", repo.path]) == 0)
            #expect(Config.specs()[0].paused)
        }
    }

    @Test("pausing twice fails politely and changes nothing")
    func pauseTwice() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            _ = CLI.run(["pause", repo.path])
            let lines = Config.rawLines()
            #expect(CLI.run(["pause", repo.path]) == 1)
            #expect(Config.rawLines() == lines)
        }
    }

    @Test("version and --version print instead of falling through to add")
    func version() {
        withTemporaryConfig {
            #expect(CLI.run(["version"]) == 0)
            #expect(CLI.run(["--version"]) == 0)
            #expect(Config.specs().isEmpty)
        }
    }

    @Test("pausing an unknown repo fails")
    func pauseUnknown() {
        withTemporaryConfig {
            #expect(CLI.run(["pause", "nothing-here"]) == 1)
        }
    }

    @Test("broken config entries surface in load() and status, like the menu")
    func configErrorsSurface() {
        withTemporaryConfig {
            let repo = TestRepo()
            _ = CLI.run([repo.path])
            Config.append(TestDirs.root + "/vanished-repo")
            Config.append("-z bogus /tmp/x")
            let (specs, errors) = Config.load()
            #expect(specs.count == 1, "the healthy repo is unaffected")
            #expect(errors.count == 2)
            #expect(errors.contains { $0.reason == "repo not found" })
            #expect(errors.contains { $0.reason.contains("unknown flag") })
            #expect(CLI.run(["status"]) == 0, "status renders them rather than crashing or hiding them")
        }
    }
}

// The help is the contract; these hold it to the implementation. The
// canonical lists below ARE the documented surface: adding a flag or command
// to only one side of the contract fails one of these tests.

@Suite("Help stays in sync with the implementation")
struct HelpSync {

    static let valueFlags = ["-s": "2", "-r": "origin", "-b": "main", "-m": "msg",
                             "-d": "%Y", "-c": "echo msg", "-l": "10", "-L": "10",
                             "-x": "\\.log$", "-g": "/tmp/gd"]
    static let boolFlags = ["-R", "-M", "-C", "-f", "--paused"]
    static let commands = ["add", "rm", "pause", "resume", "status",
                           "start", "stop", "autostart", "config", "help", "version"]

    @Test("every documented flag is accepted by the parser")
    func documentedFlagsParse() {
        for (flag, value) in Self.valueFlags {
            let (spec, err) = RepoSpecParser.parse([flag, value, "/tmp/x"])
            #expect(spec != nil && err == nil, "\(flag) is documented but doesn't parse")
        }
        for flag in Self.boolFlags {
            let (spec, err) = RepoSpecParser.parse([flag, "/tmp/x"])
            #expect(spec != nil && err == nil, "\(flag) is documented but doesn't parse")
        }
    }

    @Test("every flag and command in the contract appears in the help")
    func contractAppearsInHelp() {
        for flag in Self.valueFlags.keys {
            #expect(CLI.usageText.contains("\(flag) <"), "help is missing \(flag) with its <metavar>")
        }
        for flag in Self.boolFlags {
            #expect(CLI.usageText.contains(flag), "help is missing \(flag)")
        }
        for command in Self.commands {
            #expect(CLI.usageText.contains(command), "help is missing the \(command) command")
        }
    }

    @Test("the help documents no flags outside the contract")
    func noPhantomFlags() {
        let known = Set(Self.valueFlags.keys).union(Self.boolFlags)
        let documented = CLI.usageText.split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { $0.hasPrefix("-") }
            .compactMap { $0.split(separator: " ").first.map(String.init) }
        for flag in documented {
            #expect(known.contains(flag), "help documents \(flag), which the contract list doesn't know")
        }
    }
}
