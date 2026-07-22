# gitwatchd for Linux

Daemon that watches git repos and auto-commits (and optionally pushes) every change. Same commands, same config file, and same [gitwatch](https://github.com/gitwatch/gitwatch) semantics as the macOS version, so you can ssh between a Mac and a Linux box and use gitwatchd the same way on both.

One static binary, no runtime dependencies beyond git.

## Install

From this directory:

    make build          # or: make build-arm64
    ./install.sh        # installs to ~/.local/bin

`./install.sh /usr/local/bin` (with sudo) installs system-wide instead.

## Usage

Watch a repo:

    gitwatchd .

Every change is now committed automatically (debounced, so a burst of writes lands as one commit). Add a remote to push each commit too:

    gitwatchd -r origin .

For something like a notes vault synced across machines, add `-R` to pull and rebase before each push:

    gitwatchd -r origin -b main -R .

Manage what's being watched:

    gitwatchd status
    gitwatchd pause blog      # by name or path
    gitwatchd resume blog
    gitwatchd rm blog

The set of repos lives in `~/.gitwatchd`, one gitwatch-style line per repo. Edit it directly if you prefer; the daemon reloads it live. `gitwatchd help` lists all flags.

## Running as a daemon

    gitwatchd autostart on

installs a systemd user unit (`~/.config/systemd/user/gitwatchd.service`), enables and starts it, and turns on lingering so the daemon survives logout and reboots. Logs go to the journal:

    journalctl --user -u gitwatchd

`gitwatchd start` / `gitwatchd stop` control the daemon either way; with the unit installed they go through systemctl. On a machine without systemd, `autostart on` explains itself and exits nonzero; use `gitwatchd start`, which runs the daemon detached and logs to `~/.local/state/gitwatchd/daemon.log`.

## Uninstall

    gitwatchd autostart off
    gitwatchd stop
    rm ~/.local/bin/gitwatchd ~/.gitwatchd
    rm -rf ~/.local/state/gitwatchd

## Credit and license

gitwatchd is a daemon reimplementation of [gitwatch](https://github.com/gitwatch/gitwatch) by Patrick Lehner and contributors. Free software under the GNU GPL v3.0, same as gitwatch.
