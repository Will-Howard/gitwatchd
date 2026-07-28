# gitwatchd: one implementation per platform, in macos/ and linux/.
# This Makefile picks the one for the current platform and forwards every
# target to it. Run make inside a platform directory to be explicit.

UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
PLATFORM := macos
else ifeq ($(UNAME),Linux)
PLATFORM := linux
else
$(error unsupported platform: $(UNAME))
endif

TARGETS := all run stop clean install uninstall test sign-release notarize

.PHONY: $(TARGETS)
$(TARGETS):
	@$(MAKE) -C $(PLATFORM) $@
