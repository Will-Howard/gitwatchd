# gitwatchd for Linux: a single static Go binary.
#   make          build build/gitwatchd for the host platform
#   make test     go vet + go test
#   make clean    remove build artifacts

GOFLAGS := -trimpath -ldflags "-s -w"

.PHONY: all build test clean

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o build/gitwatchd .

test:
	go vet ./...
	go test ./...

clean:
	rm -rf build
