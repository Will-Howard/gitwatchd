# gitwatchd

An always-on macOS menu-bar daemon that watches Git repos and auto-commits (and
optionally pushes) changes on a debounce — gitwatch's behaviour, made permanent.
No Dock icon; a small CLI drives it. Standalone: native FSEvents + `git`, no
`gitwatch`/`fswatch` dependency.

> Status: local dev / self-demo, with a working local install. Homebrew-cask
> packaging is parked. See `DESIGN.md` for rationale and decisions.

## Requirements
- macOS 13+ (Ventura), Apple Silicon or Intel
- The Xcode **command-line tools** for `swiftc` (`xcode-select --install`) — you
  never open Xcode.app itself.

## Build / run — three tiers
```sh
# Tier 1 — dev loop (runs from build/, never touches /Applications or login items)
make run       # build build/gitwatchd.app and (re)launch it
make stop      # kill the running instance
make clean     # remove build artifacts

# Tier 2 — local install (no sudo)
make install   # → /Applications (or ~/Applications) + CLI on PATH, launches it
make uninstall # remove app, CLI link, login item, and first-run state

# Tier 3 — distribution (needs a paid Apple Developer account)
make sign-release DEV_ID="Developer ID Application: NAME (TEAMID)"
make notarize     NOTARY_PROFILE=<notarytool keychain profile>
```
Edit a file under `Sources/`, run `make run`, and the freshly built app relaunches.
There's no `.xcodeproj`: `make` compiles `Sources/*.swift` with `swiftc` and
hand-assembles the `.app` bundle (`Resources/Info.plist` sets `LSUIElement` so it's
menu-bar-only). For a from-scratch build: `make clean && make run`.

**Dev vs install:** `make run` deliberately runs from `build/` and never registers a
login item, so menu-item changes are one `make run` away and never masked by a stale
install. `make install` is the real thing: it copies to `/Applications`, and on the
**first launch of an installed copy** the daemon registers itself for launch-at-login
(always-on is the point). That's one-time, gated by a sentinel in
`~/Library/Application Support/gitwatchd/`, so if you later turn it off it stays off.
`make uninstall` clears that sentinel too, so you can cleanly re-test onboarding.

## Manually testing it
```sh
BIN=build/gitwatchd.app/Contents/MacOS/gitwatchd

# 1. watch a repo (gitwatch-compatible flags; see below)
$BIN /path/to/a/git/repo          # or: $BIN -s 5 -r origin -b main /path/...
# 2. edit a file in that repo, wait ~2s, and it auto-commits
# 3. click the menu-bar icon: repos, per-repo Pause / Copy Path / Open in Finder
$BIN ls                           # list watched repos + status
$BIN rm <name|path>               # stop watching
```
The menu-bar app and the CLI are the **same binary**: launched as the `.app` (or
`gitwatchd serve`) it's the daemon; run with args it's the CLI. Config lives at
`~/.config/gitwatchd/repos.txt` — one gitwatch-style argument line per repo,
hand-editable; the daemon live-reloads on change.

## The environment gotcha (why it "works in terminal but not from the app")
A login-launched daemon inherits only a minimal environment (no Homebrew `PATH`, no
`~/.zshrc` exports), so it can pick a different `git` and miss your SSH/credential
setup. The daemon avoids this by re-deriving your real login-shell environment at
startup (the same trick VS Code uses), so it uses the same `git` and SSH agent your
terminal does. Inject custom auth via an optional `~/.config/gitwatchd/env.sh`.

If that env capture ever breaks, the daemon says so — a `⚠ shell environment failed
to load` item appears in the menu (and it's logged). To see what the daemon resolves:
```sh
$BIN doctor    # internal diagnostic: git binary, PATH, SSH agent keys, env.sh
```

## Source layout
| File | Responsibility |
|---|---|
| `main.swift` | Entry point (CLI vs daemon dispatch) + menu-bar `AppDelegate` |
| `CLI.swift` | `gitwatchd` subcommands |
| `Config.swift` | `repos.txt` read/write, gitwatch-line tokenizer |
| `RepoSpec.swift` | gitwatch flag parsing → `RepoSpec` |
| `RepoWatcher.swift` | FSEvents watch + debounce + `.git`/exclude filtering |
| `Git.swift` | `git` invocation: add/commit/push/rebase |
| `GitRuntime.swift` | login-shell env capture + git discovery |
| `LaunchAtLogin.swift` | `SMAppService` launch-at-login toggle |
