import Foundation

// Pure string formatting for the menu. No AppKit in here, so `make test` can
// assert exactly what the user will read.
enum StatusFormat {
    /// One status tail at most: paused wins over errors, errors over pending.
    static func rowTitle(name: String, branch: String, paused: Bool,
                         pending: Int, errorLabel: String?) -> String {
        let base = "\(name) · \(branch)"
        if paused { return base + " · ⏸ paused" }
        if let errorLabel { return base + " · ⚠ " + errorLabel }
        if pending == 1 { return base + " · 1 pending change" }
        if pending > 1 { return base + " · \(pending) pending changes" }
        return base
    }

    /// Fixed-width row; the full path lives in the row's submenu.
    static func configErrorRow(label: String, reason: String) -> String {
        truncated("⚠ \(label) · \(reason)", max: 48)
    }

    static func errorHeadline(label: String, attempts: Int) -> String {
        attempts > 1 ? "⚠ \(label) (\(attempts) attempts)" : "⚠ \(label)"
    }

    static func retryLine(lastTried: Date, nextRetry: Date?, now: Date) -> String {
        let tried = "tried " + ago(now.timeIntervalSince(lastTried))
        guard let nextRetry else { return tried + " · retries on next change" }
        let dt = nextRetry.timeIntervalSince(now)
        return dt <= 1 ? tried + " · retrying now" : tried + " · retrying in " + span(dt)
    }

    static func truncated(_ s: String, max: Int = 60) -> String {
        s.count <= max ? s : String(s.prefix(max - 1)) + "…"
    }

    /// "just now", "40s ago", "5m ago", "3h ago", "2d ago".
    static func ago(_ seconds: TimeInterval) -> String {
        seconds < 5 ? "just now" : span(seconds) + " ago"
    }

    static func span(_ seconds: TimeInterval) -> String {
        let s = Swift.max(1, Int(seconds.rounded()))
        if s < 90 { return "\(s)s" }
        if s < 90 * 60 { return "\(Int((Double(s) / 60).rounded()))m" }
        if s < 36 * 3600 { return "\(Int((Double(s) / 3600).rounded()))h" }
        return "\(Int((Double(s) / 86400).rounded()))d"
    }
}
