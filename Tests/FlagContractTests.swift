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
        #expect(spec.commitCommand == nil)                       // -c "Overrides -m and -d"
        #expect(spec.pipeChangedFiles == false)                  // -C off by default
        #expect(spec.branch == nil)
        #expect(spec.rebase == false)
        #expect(spec.exclude == nil)
        #expect(spec.noMergeCommit == false)
        #expect(spec.listChanges == -1, "-l/-L off by default; the -m message is used")
        #expect(spec.listChangesColor == true)
        #expect(spec.commitOnStart == false, "-f is opt-in: a deliberate manual state is not flushed")
        #expect(spec.paused == false)
    }

    @Test("-p is an alias of -r")
    func pAlias() {
        #expect(RepoSpecParser.parse(["-p", "origin", "/tmp/x"]).spec?.remote == "origin")
    }

    @Test("-v and -e are accepted for gitwatch parity, then ignored")
    func acceptedNoOps() {
        #expect(RepoSpecParser.parse(["-v", "/tmp/x"]).spec?.path == "/tmp/x")
        let e = RepoSpecParser.parse(["-e", "modify,delete", "/tmp/x"])
        #expect(e.spec?.path == "/tmp/x", "-e consumes its value and does not swallow the target")
    }

    @Test("-c stores the command string verbatim; -C flags the pipe")
    func messageCommand() {
        let spec = RepoSpecParser.parse(["-c", "echo a b", "-C", "/tmp/x"]).spec!
        #expect(spec.commitCommand == "echo a b")
        #expect(spec.pipeChangedFiles)
    }

    @Test("-C consumes no argument: the next token parses as its own flag")
    func pipeFlagIsBoolean() {
        let spec = RepoSpecParser.parse(["-C", "-r", "origin", "/tmp/x"]).spec!
        #expect(spec.pipeChangedFiles)
        #expect(spec.remote == "origin")
        #expect(spec.path == "/tmp/x")
    }

    @Test("-s rejects negative and non-numeric values instead of ignoring them")
    func settleValidation() {
        #expect(RepoSpecParser.parse(["-s", "-1", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-s", "soon", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-s", "0", "/tmp/x"]).spec?.settle == 0)
    }

    @Test("-l sets the list-changes cap and keeps colour")
    func listChangesColoured() {
        let spec = RepoSpecParser.parse(["-l", "10", "/tmp/x"]).spec!
        #expect(spec.listChanges == 10)
        #expect(spec.listChangesColor == true)
    }

    @Test("-L sets the cap and switches the diff to plain")
    func listChangesPlain() {
        let spec = RepoSpecParser.parse(["-L", "10", "/tmp/x"]).spec!
        #expect(spec.listChanges == 10)
        #expect(spec.listChangesColor == false)
    }

    @Test("-L then -l stays plain: upstream's -l never restores colour")
    func colourNeverRestored() {
        let spec = RepoSpecParser.parse(["-L", "5", "-l", "3", "/tmp/x"]).spec!
        #expect(spec.listChanges == 3, "the line cap itself is last-wins")
        #expect(spec.listChangesColor == false)
    }

    @Test("-l and -L reject non-numeric and negative line counts")
    func listChangesValidation() {
        #expect(RepoSpecParser.parse(["-l", "many", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-L", "-1", "/tmp/x"]).error != nil)
        #expect(RepoSpecParser.parse(["-l", "0", "/tmp/x"]).spec?.listChanges == 0)
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
