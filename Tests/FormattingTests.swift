import Foundation
import Testing

// What the menu shows, asserted as exact strings. rowTitle feeds the main-menu
// row; the headline/retry/truncation helpers feed a failing repo's submenu.

@Suite("Menu row titles")
struct MenuRowTitles {

    @Test("a healthy repo row is just name and branch")
    func healthy() {
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: false,
                                      pending: 0, error: nil)
                == "notes · main")
    }

    @Test("pending edits show a count")
    func pending() {
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: false,
                                      pending: 3, error: nil)
                == "notes · main · ✎ 3 pending")
    }

    @Test("a failing push flags the row; detail stays in the submenu")
    func pushFailing() {
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: false,
                                      pending: 0, error: .pushFailed(detail: "x"))
                == "notes · main · ⚠ push failing")
    }

    @Test("rebase conflicts and commit failures get their own labels")
    func otherFailureLabels() {
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: false,
                                      pending: 0, error: .rebaseConflict(detail: "x"))
                == "notes · main · ⚠ rebase conflict")
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: false,
                                      pending: 0, error: .commitFailed(detail: "x"))
                == "notes · main · ⚠ commit failing")
    }

    @Test("a config entry that can't be watched is flagged with a short reason, no path")
    func configError() {
        #expect(StatusFormat.configErrorRow(label: "demo-repo", reason: "repo not found")
                == "⚠ demo-repo · repo not found")
        let longLabel = String(repeating: "y", count: 100)
        #expect(StatusFormat.configErrorRow(label: longLabel, reason: "not a git repo").count == 48,
                "rows stay fixed width; the full path lives in the submenu")
    }

    @Test("paused beats every other tail")
    func pausedWins() {
        #expect(StatusFormat.rowTitle(name: "notes", branch: "main", paused: true,
                                      pending: 3, error: .pushFailed(detail: "x"))
                == "notes · main · ⏸ paused")
    }
}

@Suite("CLI ls state column")
struct CLIStateColumn {

    @Test("the same vocabulary as the menu: missing, paused, pending, idle")
    func cliStates() {
        #expect(StatusFormat.cliState(ok: false, paused: false, pending: 0) == "⚠ missing")
        #expect(StatusFormat.cliState(ok: true, paused: true, pending: 3) == "⏸ paused")
        #expect(StatusFormat.cliState(ok: true, paused: false, pending: 3) == "✎ 3 pending")
        #expect(StatusFormat.cliState(ok: true, paused: false, pending: 0) == "✓ idle")
    }
}

@Suite("Failing repo submenu")
struct FailingRepoSubmenu {

    @Test("the headline counts consecutive attempts")
    func headline() {
        #expect(StatusFormat.errorHeadline(label: "push failing", attempts: 1)
                == "⚠ push failing")
        #expect(StatusFormat.errorHeadline(label: "push failing", attempts: 4)
                == "⚠ push failing (4 attempts)")
    }

    @Test("the retry line says when we tried and when we will again")
    func retryLine() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        #expect(StatusFormat.retryLine(lastTried: now.addingTimeInterval(-120),
                                       nextRetry: now.addingTimeInterval(180), now: now)
                == "tried 2m ago · retrying in 3m")
    }

    @Test("without an automatic retry it says what will trigger one")
    func retryLineWithoutTimer() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        #expect(StatusFormat.retryLine(lastTried: now.addingTimeInterval(-30),
                                       nextRetry: nil, now: now)
                == "tried 30s ago · retries on next change")
    }

    @Test("long git errors are clipped to menu width")
    func truncation() {
        let long = String(repeating: "x", count: 100)
        #expect(StatusFormat.truncated(long, max: 20).count == 20)
        #expect(StatusFormat.truncated("short", max: 20) == "short")
    }
}

@Suite("Push retry backoff")
struct PushRetryBackoff {

    @Test("retries back off 30s, 60s, 120s, 240s, then hold at 5 minutes")
    func schedule() {
        #expect([1, 2, 3, 4, 5, 6].map(Backoff.delay(afterFailures:))
                == [30, 60, 120, 240, 300, 300])
    }
}

@Suite("Git error summaries")
struct GitErrorSummaries {

    @Test("the rejection line is picked out of noisy push output")
    func rejectionLine() {
        let out = """
        To /tmp/origin.git
         ! [rejected]        main -> main (fetch first)
        error: failed to push some refs to '/tmp/origin.git'
        hint: Updates were rejected because the remote contains work that you do not have
        """
        #expect(Git.errorSummary(out) == "! [rejected]        main -> main (fetch first)")
    }

    @Test("fatal lines win over surrounding chatter")
    func fatalLine() {
        let out = "fatal: unable to access 'https://x/': Could not resolve host\nsome trailer"
        #expect(Git.errorSummary(out)
                == "fatal: unable to access 'https://x/': Could not resolve host")
    }

    @Test("plain hook chatter falls back to the last line")
    func lastLineFallback() {
        #expect(Git.errorSummary("lint says no") == "lint says no")
    }
}
