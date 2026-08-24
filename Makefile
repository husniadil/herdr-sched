.PHONY: build test test-full e2e release-check install clean

BIN := bin/hsched

build:
	go build -o $(BIN) ./cmd/hsched

# Two gates, one suite — the same split both siblings use, for the same reason:
#
#   make test       the loop, in seconds — the pure decision core and the
#                   payload shapes, no processes spawned. -short is what makes
#                   that true: the cases that start a daemon, walk the socket
#                   or shell out to a fake skip on it.
#   make test-full  the gate before a commit — the above plus every case that
#                   drives a fake sibling on PATH, with -race and a
#                   cross-compile vet of the other platform.
#
# gofmt is checked rather than applied: a formatting fix belongs in the commit
# that caused it, not silently in whoever runs the gate next.
test:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go test -short ./...

test-full: test
	go vet ./...
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	go test -race ./...

# Layer 3, and deliberately OUT of the gate above: it drives the SHIPPED
# binary through the plugin's own scripts and both doors, against a throwaway
# state dir. A machine that cannot build the binary gets a loud skip naming
# what was missing, never a silent pass. Run it before a release tag, and
# whenever a door, a script or the manifest moves.
e2e: build
	go test -tags e2e -count=1 -v ./internal/e2e/...

# The same layer 3, with the skip turned into a failure. A release must not be
# cut on a suite that silently did not run, so this is what goes before a tag —
# and it is the target both siblings spell the same way.
release-check: test-full build
	SCHED_E2E_REQUIRED=1 go test -tags e2e -count=1 -v ./internal/e2e/...

# `go install`, not a copy into $GOPATH/bin. GOBIN is what decides where an
# installed binary lands, and a toolchain manager sets it away from GOPATH: a
# copy can put `hsched` somewhere nothing on PATH can run it, and an agent that
# cannot reach the CLI is not choosing the MCP door, it is being handed one
# surface.
install:
	go install ./cmd/hsched

clean:
	rm -rf bin dist coverage.out
