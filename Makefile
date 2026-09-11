BINARY_NAME := baron
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || none)
DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: build test lint fmt vet tidy clean install cross fix

build:
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/baron

.PHONY: test-unit test-e2e

# e2e (test/e2e) spins up real tmux sessions, ptys and git/bd subprocesses;
# run separately from the unit packages instead of `go test ./...`, whose
# default per-package parallelism otherwise races e2e's real timing-sensitive
# waits against every other package's own subprocess-heavy tests (dolt,
# tmux, pty) for the same CPU — a full-suite run has been observed to blow
# past even a 90s wait on a bead merge that reliably completes in seconds
# run alone. -race is skipped for e2e: it's already the slow half of the
# suite, and a real data race there would show up in the unit run too.
test: test-unit test-e2e

test-unit:
	go test -race -count=1 $$(go list ./... | grep -v /test/e2e)

test-e2e:
	go test -count=1 ./test/e2e/...

lint:
	golangci-lint run

fmt:
	gofumpt -l .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin/

install: build
	cp bin/$(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME)

cross:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/baron
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 ./cmd/baron
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 ./cmd/baron
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe ./cmd/baron

fix:
	go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -fix -test \
		-any -atomictypes -embedlit -errorsastype -forvar -mapsloop -minmax \
		-newexpr -plusbuild -rangeint -reflecttypefor -slicesbackward \
		-slicescontains -slicessort -stringsbuilder -stringscut \
		-stringscutprefix -stringsseq -testingcontext -waitgroupgo ./...
	gofumpt -l -w .
	golangci-lint run --fix ./...
