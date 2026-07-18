import Foundation
import Testing

// The help text states flag defaults and exclusion behaviour as facts;
// these tests pin them so the help can't silently drift from the code.

@Suite("Flag defaults, as the help promises")
struct FlagDefaults {

    @Test("a bare path gets exactly the documented defaults")
    func defaults() {
        let spec = RepoSpecParser.parse(["/tmp/x"]).spec!
        #expect(spec.settle == 2)                                // -s "Default: 2"
        #expect(spec.remote == nil)                              // -r "Default: no push"
        #expect(spec.message == "gitwatchd auto-commit (%d)")    // -m default
        #expect(spec.dateFormat == "%Y-%m-%d %H:%M:%S")          // -d default
        #expect(spec.branch == nil)
        #expect(spec.rebase == false)
        #expect(spec.exclude.isEmpty)
        #expect(spec.noMergeCommit == false)
        #expect(spec.paused == false)
    }

    @Test("-p is accepted as an alias of -r, as upstream treats it")
    func pAlias() {
        #expect(RepoSpecParser.parse(["-p", "origin", "/tmp/x"]).spec?.remote == "origin")
    }

    @Test("-l and -L are rejected, not silently ignored")
    func logFlagsRejected() {
        #expect(RepoSpecParser.parse(["-l", "5", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-L", "5", "/tmp/x"]).error != nil)
    }
}

@Suite("Exclusions (-x), as the help promises")
struct Exclusions {

    @Test("a glob excludes matching file names anywhere in the repo")
    func nameGlob() {
        let spec = RepoSpecParser.parse(["-x", "*.log", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/deep/dir/debug.log"))
        #expect(!spec.excludes("/tmp/x/notes.txt"))
    }

    @Test("-x is repeatable and every pattern stays live")
    func repeatable() {
        let spec = RepoSpecParser.parse(["-x", "*.log", "-x", "*.tmp", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/a.log"))
        #expect(spec.excludes("/tmp/x/b.tmp"))
        #expect(!spec.excludes("/tmp/x/c.txt"))
    }

    @Test("a full-path pattern excludes a subtree")
    func fullPathPattern() {
        let spec = RepoSpecParser.parse(["-x", "/tmp/x/build/*", "/tmp/x"]).spec!
        #expect(spec.excludes("/tmp/x/build/out.o"))
        #expect(!spec.excludes("/tmp/x/src/out.o"))
    }

    @Test("without -x nothing is excluded")
    func none() {
        let spec = RepoSpecParser.parse(["/tmp/x"]).spec!
        #expect(!spec.excludes("/tmp/x/anything.log"))
    }
}
