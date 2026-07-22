import Foundation
import Testing

// The help text states flag defaults and exclusion behaviour as facts;
// these tests pin them so the help can't silently drift from the code.

@Suite("Flag defaults")
struct FlagDefaults {

    @Test("a bare path gets exactly the documented defaults")
    func defaults() {
        let spec = RepoSpecParser.parse(["/tmp/x"]).spec!
        #expect(spec.settle == 2)                                // -s "Default: 2"
        #expect(spec.remote == nil)                              // -r "Default: no push"
        #expect(spec.message == "gitwatchd auto-commit (%d)")    // -m default
        #expect(spec.dateFormat == "+%Y-%m-%d %H:%M:%S")         // -d default
        #expect(spec.branch == nil)
        #expect(spec.rebase == false)
        #expect(spec.exclude == nil)
        #expect(spec.noMergeCommit == false)
        #expect(spec.commitOnStart == false, "-f is opt-in: a deliberate manual state is not flushed")
        #expect(spec.paused == false)
    }

    @Test("-p is an alias of -r")
    func pAlias() {
        #expect(RepoSpecParser.parse(["-p", "origin", "/tmp/x"]).spec?.remote == "origin")
    }

    @Test("-s rejects negative and non-numeric values instead of ignoring them")
    func settleValidation() {
        #expect(RepoSpecParser.parse(["-s", "-1", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-s", "soon", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-s", "0", "/tmp/x"]).spec?.settle == 0)
    }
}

@Suite("Exclusions (-x)")
struct Exclusions {

    @Test("the regex matches anywhere in the path")
    func regexMatch() {
        let spec = RepoSpecParser.parse(["-x", "\\.log$", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/deep/dir/debug.log"))
        #expect(!spec.excludes("/tmp/x/log.txt"))
    }

    @Test("a directory pattern excludes a subtree")
    func subtreePattern() {
        let spec = RepoSpecParser.parse(["-x", "build/", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/build/out.o"))
        #expect(!spec.excludes("/tmp/x/src/out.o"))
    }

    @Test("the last -x wins")
    func lastWins() {
        let spec = RepoSpecParser.parse(["-x", "\\.log$", "-x", "\\.tmp$", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/b.tmp"))
        #expect(!spec.excludes("/tmp/x/a.log"))
    }

    @Test("an invalid regex is a parse error, not a silent no-op")
    func invalidPattern() {
        #expect(RepoSpecParser.parse(["-x", "*.log", "/tmp/x"]).error != nil)
    }

    @Test("without -x nothing is excluded")
    func none() {
        let spec = RepoSpecParser.parse(["/tmp/x"]).spec!
        #expect(!spec.excludes("/tmp/x/anything.log"))
    }
}
