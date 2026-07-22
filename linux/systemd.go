package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Autostart = a systemd user unit, the standard way for a per-user daemon to
// survive reboots and headless boots (with lingering). When systemd is absent
// we say so and do nothing: no shell-profile edits, ever.

func unitPath() string {
	return filepath.Join(homeDir(), ".config", "systemd", "user", "gitwatchd.service")
}

func systemctlPresent() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func unitInstalled() bool {
	_, err := os.Stat(unitPath())
	return err == nil
}

func systemctlUser(args ...string) (int, string) {
	return runCommand("systemctl", append([]string{"--user"}, args...), "")
}

func unitActive() bool {
	_, out := systemctlUser("is-active", "gitwatchd")
	return out == "active"
}

func autostartOn() int {
	if !systemctlPresent() {
		warn("systemd not found: autostart needs a systemd user session.\n" +
			"  run the daemon manually instead: gitwatchd start")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		warn("cannot resolve the gitwatchd binary path: " + err.Error())
		return 1
	}
	exe, _ = filepath.EvalSymlinks(exe)
	unit := fmt.Sprintf(`[Unit]
Description=gitwatchd: watch git repos and auto-commit changes

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		warn("cannot create " + filepath.Dir(unitPath()) + ": " + err.Error())
		return 1
	}
	if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
		warn("cannot write " + unitPath() + ": " + err.Error())
		return 1
	}
	systemctlUser("daemon-reload")
	if code, out := systemctlUser("enable", "--now", "gitwatchd"); code != 0 {
		warn("systemctl --user enable --now gitwatchd failed: " + out)
		return 1
	}
	// Lingering keeps the user manager (and the daemon) alive without an
	// open session: headless boots, logged-out laptops.
	if code, out := runCommand("loginctl", []string{"enable-linger", os.Getenv("USER")}, ""); code != 0 {
		fmt.Println("✓ autostart: on (systemd user unit enabled)")
		fmt.Println("  note: loginctl enable-linger failed (" + strings.TrimSpace(out) + ")")
		fmt.Println("  without lingering the daemon stops when you log out")
		return 0
	}
	fmt.Println("✓ autostart: on (systemd user unit enabled, survives logout and reboot)")
	return 0
}

func autostartOff() int {
	if !systemctlPresent() {
		warn("systemd not found: nothing to turn off (autostart was never installed)")
		return 1
	}
	if !unitInstalled() {
		fmt.Println("autostart: already off")
		return 0
	}
	systemctlUser("disable", "--now", "gitwatchd")
	os.Remove(unitPath())
	systemctlUser("daemon-reload")
	fmt.Println("✓ autostart: off")
	return 0
}

func autostartStatus() int {
	if !systemctlPresent() {
		fmt.Println("autostart: unavailable (systemd not found); run the daemon with: gitwatchd start")
		return 0
	}
	if !unitInstalled() {
		fmt.Println("autostart: off")
		return 0
	}
	_, enabled := systemctlUser("is-enabled", "gitwatchd")
	state := "on"
	if enabled != "enabled" {
		state = "installed but " + enabled
	}
	if unitActive() {
		fmt.Printf("autostart: %s (daemon running)\n", state)
	} else {
		fmt.Printf("autostart: %s (daemon not running)\n", state)
	}
	return 0
}
