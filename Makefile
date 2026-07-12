# gitwatchd — Xcode-free build for a macOS menu-bar app.
# Compiles with swiftc and hand-assembles a .app bundle. No .xcodeproj, no Xcode.app.
#
# Tier 1 — dev:
#   make          build build/gitwatchd.app
#   make run      build, then (re)launch from build/ — the dev loop
#   make stop     kill any running instance
#   make clean    remove build artifacts
#
# Tier 2 — local install (no sudo):
#   make install    → /Applications + CLI on PATH + launch; self-registers launch-at-login
#   make uninstall  remove app, CLI link, login item, and first-run state (re-onboards next install)
#
# Tier 3 — distribution (needs a paid Apple Developer account):
#   make sign-release DEV_ID="Developer ID Application: NAME (TEAMID)"
#   make notarize     NOTARY_PROFILE=<notarytool keychain profile>

APP_NAME    := gitwatchd
BUNDLE_ID   := com.gitwatchd.app
BUILD_DIR   := build
APP_BUNDLE  := $(BUILD_DIR)/$(APP_NAME).app
MACOS_DIR   := $(APP_BUNDLE)/Contents/MacOS
RES_DIR     := $(APP_BUNDLE)/Contents/Resources
BIN         := $(MACOS_DIR)/$(APP_NAME)

SOURCES     := $(wildcard Sources/*.swift)
SWIFTC      := swiftc
SWIFT_FLAGS := -O -framework AppKit -framework CoreServices -framework ServiceManagement

# Tier 3 config (override on the command line):
DEV_ID         ?=
NOTARY_PROFILE ?= gitwatchd-notary

.PHONY: all run stop clean install uninstall sign-release notarize

all: $(APP_BUNDLE)

$(APP_BUNDLE): $(SOURCES) Resources/Info.plist Makefile
	@echo "→ assembling $(APP_BUNDLE)"
	@mkdir -p $(MACOS_DIR) $(RES_DIR)
	@cp Resources/Info.plist $(APP_BUNDLE)/Contents/Info.plist
	@$(SWIFTC) $(SWIFT_FLAGS) $(SOURCES) -o $(BIN)
	@# ad-hoc sign so the app has a stable identity (needed for launch-at-login)
	@codesign --force --sign - $(APP_BUNDLE) 2>/dev/null || true
	@echo "✓ built $(APP_BUNDLE)"

run: stop all
	@echo "→ launching $(APP_NAME) (dev, from build/)"
	@open $(APP_BUNDLE)

stop:
	@pkill -x $(APP_NAME) 2>/dev/null || true

# --- Tier 2: local install / uninstall ---
# Sudo-free: app → /Applications (or ~/Applications), CLI symlinked onto PATH, then
# launch — the daemon self-registers launch-at-login on the first run of an installed copy.

install: all
	@pkill -x $(APP_NAME) 2>/dev/null || true
	@# App destination — prefer /Applications, fall back to ~/Applications. No sudo.
	@if [ -w /Applications ]; then APP_DEST=/Applications; \
	else APP_DEST="$$HOME/Applications"; mkdir -p "$$APP_DEST"; fi; \
	rm -rf "$$APP_DEST/$(APP_NAME).app"; \
	cp -R "$(APP_BUNDLE)" "$$APP_DEST/"; \
	echo "✓ app  → $$APP_DEST/$(APP_NAME).app"; \
	INNER="$$APP_DEST/$(APP_NAME).app/Contents/MacOS/$(APP_NAME)"; \
	CLI_DEST=""; \
	for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		mkdir -p "$$d" 2>/dev/null || true; \
		if [ -w "$$d" ]; then CLI_DEST="$$d"; break; fi; \
	done; \
	if [ -n "$$CLI_DEST" ]; then \
		ln -sf "$$INNER" "$$CLI_DEST/$(APP_NAME)"; \
		echo "✓ cli  → $$CLI_DEST/$(APP_NAME)"; \
		case ":$$PATH:" in *":$$CLI_DEST:"*) ;; \
			*) echo "  ⚠ $$CLI_DEST is not on your PATH. Add:  export PATH=\"$$CLI_DEST:\$$PATH\"";; esac; \
	else \
		echo "  ⚠ no writable bin dir found. Link manually:  ln -sf \"$$INNER\" /usr/local/bin/$(APP_NAME)"; \
	fi; \
	open "$$APP_DEST/$(APP_NAME).app"
	@echo "✓ launched $(APP_NAME) — menu-bar icon, top-right; starts at login (approve once in System Settings)"
	@echo "  Next:  $(APP_NAME) .   to watch the current repo"

uninstall:
	@pkill -x $(APP_NAME) 2>/dev/null || true
	@# unregister launch-at-login from the installed bundle BEFORE deleting it
	@for a in /Applications/$(APP_NAME).app "$$HOME/Applications/$(APP_NAME).app"; do \
		[ -x "$$a/Contents/MacOS/$(APP_NAME)" ] && "$$a/Contents/MacOS/$(APP_NAME)" autostart off >/dev/null 2>&1 || true; \
	done
	@rm -rf /Applications/$(APP_NAME).app "$$HOME/Applications/$(APP_NAME).app"
	@for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		[ -L "$$d/$(APP_NAME)" ] && rm -f "$$d/$(APP_NAME)" && echo "  removed $$d/$(APP_NAME)"; \
	done; true
	@rm -rf "$$HOME/Library/Application Support/$(APP_NAME)"
	@echo "✓ uninstalled: app, CLI link, login item, and first-run state cleared"
	@echo "  (config at ~/.config/$(APP_NAME) kept — 'rm -rf ~/.config/$(APP_NAME)' to reset watched repos too)"

# --- Tier 3: Developer ID sign + notarize (entirely CLI; no Xcode.app) ---

sign-release: all
	@test -n "$(DEV_ID)" || { echo "✗ set DEV_ID=\"Developer ID Application: NAME (TEAMID)\""; exit 1; }
	@codesign --force --options runtime --timestamp --sign "$(DEV_ID)" $(APP_BUNDLE)
	@codesign --verify --strict --verbose=2 $(APP_BUNDLE)
	@echo "✓ Developer ID signed"

notarize: sign-release
	@ditto -c -k --keepParent $(APP_BUNDLE) $(BUILD_DIR)/$(APP_NAME)-notarize.zip
	@xcrun notarytool submit $(BUILD_DIR)/$(APP_NAME)-notarize.zip --keychain-profile "$(NOTARY_PROFILE)" --wait
	@# staple the .app, THEN zip the stapled app (a zip itself can't be stapled)
	@xcrun stapler staple $(APP_BUNDLE)
	@ditto -c -k --keepParent $(APP_BUNDLE) $(BUILD_DIR)/$(APP_NAME).zip
	@shasum -a 256 $(BUILD_DIR)/$(APP_NAME).zip
	@echo "✓ notarized + stapled → $(BUILD_DIR)/$(APP_NAME).zip"

clean:
	@rm -rf $(BUILD_DIR)
	@echo "✓ cleaned"
