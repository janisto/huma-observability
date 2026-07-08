# Changelog

All notable changes to this project are documented in this file.

## v0.2.0 - Unreleased

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

## v0.1.0 - 2026-07-08

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
