# Changelog

## v0.1.0 (2026-07-19)

Initial release.

- Menu-bar daemon that watches git repos and auto-commits on a debounce, with
  optional push, pull-rebase-before-push (`-R`), excludes, and per-repo pause
- Git behaviour matches upstream gitwatch, verified by differential tests
  against the vendored script
- Push failures retry with backoff, and immediately when the network returns;
  errors surface in the menu bar, and `gitwatchd status` mirrors the menu in
  the terminal
- Notarized Developer ID build, installable via Homebrew cask
