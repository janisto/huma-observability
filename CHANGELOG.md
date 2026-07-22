# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.0.0] - 2026-07-22

Version 2 uses module path `github.com/janisto/huma-observability/v2` and cannot
be tagged on the v1 module path.

### Migration from v1

- Change imports and installation commands to
  `github.com/janisto/huma-observability/v2`.
- Enable `CapturePath`, `CapturePeerIP`, and `CaptureUserAgent` explicitly where
  those privacy-sensitive fields are still required.
- Rename consumers of `remote_ip` to `peer_ip`; the new field uses the direct
  socket peer and ignores proxy-derived addresses.
- Update status and error queries to use authoritative committed status,
  the standardized `panic` terminal reason, and unconditional `ERROR` severity
  for abnormal completion.
- Custom request-ID validators apply only to caller input and may broaden it
  within Go's native HTTP field-value boundary; generated IDs retain the
  package baseline grammar.
- Remove v1 compatibility aliases and shims; migrate imports and configuration
  directly to the documented v2 surface.

### Added

- Added independent `CapturePath`, `CapturePeerIP`, and `CaptureUserAgent`
  access-log opt-ins.
- Added immutable W3C Trace Context Level 1/Level 2 selection, effective-level
  resolution, complete selected-level `tracestate` validation, and Level 2
  `trace_id_random` projection.
- Added a conditional consumer-image build as a packaging and integration
  diagnostic, with Podman-first local builds and Docker fallback. Optional
  independent audits are informational and never a publication requirement.

### Changed

- Applied the RFC 9110 field-content and valid UTF-8 boundary before custom
  request-ID validation.
- Skipped optional access enrichment after Zap rejected the effective level.
- Defined LF-terminated NDJSON as the logging boundary.
- Omitted invalid UTF-8 User-Agent field values before Zap encoding could
  replace their bytes; preserved framework-accepted Unicode and internal
  whitespace.
- Changed access logging to omit raw path, direct peer IP, and
  user agent by default. Applications that need the previous fields must enable
  the matching capture options.
- Renamed the opt-in portable direct-peer field from `remote_ip`
  to `peer_ip` and narrowed GCP `httpRequest.requestUrl` to the sanitized
  query-free path.
- Rejected duplicate raw request-ID and `traceparent` field-lines;
  custom request-ID validators can broaden caller input within Go's native HTTP
  field-value boundary while generated IDs remain strict.
- Stopped inferring operation-default, 200, and panic 500 statuses
  when Huma has not established a status. Escaping panics now use
  `terminal_reason: "panic"`, force `ERROR`, and retain the original panic
  even if access-log enrichment also panics.
- Contained panics from the access clock, status mapper, enrichment callback,
  and writer without changing handler behavior.
- Kept the first repeated custom field so package-controlled JSON contains no
  duplicate member names.
- Preserved nonempty Huma operation templates and framework-exposed escaped
  paths, including asterisk-form paths, without package-invented route or
  percent-encoding validation.
- Folded every GCP severity into the portable five-level vocabulary, rejected
  terminal or unknown status-callback levels, omitted unavailable request
  paths, and emitted only canonical unzoned IP address literals for direct
  peers.

### Removed

- Removed v1 compatibility aliases and configuration shims from the v2 API.

### Fixed

- Protected only exact record-owned top-level fields in raw NDJSON, while
  preserving access-only application fields, exact aliases for inactive
  provider presets, other non-owned provider-looking keys, application
  namespaces, and reserved-looking fields nested with `zap.Namespace`.
- Preserved the selected provider preset through `HTTPRequestContext`, rejected
  a mismatched preset whenever existing request metadata is reused, and called
  a configured request-ID generator once before using the package fallback.
- Preserved the default request-ID entropy path on successful reads and used
  the package fallback only on read failure.
- Emitted GCP `httpRequest.latency` with canonical ProtoJSON fractional widths:
  0, 3, 6, or 9 digits according to the required precision.
- Preserved framework-valid route parameter names, including extended and
  longer names, HTTP-safe opaque future `traceparent` suffixes without an
  invented length cap, valid `tracestate` beyond 512 characters, HTAB
  User-Agent values, custom-admitted request IDs, and nonempty static operation
  IDs. Rejected provider-preset or trace-level disagreement regardless of
  middleware order, rejected unknown presets consistently, reserved Zap
  `stacktrace`, and protected Zap-owned caller and Level 2 trace fields.
- Admitted a comma in one request-ID field-line when the configured
  application validator accepts it; real duplicate field-lines remain
  rejected.
- Preserved sampling while omitting the Level 2 random flag for unknown future
  `traceparent` versions.
- Rejected `zap.Inline` values from access-log `ExtraFields` so nested
  marshalers cannot bypass reserved-key collision protection.

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

[Unreleased]: https://github.com/janisto/huma-observability/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/janisto/huma-observability/compare/v1.0.1...v2.0.0
[1.0.1]: https://github.com/janisto/huma-observability/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/janisto/huma-observability/compare/v0.3.1...v1.0.0
[0.3.1]: https://github.com/janisto/huma-observability/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/janisto/huma-observability/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/janisto/huma-observability/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/janisto/huma-observability/releases/tag/v0.1.0
