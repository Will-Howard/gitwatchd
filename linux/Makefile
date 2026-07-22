GOFLAGS := -trimpath -ldflags "-s -w"

.PHONY: all build build-arm64 test clean

all: build

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o dist/gitwatchd-linux-x86_64 .

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o dist/gitwatchd-linux-aarch64 .

test:
	go test ./...

clean:
	rm -rf dist
