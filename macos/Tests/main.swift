import Darwin
import Testing

// Entry point for `make test`: hand the process to Swift Testing's runner,
// which discovers every @Test function in this binary automatically and runs
// them in parallel. (__swiftPMEntryPoint is the same runner `swift test` uses;
// underscored but public. If a future toolchain renames it, this is the one
// line to fix.) The temp root holding the throwaway repos is removed at exit.

atexit { TestDirs.cleanup() }
exit(await Testing.__swiftPMEntryPoint())
