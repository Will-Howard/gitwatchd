import AppKit

// The `gitwatchd` command-line client. Writes the shared config file (so it works
// even when the daemon is down) and nudges the running daemon, which live-reloads.
// `add` accepts gitwatch's own flags verbatim, so gitwatch users need no relearning.
enum CLI {
    static func run(_ args: [String]) -> Int32 {
        guard let first = args.first else { printUsage(); return 0 }

        switch first {
        case "help", "-h", "--help": printUsage(); return 0
        case "ls", "list":           return list()
        case "rm", "remove":         return remove(Array(args.dropFirst()))
        case "status":               return status()
        case "doctor":               return doctor()   // internal/undocumented: env diagnostic
        case "start":                return startDaemon()
        case "stop":                 return stopDaemon()
        case "config":               return config(Array(args.dropFirst()))
        case "autostart":            return autostart(Array(args.dropFirst()))
        case "add":                  return add(Array(args.dropFirst()))
        default:
            // Bare form: `gitwatchd [flags] <target>` — implicit add.
            return add(args)
        }
    }

    // MARK: - add

    private static func add(_ args: [String]) -> Int32 {
        let (spec, err) = RepoSpecParser.parse(args)
        guard let spec else { warn(err ?? "could not parse arguments"); return 1 }

        guard FileManager.default.fileExists(atPath: spec.path) else {
            warn("path does not exist: \(spec.path)"); return 1
        }
        guard Git.isRepo(spec.path) else {
            warn("not a git repo: \(spec.path)\n  run: git init \(spec.path)"); return 1
        }

        // Persist exactly what the user typed (gitwatch-style line).
        let line = args.map(quoteIfNeeded).joined(separator: " ")
        Config.append(line)
        ensureDaemonRunning()

        let pushNote = spec.remote.map { "→ \($0)/\(spec.branch ?? Git.currentBranch(spec.path))" } ?? "local only"
        print("✓ watching  \(spec.name)  (\(spec.path))  \(pushNote), settle \(Int(spec.settle))s")
        print("  gitwatchd ls   to see status")
        return 0
    }

    // MARK: - ls

    private static func list() -> Int32 {
        let specs = Config.specs()
        if specs.isEmpty { print("No repos watched. Add one:  gitwatchd ."); return 0 }
        for s in specs {
            let ok = Git.isRepo(s.path)
            let branch = ok ? Git.currentBranch(s.path) : "—"
            let pending = ok ? Git.pendingCount(s.path) : 0
            let state = !ok ? "⚠ missing" : pending > 0 ? "✎ \(pending) pending" : "✓ idle"
            let dest = s.remote.map { " → \($0)/\(s.branch ?? branch)" } ?? ""
            print("  \(state.padding(toLength: 14, withPad: " ", startingAt: 0)) \(s.name)  (\(s.path))  \(branch)\(dest)")
        }
        return 0
    }

    // MARK: - rm

    private static func remove(_ args: [String]) -> Int32 {
        guard let needle = args.first else { warn("usage: gitwatchd rm <name|path>"); return 1 }
        let n = Config.remove(matching: (needle as NSString).expandingTildeInPath)
            + (needle.contains("/") ? 0 : Config.remove(matching: needle))
        if n == 0 { warn("no watched repo matches \(needle)"); return 1 }
        ensureDaemonRunning()
        print("✓ stopped watching \(needle) (\(n) entr\(n == 1 ? "y" : "ies") removed)")
        return 0
    }

    // MARK: - status / config / daemon

    private static func status() -> Int32 {
        let running = isDaemonRunning()
        print("daemon:  \(running ? "running" : "not running")")
        print("config:  \(Config.path)")
        print("repos:   \(Config.specs().count)")
        return 0
    }

    /// Internal, undocumented dev diagnostic (not in `help`): shows the environment
    /// the login-launched DAEMON will use for git — the "works in terminal, fails
    /// from the app" trap, made visible. Kept for debugging, not a user-facing feature.
    private static func doctor() -> Int32 {
        let (env, captureError) = GitRuntime.loginShellEnv()   // what the daemon re-derives
        let git = GitRuntime.findGit(in: env)
        print("What the login-launched daemon will use for git:")
        if let captureError { print("  ⚠ env capture FAILED: \(captureError)") }
        print("  git binary     \(git)")
        print("  git --version  \(capture(git, ["--version"], env).out)")
        print("  PATH           \(env["PATH"] ?? "(unset → minimal)")")

        if let sock = env["SSH_AUTH_SOCK"], !sock.isEmpty {
            let keys = capture("/usr/bin/ssh-add", ["-l"], env)
            let n = keys.code == 0 ? keys.out.split(separator: "\n").count : 0
            print("  SSH_AUTH_SOCK  present · \(n) key\(n == 1 ? "" : "s") in agent")
            if n == 0 { print("                 ⚠ no keys loaded — SSH pushes may fail. Add: ssh-add --apple-use-keychain ~/.ssh/id_ed25519") }
        } else {
            print("  SSH_AUTH_SOCK  (unset) ⚠ SSH pushes will fail from the daemon")
        }

        let hook = GitRuntime.envHookPath
        let hasHook = FileManager.default.fileExists(atPath: hook)
        print("  env.sh hook    \(hasHook ? hook : "(none — create it to inject custom auth: tokens, ssh-add, GIT_SSH)")")
        return 0
    }

    /// Run a tool with an explicit environment and capture stdout+stderr.
    private static func capture(_ exe: String, _ args: [String], _ env: [String: String]) -> (code: Int32, out: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: exe)
        p.arguments = args
        p.environment = env
        let pipe = Pipe(); p.standardOutput = pipe; p.standardError = pipe
        do { try p.run() } catch { return (-1, "\(error)") }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
    }

    private static func config(_ args: [String]) -> Int32 {
        switch args.first {
        case "path", nil: print(Config.path)
        case "edit": NSWorkspace.shared.open(URL(fileURLWithPath: Config.path))
        default: warn("usage: gitwatchd config [path|edit]"); return 1
        }
        return 0
    }

    private static func autostart(_ args: [String]) -> Int32 {
        switch args.first {
        case "on":  if let e = LaunchAtLogin.set(true)  { warn(e); return 1 }; print("✓ launch at login: on")
        case "off": if let e = LaunchAtLogin.set(false) { warn(e); return 1 }; print("✓ launch at login: off")
        case "status", nil: print("launch at login: \(LaunchAtLogin.statusText)")
        default: warn("usage: gitwatchd autostart [on|off|status]"); return 1
        }
        return 0
    }

    private static func startDaemon() -> Int32 {
        if isDaemonRunning() { print("daemon already running"); return 0 }
        ensureDaemonRunning()
        print(isDaemonRunning() ? "✓ daemon started" : "could not locate gitwatchd.app to launch")
        return 0
    }

    private static func stopDaemon() -> Int32 {
        for app in NSRunningApplication.runningApplications(withBundleIdentifier: bundleID) {
            app.terminate()
        }
        print("✓ daemon stopped")
        return 0
    }

    // MARK: - daemon discovery

    static let bundleID = "com.gitwatchd.app"

    private static func isDaemonRunning() -> Bool {
        !NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).isEmpty
    }

    /// Best-effort: launch the menu-bar daemon if it isn't running.
    private static func ensureDaemonRunning() {
        guard !isDaemonRunning() else { return }
        guard let appURL = locateApp() else { return }
        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = false
        NSWorkspace.shared.openApplication(at: appURL, configuration: cfg)
    }

    private static func locateApp() -> URL? {
        let fm = FileManager.default
        var candidates = ["/Applications/gitwatchd.app",
                          (NSHomeDirectory() as NSString).appendingPathComponent("Applications/gitwatchd.app")]
        if let env = ProcessInfo.processInfo.environment["GITWATCHD_APP"] { candidates.insert(env, at: 0) }
        // Also look next to the CLI binary (e.g. build/gitwatchd.app during dev).
        let binDir = (CommandLine.arguments[0] as NSString).deletingLastPathComponent
        candidates.append((binDir as NSString).appendingPathComponent("gitwatchd.app"))
        return candidates.first { fm.fileExists(atPath: $0) }.map { URL(fileURLWithPath: $0) }
    }

    // MARK: - helpers

    private static func quoteIfNeeded(_ s: String) -> String {
        s.contains(" ") ? "\"\(s)\"" : s
    }

    private static func warn(_ msg: String) { FileHandle.standardError.write(("✗ " + msg + "\n").data(using: .utf8)!) }

    private static func printUsage() {
        print("""
        gitwatchd — always-on gitwatch daemon

        USAGE
          gitwatchd [gitwatch flags] <path>   watch a repo (gitwatch-compatible)
          gitwatchd add [flags] <path>        same, explicit
          gitwatchd ls                        list watched repos + status
          gitwatchd rm <name|path>            stop watching a repo
          gitwatchd status                    daemon + config summary
          gitwatchd start | stop              start/stop the menu-bar daemon
          gitwatchd autostart [on|off|status] launch at login
          gitwatchd config [path|edit]        show / open the config file

        GITWATCH FLAGS (on add)
          -s secs   debounce      -r remote  push after commit    -R  pull --rebase first
          -b branch push branch   -m msg     commit msg (%d=date)  -d fmt  date format
          -x glob   exclude        -M         no commit mid-merge  -g dir  --git-dir

        EXAMPLE
          gitwatchd -r origin -b main -s 5 ~/code/blog
        """)
    }
}
