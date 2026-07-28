# gitwatchd for Linux: a single static Go binary.
#   make          build build/gitwatchd for the host platform
#   make test     go vet + go test
#   make install  put the binary on PATH (install.sh does the work)
#   make clean    remove build artifacts

GOFLAGS := -trimpath -ldflags "-s -w"
BUILD   := build

# Where install.sh puts the binary; override as `make install DEST=/usr/local/bin`.
DEST ?=

.PHONY: all build test install uninstall clean

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BUILD)/gitwatchd .

test:
	go vet ./...
	go test ./...

install: build
	@./install.sh $(DEST)

uninstall:
	@gitwatchd autostart off >/dev/null 2>&1 || true
	@gitwatchd stop >/dev/null 2>&1 || true
	@for d in /usr/local/bin "$$HOME/.local/bin" "$$HOME/bin"; do \
		[ -f "$$d/gitwatchd" ] && rm -f "$$d/gitwatchd" && echo "  removed $$d/gitwatchd"; \
	done; true
	@rm -rf "$$HOME/.local/state/gitwatchd"
	@echo "✓ uninstalled: binary, systemd unit and daemon state"
	@echo "  (config at ~/.gitwatchd kept: 'rm ~/.gitwatchd' to reset watched repos too)"

clean:
	rm -rf $(BUILD)
