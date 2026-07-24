import AppKit

// The `gitwatchd` command-line client. Writes the shared config file (so it works
// even when the daemon is down) and nudges the running daemon, which live-reloads.
// `add` accepts gitwatch's own flags verbatim, so gitwatch users need no relearning.
enum CLI {
    static let version = "0.1.1"   // keep in step with Resources/Info.plist

    static func run(_ args: [String]) -> Int32 {
        guard let first = args.first else { printUsage(); return 0 }

        switch first {
        case "help", "-h", "--help": printUsage(); return 0
        case "version", "--version": print("gitwatchd \(version)"); return 0
        case "rm", "remove":         return remove(Array(args.dropFirst()))
        case "pause":                return setPaused(Array(args.dropFirst()), true)
        case "resume":               return setPaused(Array(args.dropFirst()), false)
        case "status":               return status()
        case "doctor":               return doctor()
        case "start":                return startDaemon()
        case "stop":                 return stopDaemon()
        case "config":               return config(Array(args.dropFirst()))
        case "autostart":            return autostart(Array(args.dropFirst()))
        case "add":                  return add(Array(args.dropFirst()))
        default:
            // Bare form: `gitwatchd [flags] <target>`: implicit add.
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
        guard Git.isRepo(spec.workDir, gitDir: spec.gitDir) else {
            warn("not inside a git repo: \(spec.path)\n  run: git init \(spec.workDir)"); return 1
        }
        guard !Config.specs().contains(where: { $0.path == spec.path }) else {
            warn("already watching \(spec.name) (\(spec.path))"); return 1
        }

        // Persist exactly what the user typed (gitwatch-style line).
        let line = args.map(Config.quoteIfNeeded).joined(separator: " ")
        Config.append(line)
        ensureDaemonRunning()

        let pushNote = spec.remote.map { "→ \($0)/\(spec.branch ?? Git.currentBranch(spec.workDir))" } ?? "local only"
        print("✓ watching  \(spec.name)  (\(spec.path))  \(pushNote), settle \(Int(spec.settle))s")
        print("  gitwatchd status   to see everything watched")
        return 0
    }

    // MARK: - rm

    private static func remove(_ args: [String]) -> Int32 {
        guard let needle = args.first else { warn("usage: gitwatchd rm <name|path>"); return 1 }
        let n = Config.remove(matching: needle)
        if n == 0 { warn("no watched repo matches \(needle)"); return 1 }
        ensureDaemonRunning()
        print("✓ stopped watching \(needle) (\(n) entr\(n == 1 ? "y" : "ies") removed)")
        return 0
    }

    // MARK: - pause / resume

    private static func setPaused(_ args: [String], _ paused: Bool) -> Int32 {
        let verb = paused ? "pause" : "resume"
        guard let needle = args.first else { warn("usage: gitwatchd \(verb) <name|path>"); return 1 }
        let n = Config.setPaused(matching: needle, paused: paused)
        guard n > 0 else {
            let known = Config.specs().contains { Config.matches($0, needle) }
            warn(known ? "\(needle) is already \(paused ? "paused" : "watching")"
                       : "no watched repo matches \(needle)")
            return 1
        }
        ensureDaemonRunning()
        print(paused ? "⏸ paused \(needle)  (resume with: gitwatchd resume \(needle))"
                     : "✓ resumed \(needle); catching up on anything that changed meanwhile")
        return 0
    }

    // MARK: - status / config / daemon

    /// The menu, rendered for the terminal: same rows, same strings, plus the
    /// daemon's published error state when it is running.
    private static func status() -> Int32 {
        let running = isDaemonRunning()
        print("daemon:  \(running ? "running" : "not running")")
        print("config:  \(Config.path)")
        print("")
        let (specs, errors) = Config.load()
        if specs.isEmpty && errors.isEmpty { print("No repos watched yet"); return 0 }
        let daemonErrors = running ? StateStore.errors() : [:]
        print("Watching \(specs.count) repo\(specs.count == 1 ? "" : "s")")
        print("")
        for s in specs {
            let branch = Git.currentBranch(s.workDir, gitDir: s.gitDir)
            let pending = Git.pendingCount(s.workDir, gitDir: s.gitDir)
            let err = daemonErrors[s.path]
            print(StatusFormat.rowTitle(name: s.name, branch: branch, paused: s.paused,
                                        pending: pending, errorLabel: err?.errorLabel))
            print("     " + Git.lastCommitSummary(s.workDir, gitDir: s.gitDir))
            if let err {
                print("     " + StatusFormat.errorHeadline(label: err.errorLabel, attempts: err.attempts))
                if let detail = err.detail, !detail.isEmpty {
                    print("     " + StatusFormat.truncated(detail))
                }
                print("     " + StatusFormat.retryLine(lastTried: err.lastAttempt,
                                                       nextRetry: err.nextRetry, now: Date()))
            }
        }
        for e in errors {
            print(StatusFormat.configErrorRow(label: e.label, reason: e.reason))
            print("     " + e.detail)
        }
        return 0
    }

    /// Undocumented diagnostic: the environment the login-launched daemon
    /// will use for git.
    private static func doctor() -> Int32 {
        let (env, captureError) = GitRuntime.loginShellEnv()   // what the daemon re-derives
        let git = GitRuntime.findGit(in: env)
        print("What the login-launched daemon will use for git:")
        if let captureError { print("  ⚠ env capture FAILED: \(captureError)") }
        print("  git binary     \(git)")
        print("  git --version  \(runProcess(git, ["--version"], env: env).out)")
        print("  PATH           \(env["PATH"] ?? "(unset → minimal)")")

        if let sock = env["SSH_AUTH_SOCK"], !sock.isEmpty {
            let keys = runProcess("/usr/bin/ssh-add", ["-l"], env: env)
            let n = keys.code == 0 ? keys.out.split(separator: "\n").count : 0
            print("  SSH_AUTH_SOCK  present · \(n) key\(n == 1 ? "" : "s") in agent")
            if n == 0 { print("                 ⚠ no keys loaded: SSH pushes may fail. Add: ssh-add --apple-use-keychain ~/.ssh/id_ed25519") }
        } else {
            print("  SSH_AUTH_SOCK  (unset) ⚠ SSH pushes will fail from the daemon")
        }

        return 0
    }

    private static func config(_ args: [String]) -> Int32 {
        switch args.first {
        case "path", nil: print(Config.path)
        case "edit": return editConfig()
        default: warn("usage: gitwatchd config [path|edit]"); return 1
        }
        return 0
    }

    /// Open the config in the editor `git commit` would use: `git var
    /// GIT_EDITOR` resolves $GIT_EDITOR, core.editor, $VISUAL, $EDITOR, then
    /// vi. Runs in this terminal (the menu's "Open Config File" stays GUI).
    ///
    /// This execs the editor in place of the CLI rather than spawning it:
    /// Foundation's Process puts children in their own process group, which
    /// costs a full-screen editor the terminal (it hangs on SIGTTIN reading
    /// the tty, and ^C orphans it into a HUP). There is nothing to do after
    /// the editor exits anyway; the daemon live-reloads off the file change.
    private static func editConfig() -> Int32 {
        Config.ensureExists()
        let r = Git.run(["var", "GIT_EDITOR"], in: FileManager.default.currentDirectoryPath)
        let editor = (r.code == 0 && !r.out.isEmpty) ? r.out : "vi"
        // The editor value can be a command with flags (e.g. "code --wait"),
        // so hand it to the shell; the path rides in as $1, safely quoted.
        let argv = ["sh", "-c", "\(editor) \"$1\"", "gitwatchd-edit", Config.path]
        var cArgv = argv.map { strdup($0) }
        cArgv.append(nil)
        execv("/bin/sh", cArgv)
        warn("couldn't launch editor '\(editor)': \(String(cString: strerror(errno)))")
        return 1   // reached only if execv itself failed
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
        guard let appURL = locateApp() else {
            warn("gitwatchd.app not found in /Applications or ~/Applications (run `make install`, or set GITWATCHD_APP)")
            return 1
        }
        // openApplication is asynchronous; wait for its verdict.
        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = false
        var failure: String?
        let done = DispatchSemaphore(value: 0)
        NSWorkspace.shared.openApplication(at: appURL, configuration: cfg) { _, error in
            failure = error?.localizedDescription
            done.signal()
        }
        _ = done.wait(timeout: .now() + 15)
        if let failure { warn("could not launch \(appURL.path): \(failure)"); return 1 }
        for _ in 0..<20 where !isDaemonRunning() { usleep(100_000) }  // registration can lag the callback
        print(isDaemonRunning() ? "✓ daemon started" : "launch requested; the menu bar icon should appear shortly")
        return 0
    }

    private static func stopDaemon() -> Int32 {
        guard isDaemonRunning() else { print("daemon not running"); return 0 }
        for app in NSRunningApplication.runningApplications(withBundleIdentifier: bundleID) {
            app.terminate()
        }
        // terminate() is asynchronous, like openApplication: wait for the exit
        // so `gitwatchd stop && gitwatchd start` doesn't race the old process.
        for _ in 0..<50 where isDaemonRunning() { usleep(100_000) }
        if isDaemonRunning() {
            warn("daemon did not exit; force with: pkill -x gitwatchd")
            return 1
        }
        print("✓ daemon stopped")
        return 0
    }

    // MARK: - daemon discovery

    static let bundleID = "com.gitwatchd.app"

    private static func isDaemonRunning() -> Bool {
        !NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).isEmpty
    }

    /// Tests set this false so CLI calls don't launch the real menu-bar app.
    static var spawnsDaemon = true

    /// Best-effort: launch the menu-bar daemon if it isn't running.
    private static func ensureDaemonRunning() {
        guard spawnsDaemon, !isDaemonRunning() else { return }
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

    private static func warn(_ msg: String) { FileHandle.standardError.write(("✗ " + msg + "\n").data(using: .utf8)!) }

    // A constant so tests can hold the help to the implementation.
    private static func printUsage() { print(usageText) }

    static let usageText = """
        gitwatchd - daemon that watches git repos and auto-commits changes

        USAGE
          gitwatchd [flags] <path>     watch a repo
          gitwatchd <command> [args]

        EXAMPLES
          gitwatchd .                       watch the current repo, commit locally
          gitwatchd -r origin .             also push every commit to origin
          gitwatchd -r origin -b main -R .  two-way sync: fetch commits made on
                                            other machines and rebase yours on top
                                            before each push to origin/main
          gitwatchd status                  see everything being watched
          gitwatchd rm blog                 stop watching, by name or path

        COMMANDS
          add [flags] <path>    watch a repo (bare `gitwatchd [flags] <path>` works too)
          rm <name|path>        stop watching a repo
          pause <name|path>     stop watching temporarily; the repo stays listed
          resume <name|path>    start watching again and commit what piled up
          status                everything the menu shows, in the terminal
          start, stop           start or stop the menu-bar daemon
          autostart [on|off|status]
                                launch the daemon at login (on by default on install)
          config [path|edit]    print the config file path, or open it in your
                                editor (the one `git commit` uses)
          help                  show this help
          version               print the version

        FLAGS (for add)
          -s <secs>     Wait <secs> after the last change before committing, so a
                        batch of writes lands as one commit. Default: 2.
          -r <remote>   Push to <remote> after every commit. Default: no push.
          -b <branch>   Branch to push to. Without it, a plain `git push <remote>`
                        decides. Only meaningful together with -r.
          -R            Before each push, pull commits made elsewhere and rebase
                        yours on top (`git pull --rebase <remote>`). Use with -r
                        when more than one machine pushes to the same branch.
          -m <msg>      Commit message; %d becomes the timestamp.
                        Default: "gitwatchd auto-commit (%d)".
          -d <fmt>      Format string for that timestamp (see `man date`).
                        Default: "+%Y-%m-%d %H:%M:%S".
          -l <lines>    Use the diff itself as the commit message (file:line: change,
                        in colour), up to <lines> lines (0 = no limit). A diff
                        longer than <lines> falls back to the `git diff --stat`
                        summary. Overrides -m.
          -L <lines>    Same as -l, without colour. Known bug: on git versions > 2.39
                        this falls back to a status summary.
          -c <command>  Run <command> and use its output as the commit message.
                        Overrides -m and -d.
          -C            Pipe the changed file names into the -c command's stdin.
                        Requires -c flag, e.g. `gitwatchd -c 'xargs echo updated:' -C .`
          -x <pattern>  Skip changes whose path matches this regular
                        expression (e.g. '\\.log$' or 'build/').
          -M            Skip committing while the repo has a merge in progress.
          -f            Commit anything already pending as soon as watching
                        starts (daemon launch, or when the repo is added).
          -g <path>     Location of the .git directory, if elsewhere (--git-dir).
          --paused      Keep the repo in the config but don't watch it. This is
                        what `gitwatchd pause` and the menu's Pause Watching set.

        The daemon lives in the menu bar and watches every repo listed in ~/.gitwatchd
        """
}
