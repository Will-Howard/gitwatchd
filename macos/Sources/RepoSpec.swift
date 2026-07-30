import Foundation

// A watched-repo specification, expressed in gitwatch's own flag vocabulary so
// that gitwatch users read our config/CLI with zero translation.
//
//   gitwatch  [-s secs] [-d fmt] [-r remote [-b branch]] [-R] [-m msg]
//             [-c cmd] [-C] [-l|-L lines] [-x pattern] [-M] [-g gitdir]
//             [-e events] <target>
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
    var commitCommand: String? = nil  // -c  its stdout becomes the commit message
    var pipeChangedFiles: Bool = false // -C  pipe changed file names to the -c command
    var listChanges: Int = -1         // -l/-L  diff lines allowed in the message; -1 off, 0 unlimited
    var listChangesColor: Bool = true // false once -L appears; -l never restores it, as upstream
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
        let parsed = parse(args, invokedFrom: FileManager.default.currentDirectoryPath)
        return (parsed.spec, parsed.error)
    }

    static func parse(_ args: [String], invokedFrom directory: String)
        -> (spec: RepoSpec?, error: String?, resolved: [String]) {
        var spec = RepoSpec(path: "")
        var target: (token: String, index: Int)? = nil
        var i = 0
        func next() -> String? { i += 1; return i < args.count ? args[i] : nil }

        while i < args.count {
            let a = args[i]
            switch a {
            case "-s":
                guard let v = next(), let d = Double(v), d >= 0 else {
                    return (nil, "-s needs a number of seconds, 0 or more", args)
                }
                spec.settle = d
            case "-d": if let v = next() { spec.dateFormat = v }
            case "-r", "-p": if let v = next() { spec.remote = v } // -p: upstream's alias of -r
            case "-b": if let v = next() { spec.branch = v }
            case "-R": spec.rebase = true
            case "-m": if let v = next() { spec.message = v }
            case "-c": if let v = next() { spec.commitCommand = v }
            case "-C": spec.pipeChangedFiles = true // boolean: takes no argument
            case "-l", "-L":
                guard let v = next(), let n = Int(v), n >= 0 else {
                    return (nil, "\(a) needs a number of lines, 0 or more", args)
                }
                spec.listChanges = n
                if a == "-L" { spec.listChangesColor = false }
            case "-x":
                guard let v = next(), (try? NSRegularExpression(pattern: v)) != nil else {
                    return (nil, "-x needs a valid regular expression", args)
                }
                spec.exclude = v
            case "-M": spec.noMergeCommit = true
            case "-f": spec.commitOnStart = true
            case "-g": if let v = next() { spec.gitDir = v }
            case "-e": _ = next() // inotify events: accepted, no-op on macOS (as upstream)
            case "-v": break // verbose: accepted, no-op (no daemon equivalent to gitwatch's set -x)
            case "--paused": spec.paused = true // gitwatchd extension (see RepoSpec)
            default:
                if a.hasPrefix("-") {
                    return (nil, "unknown flag \(a)", args)
                } else {
                    target = (a, i) // last bare arg wins as the target
                }
            }
            i += 1
        }

        guard let target else { return (nil, "no target path given", args) }
        spec.path = resolve(target.token, invokedFrom: directory)
        var resolved = args
        resolved[target.index] = spec.path
        return (spec, nil, resolved)
    }

    /// The absolute path a target token names, with a relative one taken from
    /// `directory`: the shell's working directory, never the daemon's.
    static func resolve(_ target: String, invokedFrom directory: String) -> String {
        var path = (target as NSString).expandingTildeInPath
        if !(path as NSString).isAbsolutePath {
            path = (directory as NSString).appendingPathComponent(path)
        }
        return (path as NSString).standardizingPath
    }

    /// The -d value goes to date(1) verbatim, as upstream: formats need a
    /// leading "+", and a bad format splices an empty string.
    static func formattedDate(_ fmt: String) -> String {
        let r = runProcess("/bin/date", [fmt])
        return r.code == 0 ? r.out : ""
    }
}
