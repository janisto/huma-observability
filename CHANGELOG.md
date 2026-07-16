# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add a `just mutation` recipe backed by the Gremlins CLI, with
  contributor guidance for reviewing meaningful surviving mutants.
- Add a `just fuzz` recipe and contributor guidance for running the existing
  `FuzzParseTraceparent` target with Go's native fuzzing engine.

## [0.3.1] - 2026-07-15

### Added

- Added structured bug and feature request forms that collect reproducible,
  redacted diagnostics and package, Huma, Go, and platform versions.
- Added a repository-local adversarial testing skill for contributors.

### Changed

- Simplified the runnable examples and public documentation, using GCP as the
  canonical configuration while retaining provider-neutral, AWS, Azure, and
  local wrapper examples.
- Strengthened the test suite with behavioral, boundary, failure-recovery,
  concurrency, fuzz, and mutation checks for request context, access logging,
  logger configuration, trace parsing, provider schemas, and the local wrapper
  example.
- Updated CI to cancel superseded runs for the same ref and removed the weekly
  scheduled run.
- Runtime behavior and the public API are unchanged from v0.3.0.

## [0.3.0] - 2026-07-12

### Fixed

- Combine multiple W3C `tracestate` header fields in wire order for both Huma
  and standard `net/http` request-context middleware.
- Preserve the configured cloud preset when access logging completes partial
  internal request metadata.

### Changed

- Run CI on the latest patched Go 1.25 toolchain.

## [0.2.0] - 2026-07-08

### Added

- Added `HTTPRequestContext`, a standard `net/http` middleware for services
  that have routes outside Huma. It installs validated or generated request
  IDs, response request ID headers, W3C trace correlation metadata, and an
  optional request-scoped Zap logger.
- Added mixed-service examples that show Huma routes and non-Huma readiness
  routes sharing the same request metadata and logger.
- Added non-Huma logging guidance for plain `net/http` handlers and Chi route
  groups without adding a generic response-writer wrapper to this package.
- Added behavioral tests for HTTP request context lifecycle, custom headers,
  trace parsing, logger installation and reuse, provider preset fields, and
  Huma/HTTP metadata reuse.

### Changed

- `RequestContextConfig` now accepts `Logger` and `Preset`, allowing Huma
  request context middleware to install the request-scoped logger directly.
- `AccessLogger` now reuses an existing request-scoped logger before falling
  back to `AccessLoggerConfig.Logger`, keeping handler logs and Huma access
  logs on the same per-request logger.
- Updated README and examples to document the package as Huma v2-only and to
  use the same preset across `NewLogger`, `RequestContext`, `AccessLogger`, and
  `HTTPRequestContext`.
- Clarified that `Logger(ctx)` returns the request-scoped logger stored on the
  context, or a no-op logger when no request logger has been installed.
- Clarified that non-Huma access logging is application-owned or router-owned;
  this package does not emit generic `net/http` access logs.

## [0.1.0] - 2026-07-08

### Added

- Initial public release.
- Added Huma v2 request context middleware for request IDs, response request ID
  headers, W3C `traceparent` parsing, and context accessors.
- Added Huma v2 structured Zap access logging middleware.
- Added JSON logger presets for generic logs, Google Cloud Logging, AWS
  CloudWatch/X-Ray query paths, and Azure Monitor/Application Insights query
  paths.
- Added README and runnable examples for basic, GCP, AWS, Azure, and
  project-local wrapper usage.

[Unreleased]: https://github.com/janisto/huma-observability/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/janisto/huma-observability/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/janisto/huma-observability/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/janisto/huma-observability/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/janisto/huma-observability/releases/tag/v0.1.0
