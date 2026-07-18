# gitwatchd

Auto-commit and sync your git repos, from the macOS menu bar.

[gitwatch](https://github.com/gitwatch/gitwatch) is a great idea: watch a repo,
auto-commit every change, optionally push it. The flaw is that you have to leave
it running in a terminal somewhere, which kills the utility of just trusting
that it's always on. gitwatchd is the same idea as a proper Mac citizen: a
menu-bar daemon that starts at login, watches your repos in the background, and
shows you what it's doing (and what's failing). If you're comfortable with git,
it covers a lot of what people use Dropbox for.

## Install

Requires macOS 13+ and the Xcode command-line tools (`xcode-select --install`).
You never open Xcode.

```sh
git clone https://github.com/Will-Howard/gitwatchd.git
cd gitwatchd
make install
```

That builds the app, puts it in /Applications, puts the `gitwatchd` CLI on your
PATH, and launches it. It registers itself to start at login, because always-on
is the point; turn that off in the menu if you don't want it. `make uninstall`
removes everything.

macOS will ask once for permission to access the folders your repos live in.

## Use

```sh
cd ~/code/my-notes
gitwatchd -r origin -b main .
```

Edit a file, wait a couple of seconds, and it's committed and pushed. The
menu-bar icon shows every watched repo, what's pending, and what's failing; the
icon changes when something needs your attention. `gitwatchd help` covers the
rest: pause/resume, excludes, and `-R` for pull-rebase-before-push when more
than one machine syncs to the same branch.

Repos live in `~/.config/gitwatchd/repos.txt`, one line per repo, same flags as
the CLI. Edit it by hand if you like (`gitwatchd config edit` opens it in your
git editor); the daemon picks up changes live.

## What it does to your repo, honestly

- It commits everything that isn't gitignored, on a debounce. If your
  `.gitignore` is sloppy, that includes secrets and half-finished work. Use it
  on repos where "commit everything, often" is what you actually want.
- It never force-pushes and never resolves conflicts. If a rebase hits a
  conflict it stops, flags it in the menu, and leaves the repo for you to fix.
- If a push fails (offline, server down), the commit stays local and gitwatchd
  retries with backoff, and immediately when the network comes back.

## If pushes work in your terminal but not from the app

A login-launched app doesn't get your shell environment, which normally breaks
SSH keys and credential helpers. gitwatchd re-derives your login shell's
environment at startup (the same trick VS Code uses), so the daemon pushes with
the same git and SSH agent your terminal uses. If something is still off,
`gitwatchd doctor` shows exactly what the daemon sees.

## Credit and license

gitwatchd is a from-scratch Swift reimplementation of
[gitwatch](https://github.com/gitwatch/gitwatch) by Patrick Lehner and
contributors. The git behaviour is intended to match gitwatch exactly, and is
tested differentially against it. GPL-3.0, like the original.
