import Foundation
import Testing

// Pause is config-level state: a --paused token on the repo's line, so a
// paused repo stays paused across daemon and computer restarts. These tests
// pin the parsing and the pure line-rewrite the menu toggle uses.

@Suite("Pause is config-level state (--paused)")
struct PausedConfig {

    @Test("--paused parses into the spec alongside gitwatch flags")
    func parses() {
        let (spec, err) = RepoSpecParser.parse(["--paused", "-s", "2", "/tmp/x"])
        #expect(err == nil)
        #expect(spec?.paused == true)
        #expect(spec?.settle == 2)
    }

    @Test("pausing rewrites the line; resuming restores it")
    func roundTrip() {
        let line = "-s 2 -r origin -b main -R /tmp/x"
        let pausedLine = Config.togglingPaused(line: line, path: "/tmp/x", paused: true)
        #expect(pausedLine == "--paused -s 2 -r origin -b main -R /tmp/x")
        let resumed = Config.togglingPaused(line: pausedLine!, path: "/tmp/x", paused: false)
        #expect(resumed == line)
    }

    @Test("quoted commit messages survive the rewrite")
    func quoting() {
        #expect(Config.togglingPaused(line: "-m \"two words\" /tmp/x", path: "/tmp/x", paused: true)
                == "--paused -m \"two words\" /tmp/x")
    }

    @Test("lines for other repos are left alone")
    func otherLines() {
        #expect(Config.togglingPaused(line: "-s 2 /tmp/other", path: "/tmp/x", paused: true) == nil)
    }

    @Test("a no-op toggle (already in the wanted state) changes nothing")
    func noOp() {
        #expect(Config.togglingPaused(line: "--paused /tmp/x", path: "/tmp/x", paused: true) == nil)
        #expect(Config.togglingPaused(line: "/tmp/x", path: "/tmp/x", paused: false) == nil)
    }
}
