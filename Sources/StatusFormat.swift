import Foundation

// Pure string formatting for the menu. No AppKit in here, so `make test` can
// assert exactly what the user will read.
enum StatusFormat {
    /// Main-menu row: "name · branch" plus at most one status tail.
    /// Paused wins over errors; errors win over the pending count. Error rows
    /// carry the tail only; the full story (detail, attempts, next retry) lives
    /// in the repo's submenu.
    static func rowTitle(name: String, branch: String, paused: Bool,
                         pending: Int, error: CommitOutcome?) -> String {
        let base = "\(name) · \(branch)"
        if paused { return base + " · ⏸ paused" }
        if let label = error?.errorLabel { return base + " · ⚠ " + label }
        if pending > 0 { return base + " · ✎ \(pending) pending" }
        return base
    }

    /// Menu row for a config entry that can't be watched at all (unparseable
    /// line, missing path, not a git repo). These must never disappear
    /// silently. Short fixed-width row (reasons are a fixed vocabulary, no
    /// paths); the full path lives in the row's submenu.
    static func configErrorRow(label: String, reason: String) -> String {
        truncated("⚠ \(label) · \(reason)", max: 48)
    }

    /// The CLI `ls` state column; same vocabulary as the menu-row tails.
    static func cliState(ok: Bool, paused: Bool, pending: Int) -> String {
        if !ok { return "⚠ missing" }
        if paused { return "⏸ paused" }
        if pending > 0 { return "✎ \(pending) pending" }
        return "✓ idle"
    }

    /// Submenu headline for a failing repo.
    static func errorHeadline(label: String, attempts: Int) -> String {
        attempts > 1 ? "⚠ \(label) (\(attempts) attempts)" : "⚠ \(label)"
    }

    /// Submenu line explaining when we last tried and what happens next.
    static func retryLine(lastTried: Date, nextRetry: Date?, now: Date) -> String {
        let tried = "tried " + ago(now.timeIntervalSince(lastTried))
        guard let nextRetry else { return tried + " · retries on next change" }
        let dt = nextRetry.timeIntervalSince(now)
        return dt <= 1 ? tried + " · retrying now" : tried + " · retrying in " + span(dt)
    }

    /// Clip an error detail to menu width.
    static func truncated(_ s: String, max: Int = 60) -> String {
        s.count <= max ? s : String(s.prefix(max - 1)) + "…"
    }

    /// "just now", "40s ago", "5m ago", "3h ago", "2d ago".
    static func ago(_ seconds: TimeInterval) -> String {
        seconds < 5 ? "just now" : span(seconds) + " ago"
    }

    /// A duration in the largest sensible unit.
    static func span(_ seconds: TimeInterval) -> String {
        let s = Swift.max(1, Int(seconds.rounded()))
        if s < 90 { return "\(s)s" }
        if s < 90 * 60 { return "\(Int((Double(s) / 60).rounded()))m" }
        if s < 36 * 3600 { return "\(Int((Double(s) / 3600).rounded()))h" }
        return "\(Int((Double(s) / 86400).rounded()))d"
    }
}

/// Retry backoff for failed pushes: an additive reliability layer on top of
/// gitwatch's fire-and-forget push (which never retries at all).
enum Backoff {
    static let first: TimeInterval = 30
    static let cap: TimeInterval = 300

    /// Delay before the next automatic retry after `n` consecutive failures:
    /// 30s, 60s, 120s, 240s, then hold at 5 minutes.
    static func delay(afterFailures n: Int) -> TimeInterval {
        guard n > 1 else { return first }
        return Swift.min(first * pow(2, Double(n - 1)), cap)
    }
}
