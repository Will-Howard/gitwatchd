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
    var dateFormat: String = "+%Y-%m-%d %H:%M:%S"  // -d
    var remote: String? = nil         // -r
    var branch: String? = nil         // -b
    var rebase: Bool = false          // -R  pull --rebase before push
    var message: String = "gitwatchd auto-commit (%d)"  // -m  (%d -> date)
    var exclude: String? = nil        // -x  regex; last one wins, as upstream
    var noMergeCommit: Bool = false   // -M
    var commitOnStart: Bool = false   // -f  commit pending changes when watching starts
    var gitDir: String? = nil         // -g  --git-dir
    var paused: Bool = false          // --paused (gitwatchd extension, not gitwatch:
                                      // config-level so a pause survives restarts)

    var name: String { (path as NSString).lastPathComponent }

    var isFileTarget: Bool {
        var isDir: ObjCBool = false
        FileManager.default.fileExists(atPath: path, isDirectory: &isDir)
        return !isDir.boolValue
    }

    /// Where git commands run: the target itself, or its parent for a file
    /// target (upstream's TARGETDIR).
    var workDir: String {
        isFileTarget ? (path as NSString).deletingLastPathComponent : path
    }

    func excludes(_ fullPath: String) -> Bool {
        guard let exclude, let regex = try? NSRegularExpression(pattern: exclude) else {
            return false
        }
        let range = NSRange(fullPath.startIndex..., in: fullPath)
        return regex.firstMatch(in: fullPath, range: range) != nil
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
            case "-s":
                guard let v = next(), let d = Double(v), d >= 0 else {
                    return (nil, "-s needs a number of seconds, 0 or more")
                }
                spec.settle = d
            case "-d": if let v = next() { spec.dateFormat = v }
            case "-r", "-p": if let v = next() { spec.remote = v } // -p: upstream's alias of -r
            case "-b": if let v = next() { spec.branch = v }
            case "-R": spec.rebase = true
            case "-m": if let v = next() { spec.message = v }
            case "-x":
                guard let v = next(), (try? NSRegularExpression(pattern: v)) != nil else {
                    return (nil, "-x needs a valid regular expression")
                }
                spec.exclude = v
            case "-M": spec.noMergeCommit = true
            case "-f": spec.commitOnStart = true
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

    /// The -d value goes to date(1) verbatim, as upstream: formats need a
    /// leading "+", and a bad format splices an empty string.
    static func formattedDate(_ fmt: String) -> String {
        let r = runProcess("/bin/date", [fmt])
        return r.code == 0 ? r.out : ""
    }
}
