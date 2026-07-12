import Foundation

/// Resolves the environment + git binary the daemon uses for every git call.
///
/// A GUI/launchd-launched daemon inherits only a minimal environment: PATH is
/// just /usr/bin:/bin:/usr/sbin:/sbin and none of your ~/.zshrc exports are set.
/// It would therefore find a different git than your terminal (missing a Homebrew
/// git / credential helpers) and miss custom auth — the classic "works in the
/// terminal, fails from the app" trap. To avoid it, the daemon re-derives your
/// real login-shell environment (same technique as VS Code / exec-path-from-shell),
/// optionally sourcing ~/.config/gitwatchd/env.sh for custom auth. The CLI already
/// runs inside your shell, so it simply uses its own environment.
enum GitRuntime {
    /// Set true by the daemon entry point before any git call. Left false for the CLI.
    static var isDaemon = false

    /// Optional user hook sourced during capture — a place to inject auth
    /// (export tokens, `ssh-add`, set GIT_SSH, …).
    static var envHookPath: String { (Config.dir as NSString).appendingPathComponent("env.sh") }

    struct Resolution {
        let gitPath: String
        let env: [String: String]
        let error: String?   // non-nil if daemon login-shell env capture failed
    }

    /// Resolved once per process: which git to run, in what environment, and whether
    /// login-shell env capture failed. On failure we DON'T pretend everything is fine —
    /// the daemon surfaces the error (menu + log) so a broken env loader is obvious,
    /// rather than silently limping along on a minimal environment.
    static let resolved: Resolution = {
        if isDaemon {
            let r = loginShellEnv()
            return Resolution(gitPath: findGit(in: r.env), env: r.env, error: r.error)
        }
        let env = ProcessInfo.processInfo.environment
        return Resolution(gitPath: findGit(in: env), env: env, error: nil)
    }()

    // MARK: - Login-shell environment capture (daemon only)

    /// Run the user's login+interactive shell to source their profile and dump env.
    /// Returns the parsed environment, plus a non-nil error string if capture failed
    /// (in which case env falls back to the minimal process env — flagged, not hidden).
    static func loginShellEnv() -> (env: [String: String], error: String?) {
        let shell = ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh"
        let marker = "__GITWATCHD_ENV__"
        // -i -l → source login files (.zprofile/.bash_profile) AND interactive files
        // (.zshrc/.bashrc), then our optional hook, then print env between markers.
        let script = """
        [ -r \(quote(envHookPath)) ] && . \(quote(envHookPath)) 2>/dev/null
        printf '%s\\n' \(marker); /usr/bin/env; printf '%s\\n' \(marker)
        """
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: shell)
        proc.arguments = ["-ilc", script]
        proc.standardInput = FileHandle.nullDevice
        let out = Pipe()
        proc.standardOutput = out
        proc.standardError = FileHandle.nullDevice
        guard (try? proc.run()) != nil else {
            return (ProcessInfo.processInfo.environment, "couldn't launch login shell: \(shell)")
        }
        // Guard against a slow/hanging rc file: kill after 5s and fall back.
        DispatchQueue.global().asyncAfter(deadline: .now() + 5) { if proc.isRunning { proc.terminate() } }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        proc.waitUntilExit()
        let text = String(data: data, encoding: .utf8) ?? ""
        // A successful capture always yields a PATH; its absence means capture broke.
        guard let parsed = parseEnv(text, marker: marker), parsed["PATH"] != nil else {
            return (ProcessInfo.processInfo.environment,
                    "login-shell env capture failed — check your \(shell) startup files")
        }
        return (parsed, nil)
    }

    private static func parseEnv(_ text: String, marker: String) -> [String: String]? {
        let parts = text.components(separatedBy: marker)
        guard parts.count >= 3 else { return nil } // before / body / after
        var env: [String: String] = [:]
        for line in parts[1].split(separator: "\n") {
            guard let eq = line.firstIndex(of: "=") else { continue }
            env[String(line[..<eq])] = String(line[line.index(after: eq)...])
        }
        return env.isEmpty ? nil : env
    }

    // MARK: - git discovery

    /// Find git the way the shell would: explicit override, else first on PATH,
    /// else Apple's /usr/bin/git (a CLT stub on a bare Mac).
    static func findGit(in env: [String: String]) -> String {
        if let override = env["GITWATCHD_GIT"], !override.isEmpty { return override }
        for dir in (env["PATH"] ?? "").split(separator: ":") {
            let candidate = (String(dir) as NSString).appendingPathComponent("git")
            if FileManager.default.isExecutableFile(atPath: candidate) { return candidate }
        }
        return "/usr/bin/git"
    }

    private static func quote(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
