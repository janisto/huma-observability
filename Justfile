# Justfile for huma-observability
# https://github.com/casey/just
# Development and release checks for huma-observability.

@_:
    just --list

# Check Go formatting without changing files.
[group('qa')]
format:
    test -z "$(gofmt -l .)"

# Run the configured Go linters.
[group('qa')]
lint:
    golangci-lint run ./...

# Apply standard formatting and safe lint fixes.
[group('qa')]
fix:
    go fmt ./...
    golangci-lint run --fix ./...

# Build all packages.
[group('qa')]
build:
    go build ./...

# Run tests, optionally forwarding go test arguments.
[group('test')]
test *args:
    go test {{ args }} ./...

# Run tests with the race detector, optionally forwarding go test arguments.
[group('test')]
race *args:
    go test -race {{ args }} ./...

# Run mutation testing with the installed Gremlins CLI.
[group('test')]
mutation *args:
    gremlins unleash . {{ args }}

# Run a named root-package fuzz target for a bounded duration.
[group('test')]
fuzz target='FuzzParseTraceparent' duration='10s' *args:
    go test -fuzz={{ target }} -fuzztime={{ duration }} {{ args }} .

# Run the complete non-mutating repository gate.
[group('qa')]
workflow-check:
    actionlint
    zizmor --offline .

# Run the complete non-mutating repository gate.
[group('qa')]
qa: workflow-check format lint build test race

# Check dependencies and the Go toolchain for known vulnerabilities.
[group('security')]
vuln:
    govulncheck ./...

# Download dependencies without changing module files.
[group('lifecycle')]
install:
    go mod download

# Update dependencies and normalize module files.
[group('lifecycle')]
update:
    go get -u -t ./...
    go mod tidy
