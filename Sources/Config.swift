import Foundation

// Config = a list of gitwatch-style argument lines, one repo per line.
// Hand-editable and CLI-writable; the CLI appends exactly what the user typed.
enum Config {
    /// Tests point this at a scratch directory; nil means the real location.
    static var overrideDir: String?

    static var dir: String {
        overrideDir ?? (NSHomeDirectory() as NSString).appendingPathComponent(".config/gitwatchd")
    }
    static var path: String {
        // .txt (not .conf) so "Open Config File" launches a sensible default app.
        (dir as NSString).appendingPathComponent("repos.txt")
    }

    static func ensureExists() {
        let fm = FileManager.default
        try? fm.createDirectory(atPath: dir, withIntermediateDirectories: true)
        guard !fm.fileExists(atPath: path) else { return }
        let template = """
        # gitwatchd: one repo per line.
        #   [-s secs] [-r remote [-b branch]] [-R] [-m msg] [-x pattern] [-M] [--paused] <path>
        # `gitwatchd help` explains each flag. Examples:
        #   ~/code/my-notes
        #   -s 5 -r origin -b main ~/code/blog
        # (from a terminal, `gitwatchd .` adds the current repo here for you)
        """
        try? template.write(toFile: path, atomically: true, encoding: .utf8)
    }

    /// Non-comment, non-empty raw lines.
    static func rawLines() -> [String] {
        guard let text = try? String(contentsOfFile: path, encoding: .utf8) else { return [] }
        return text.split(separator: "\n", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("#") }
    }

    /// Parsed specs (invalid lines are skipped; surface them via lineErrors).
    static func specs() -> [RepoSpec] {
        rawLines().compactMap { line in
            RepoSpecParser.parse(tokenize(line), raw: line).spec
        }
    }

    /// Config lines that don't parse into a spec at all, with the reason.
    static func lineErrors() -> [(line: String, error: String)] {
        rawLines().compactMap { line in
            let (spec, err) = RepoSpecParser.parse(tokenize(line), raw: line)
            return spec == nil ? (line, err ?? "unparseable line") : nil
        }
    }

    /// Append a repo line (raw gitwatch args). Creates the file if needed.
    static func append(_ line: String) {
        ensureExists()
        var text = (try? String(contentsOfFile: path, encoding: .utf8)) ?? ""
        if !text.isEmpty && !text.hasSuffix("\n") { text += "\n" }
        text += line + "\n"
        try? text.write(toFile: path, atomically: true, encoding: .utf8)
    }

    /// Remove any line whose parsed target path matches (by full path or basename).
    /// Returns the number of lines removed.
    @discardableResult
    static func remove(matching needle: String) -> Int {
        guard let text = try? String(contentsOfFile: path, encoding: .utf8) else { return 0 }
        let want = (needle as NSString).expandingTildeInPath
        var removed = 0
        let kept = text.split(separator: "\n", omittingEmptySubsequences: false).filter { rawSub in
            let line = String(rawSub).trimmingCharacters(in: .whitespaces)
            if line.isEmpty || line.hasPrefix("#") { return true }
            guard let spec = RepoSpecParser.parse(tokenize(line), raw: line).spec else { return true }
            let match = spec.path == want || spec.name == needle || spec.path == needle
            if match { removed += 1 }
            return !match
        }
        try? kept.joined(separator: "\n").write(toFile: path, atomically: true, encoding: .utf8)
        return removed
    }

    /// Flip the --paused token on config lines matching `needle` (by full
    /// path or repo name, like remove). Pause lives in the config, not app
    /// state, so it survives daemon and computer restarts. Returns the number
    /// of lines changed.
    @discardableResult
    static func setPaused(matching needle: String, paused: Bool) -> Int {
        guard let text = try? String(contentsOfFile: path, encoding: .utf8) else { return 0 }
        let want = (needle as NSString).expandingTildeInPath
        var changed = 0
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false).map { sub -> String in
            let line = String(sub)
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#"),
                  let spec = RepoSpecParser.parse(tokenize(trimmed), raw: trimmed).spec,
                  spec.path == want || spec.name == needle || spec.path == needle,
                  let rewritten = togglingPaused(line: trimmed, path: spec.path, paused: paused)
            else { return line }
            changed += 1
            return rewritten
        }
        if changed > 0 {
            try? lines.joined(separator: "\n").write(toFile: path, atomically: true, encoding: .utf8)
        }
        return changed
    }

    /// The pure rewrite behind setPaused: if `line` watches `path` and its
    /// paused state differs, return the line with --paused added (in front)
    /// or removed; else nil for "leave this line alone".
    static func togglingPaused(line: String, path: String, paused: Bool) -> String? {
        let tokens = tokenize(line)
        guard let spec = RepoSpecParser.parse(tokens, raw: line).spec,
              spec.path == path, spec.paused != paused else { return nil }
        var kept = tokens.filter { $0 != "--paused" }
        if paused { kept.insert("--paused", at: 0) }
        return kept.map(quoteIfNeeded).joined(separator: " ")
    }

    /// Quote one argument for a config line (inverse of tokenize's quoting).
    static func quoteIfNeeded(_ s: String) -> String {
        s.contains(" ") ? "\"\(s)\"" : s
    }

    /// Split a config line into arguments on whitespace, respecting simple single
    /// or double quotes so that `-m "two words"` stays a single argument.
    static func tokenize(_ line: String) -> [String] {
        var args: [String] = []
        var current = ""
        var openQuote: Character? = nil   // the quote char we're inside, if any

        func finishArg() {
            if !current.isEmpty { args.append(current); current = "" }
        }

        for ch in line {
            if let quote = openQuote {
                // Inside quotes: the matching quote closes; everything else is literal.
                if ch == quote { openQuote = nil } else { current.append(ch) }
            } else if ch == "\"" || ch == "'" {
                openQuote = ch            // start a quoted section
            } else if ch.isWhitespace {
                finishArg()               // whitespace separates arguments
            } else {
                current.append(ch)
            }
        }
        finishArg()
        return args
    }
}
