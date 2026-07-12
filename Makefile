# gitwatchd — Xcode-free build for a macOS menu-bar app.
# Compiles with swiftc and hand-assembles a .app bundle. No .xcodeproj, no Xcode.app.
#
#   make          build build/gitwatchd.app
#   make run      build, then (re)launch it — the dev loop
#   make stop     kill any running instance
#   make reset    stop + wipe any stale installed copies / CLI links / login item
#   make clean    remove build artifacts

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

.PHONY: all run stop reset clean

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
	@echo "→ launching $(APP_NAME)"
	@open $(APP_BUNDLE)

stop:
	@pkill -x $(APP_NAME) 2>/dev/null || true

# Wipe anything a previous experiment may have left behind, so manual testing
# always starts from the freshly built bundle and never a stale install.
# (Leaves your config at ~/.config/$(APP_NAME) alone — that's your data.)
reset: stop
	@[ -x "$(BIN)" ] && "$(BIN)" autostart off >/dev/null 2>&1 || true
	@rm -rf /Applications/$(APP_NAME).app "$$HOME/Applications/$(APP_NAME).app"
	@for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		[ -L "$$d/$(APP_NAME)" ] && rm -f "$$d/$(APP_NAME)" && echo "  removed $$d/$(APP_NAME)"; \
	done; true
	@echo "✓ reset (config at ~/.config/$(APP_NAME) kept — 'rm -rf ~/.config/$(APP_NAME)' to wipe that too)"

clean:
	@rm -rf $(BUILD_DIR)
	@echo "✓ cleaned"
