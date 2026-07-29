# gitwatchd for Linux: a single static Go binary.
#   make            build build/gitwatchd for the host platform
#   make test       go vet + go test
#   make install    put the binary on PATH and start the daemon (no sudo)
#   make uninstall  remove the binary, systemd unit and daemon state
#   make clean      remove build artifacts

GOFLAGS := -trimpath -ldflags "-s -w"
BUILD   := build

# Install destination; empty means the first writable of /usr/local/bin,
# ~/.local/bin, ~/bin. Override as `make install DEST=/custom/bin`.
DEST ?=

.PHONY: all build test install uninstall clean

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BUILD)/gitwatchd .

test:
	go vet ./...
	go test ./...

# --- local install / uninstall ---
# Sudo-free: binary onto PATH, then started from there, which is what turns
# autostart on for the first run of an installed copy.

install: build
	@$(BUILD)/gitwatchd stop >/dev/null 2>&1 || true
	@DEST="$(DEST)"; \
	if [ -n "$$DEST" ]; then mkdir -p "$$DEST"; else \
		for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
			mkdir -p "$$d" 2>/dev/null || true; \
			if [ -w "$$d" ]; then DEST="$$d"; break; fi; \
		done; \
	fi; \
	if [ -z "$$DEST" ]; then \
		echo "  ⚠ no writable bin dir found. Install by hand:  install -m 0755 $(BUILD)/gitwatchd /usr/local/bin/gitwatchd"; \
		exit 1; \
	fi; \
	install -m 0755 $(BUILD)/gitwatchd "$$DEST/gitwatchd"; \
	echo "✓ binary → $$DEST/gitwatchd"; \
	case ":$$PATH:" in *":$$DEST:"*) ;; \
		*) echo "  ⚠ $$DEST is not on your PATH. Add:  export PATH=\"$$DEST:\$$PATH\"";; esac; \
	"$$DEST/gitwatchd" start
	@echo "  Next:  gitwatchd .   to watch the current repo"

# Uninstall never trusts PATH: everything is found by absolute path.
uninstall:
	@for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		[ -x "$$d/gitwatchd" ] && "$$d/gitwatchd" autostart off >/dev/null 2>&1 || true; \
	done
	@UNIT="$$HOME/.config/systemd/user/gitwatchd.service"; \
	STATE="$${XDG_STATE_HOME:-$$HOME/.local/state}/gitwatchd"; GONE=""; \
	if command -v systemctl >/dev/null 2>&1; then \
		systemctl --user disable --now gitwatchd >/dev/null 2>&1 || true; \
	fi; \
	if [ -f "$$UNIT" ]; then \
		rm -f "$$UNIT"; GONE="systemd unit"; echo "  removed $$UNIT"; \
		command -v systemctl >/dev/null 2>&1 && systemctl --user daemon-reload >/dev/null 2>&1 || true; \
	fi; \
	PID=$$(cat "$$STATE/gitwatchd.pid" 2>/dev/null); \
	if [ -n "$$PID" ] && [ "$$(cat /proc/$$PID/comm 2>/dev/null)" = gitwatchd ]; then \
		kill "$$PID" 2>/dev/null || true; \
		for i in 1 2 3 4 5 6 7 8 9 10; do [ -d /proc/$$PID ] || break; sleep 0.2; done; \
		[ -d /proc/$$PID ] && kill -9 "$$PID" 2>/dev/null; \
		echo "  stopped daemon (pid $$PID)"; \
	fi; \
	BIN=""; \
	for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		[ -f "$$d/gitwatchd" ] && rm -f "$$d/gitwatchd" && BIN=1 && echo "  removed $$d/gitwatchd"; \
	done; \
	[ -n "$$BIN" ] && GONE="$${GONE:+$$GONE, }binary"; \
	[ -d "$$STATE" ] && rm -rf "$$STATE" && GONE="$${GONE:+$$GONE, }daemon state"; \
	if [ -n "$$GONE" ]; then echo "✓ uninstalled: $$GONE"; \
	else echo "✓ nothing to uninstall: no gitwatchd binary, unit or state found"; fi
	@echo "  (config at ~/.gitwatchd kept: 'rm ~/.gitwatchd' to reset watched repos too)"

clean:
	rm -rf $(BUILD)
