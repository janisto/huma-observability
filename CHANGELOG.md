# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Added GCP profile `0.1.0` selection with newest-supported default resolution,
  exact pinning, and effective-version introspection.
- Added independent `CapturePath`, `CapturePeerIP`, and `CaptureUserAgent`
  access-log opt-ins.
- Added immutable W3C Trace Context Level 1/Level 2 selection, effective-level
  resolution, complete selected-level `tracestate` validation, and Level 2
  `trace_id_random` projection.

### Changed

- **Breaking:** Changed access logging to omit raw path, direct peer IP, and
  user agent by default. Applications that need the previous fields must enable
  the matching capture options.
- **Breaking:** Renamed the opt-in portable direct-peer field from `remote_ip`
  to `peer_ip` and narrowed GCP `httpRequest.requestUrl` to the sanitized
  query-free path.
- Aligned the GCP health fixture to operation `health_check` and deterministic
  12.5 ms access timing.
- **Breaking:** Reject duplicate raw request-ID and `traceparent` field-lines,
  and prevent custom request-ID validators from admitting values outside the
  package's safe baseline grammar.
- **Breaking:** Stop inferring operation-default, 200, and panic 500 statuses
  when Huma has not established a status. Escaping panics now use
  `terminal_reason: "panic"`, force `ERROR`, and retain the original panic
  even if access-log enrichment also panics.
- Contain panics from the access clock, status mapper, enrichment callback, and
  writer without changing handler behavior; keep the first repeated custom
  field so package-controlled JSON contains no duplicate member names.
- Validate Huma operation paths before emitting canonical `path_template`
  values; unmatched and router-specific catch-all requests remain outside the
  operation middleware boundary.
- Fold every GCP severity into the portable five-level vocabulary, reject
  terminal or unknown status-callback levels, omit unavailable request paths,
  and emit only canonical unzoned IP address literals for direct peers.

### Fixed

- Preserve sampling while omitting the Level 2 random flag for unknown future
  `traceparent` versions.
- Reject `zap.Inline` values from access-log `ExtraFields` so nested marshalers
  cannot bypass reserved-key collision protection.
- Align path-privacy documentation with the tested absolute-form behavior:
  scheme, authority, query, and fragment are removed while the escaped path is
  retained.

## [1.0.1] - 2026-07-17

### Added

- Added a tested GCP health-route use case that writes correlated structured
  application logs at info and debug levels alongside the terminal access log.

### Changed

- Lowered the minimum supported Huma v2 version from v2.38.0 to v2.30.0 and
  added CI coverage against the latest Huma v2 release.

## [1.0.0] - 2026-07-16

### Added

- Added README badges for the latest release, Go reference, supported Go
  version, CI status, and license.
- Added a maintainer release guide and grouped `just` commands for repository
  QA, tests, dependency lifecycle tasks, and vulnerability checks.
- Added a `just mutation` recipe backed by the Gremlins CLI, with
  contributor guidance for reviewing meaningful surviving mutants.
- Added a `just fuzz` recipe and contributor guidance for running the existing
  `FuzzParseTraceparent` target with Go's native fuzzing engine.

### Changed

- Declared the current public API, structured log fields, defaults, and
  supported runtime versions stable under Semantic Versioning for v1.
- Clarified the package's structured standard-output logging model, scope, and
  non-goals.
- Runtime behavior and the public API are unchanged from v0.3.1.

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

[Unreleased]: https://github.com/janisto/huma-observability/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/janisto/huma-observability/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/janisto/huma-observability/compare/v0.3.1...v1.0.0
[0.3.1]: https://github.com/janisto/huma-observability/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/janisto/huma-observability/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/janisto/huma-observability/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/janisto/huma-observability/releases/tag/v0.1.0
