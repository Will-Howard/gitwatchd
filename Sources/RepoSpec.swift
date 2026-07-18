import Foundation

// A watched-repo specification, expressed in gitwatch's own flag vocabulary so
// that gitwatch users read our config/CLI with zero translation.
//
//   gitwatch  [-s secs] [-d fmt] [-r remote [-b branch]] [-R] [-m msg]
//             [-l|-L lines] [-x pattern] [-M] [-g gitdir] [-e events] <target>
//
// Each config line is exactly the argument string you'd pass to gitwatch.
struct RepoSpec {
    var path: String
    var settle: Double = 2            // -s  debounce seconds
    var dateFormat: String = "%Y-%m-%d %H:%M:%S"  // -d
    var remote: String? = nil         // -r
    var branch: String? = nil         // -b
    var rebase: Bool = false          // -R  pull --rebase before push
    var message: String = "gitwatchd auto-commit (%d)"  // -m  (%d -> date)
    var exclude: [String] = []        // -x  (repeatable)
    var noMergeCommit: Bool = false   // -M
    var gitDir: String? = nil         // -g  --git-dir
    var paused: Bool = false          // --paused (gitwatchd extension, not gitwatch:
                                      // config-level so a pause survives restarts)

    var name: String { (path as NSString).lastPathComponent }

    /// True if the -x patterns exclude this changed path. A pattern matches
    /// the bare file name or the full path (fnmatch, like the inotifywait
    /// exclude gitwatch feeds -x into). Patterns are repeatable; none means
    /// nothing is excluded.
    func excludes(_ fullPath: String) -> Bool {
        guard !exclude.isEmpty else { return false }
        let name = (fullPath as NSString).lastPathComponent
        return exclude.contains { pattern in
            fnmatch(pattern, name, 0) == 0 || fnmatch(pattern, fullPath, 0) == 0
        }
    }
}

enum RepoSpecParser {
    /// Parse a gitwatch-style argument list into a RepoSpec.
    /// Returns nil + an error message if there's no valid target.
    static func parse(_ args: [String]) -> (spec: RepoSpec?, error: String?) {
        var spec = RepoSpec(path: "")
        var target: String? = nil
        var i = 0
        func next() -> String? { i += 1; return i < args.count ? args[i] : nil }

        while i < args.count {
            let a = args[i]
            switch a {
            case "-s": if let v = next(), let d = Double(v) { spec.settle = d }
            case "-d": if let v = next() { spec.dateFormat = v }
            case "-r", "-p": if let v = next() { spec.remote = v } // -p: upstream's alias of -r
            case "-b": if let v = next() { spec.branch = v }
            case "-R": spec.rebase = true
            case "-m": if let v = next() { spec.message = v }
            case "-x": if let v = next() { spec.exclude.append(v) }
            case "-M": spec.noMergeCommit = true
            case "-g": if let v = next() { spec.gitDir = v }
            case "-e": _ = next() // inotify events: accepted, no-op on macOS (as upstream)
            case "--paused": spec.paused = true // gitwatchd extension (see RepoSpec)
            default:
                if a.hasPrefix("-") {
                    return (nil, "unknown flag \(a)")
                } else {
                    target = a // last bare arg wins as the target
                }
            }
            i += 1
        }

        guard let t = target else { return (nil, "no target path given") }
        spec.path = (t as NSString).expandingTildeInPath
        if !(spec.path as NSString).isAbsolutePath {
            spec.path = (FileManager.default.currentDirectoryPath as NSString)
                .appendingPathComponent(spec.path)
        }
        spec.path = (spec.path as NSString).standardizingPath
        return (spec, nil)
    }

    /// Render the current date using gitwatch's strftime-style format via /bin/date.
    static func formattedDate(_ fmt: String) -> String {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/date")
        p.arguments = ["+\(fmt)"]
        let pipe = Pipe(); p.standardOutput = pipe
        do { try p.run() } catch { return "" }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (String(data: data, encoding: .utf8) ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
