# huma-observability

[![Latest release](https://img.shields.io/github/v/release/janisto/huma-observability)](https://github.com/janisto/huma-observability/releases/latest)
[![Go Reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white)](https://pkg.go.dev/github.com/janisto/huma-observability/v2)
[![Go version](https://img.shields.io/github/go-mod/go-version/janisto/huma-observability)](https://github.com/janisto/huma-observability/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/janisto/huma-observability/ci.yml?branch=main&label=CI)](https://github.com/janisto/huma-observability/actions/workflows/ci.yml)
[![Socket Badge](https://badge.socket.dev/go/package/github.com/janisto/huma-observability/v2)](https://socket.dev/go/package/github.com/janisto/huma-observability/v2)

`huma-observability` provides request correlation, request-scoped Zap loggers,
and structured Zap access logging middleware for
[Huma v2](https://github.com/danielgtaylor/huma) APIs. It also provides a small
standard `net/http` request-context middleware for services that have non-Huma
routes.

## Why this package exists

Managed platforms such as Cloud Run already collect container output.
Applications should only need to write structured JSON to standard output
(`stdout`); the platform can handle ingestion and delivery.

Compared with sending logs through an in-process cloud logging client, this
reduces container CPU, memory, and network use by removing logging API calls,
authentication, buffering, batching, and retry work from the application. Under
sustained logging load, that reduction can provide a noticeable performance
improvement. It also avoids the dependency and maintenance cost of a cloud
logging SDK, including its configuration, credentials, and upgrades.

This package turns that simple pipeline into useful production observability.
It provides validated request IDs, strict W3C trace correlation,
request-scoped fields, and one structured terminal access record. Application
and access logs share the same correlation metadata, making all records from a
request easier to find, filter, and understand.

Cloud presets map the same logging contract to provider-oriented fields without
coupling application code to a cloud logging SDK. The package focuses on
structured logging and request correlation: it does not create spans, configure
OpenTelemetry, or ship logs to a backend.

## Why newline-delimited JSON

`NewLogger` emits newline-delimited JSON (NDJSON, also called JSON Lines): each
application or access event is one compact, self-contained JSON object followed
by one LF (`\n`). The output is a stream of objects, never a JSON array.

NDJSON is deliberate for production logging:

- Agents such as Vector, Fluent Bit, and Datadog can parse entries as a stream
  with bounded memory instead of waiting for a closing array bracket.
- Append-only output needs no array brackets, commas, whole-file rewrites, or
  trailing-comma coordination. Each logger call submits one complete encoded
  line; the destination and record size determine OS-level write atomicity.
- A crash or interrupted final write can damage the incomplete last line, while
  previously completed lines remain independently parseable.
- Analytics systems can split large inputs on newline boundaries and process
  independent records in parallel.
- Standard tools work directly on the stream, for example
  `head -n 20 app.log | jq -r '.message'`.

Standard JSON arrays are suited to complete documents; NDJSON retains JSON's
structured fields while providing framing designed for continuous log streams.

## Package scope

The module path is `github.com/janisto/huma-observability/v2`; the declared Go
package name is `obs`.

This is not official Huma framework middleware. It is a small, opinionated
package for services that want the same production logging contract without
copying request middleware into every application.

## When To Use It

Use this package when your Huma v2 service needs:

- Request IDs with validation, generation, response propagation, and context
  accessors.
- Request-scoped `*zap.Logger` values available through `obs.Logger(ctx)`.
- JSON access logs from Huma middleware, independent of the HTTP router.
- Router-wide request metadata for health checks, readiness probes, redirects,
  static handlers, 404/405 handlers, and recovery middleware.
- W3C `traceparent` parsing for trace-level log correlation.
- Cloud-oriented log fields for Google Cloud Logging, AWS CloudWatch/X-Ray
  query paths, or Azure Monitor/Application Insights ingestion.

It also does not export metrics, create AWS X-Ray segments, or emit generic
`net/http` access logs.

## Requirements

- Go 1.25 or newer.
- Huma v2.30.0 or newer within the Huma v2 line.
- Zap.

The v1 API and log contract remains available at the unsuffixed module path.
This checkout targets v2 because its privacy defaults and structured output are
intentionally incompatible with v1. See the changelog migration section before
upgrading.
Version 2 provides no v1 field aliases, option shims, or unsuffixed import
fallback; applications must migrate to the documented v2 API and module path.

## Install

```sh
go get github.com/janisto/huma-observability/v2
```

Import the module path normally. The package name is `obs`, so application code
uses the `obs` identifier:

```go
import "github.com/janisto/huma-observability/v2"
```

## Quick Start

When this documentation shows one configuration, it uses GCP. Complete
runnable GCP, provider-neutral, AWS, and Azure applications are available in
[`examples`](examples), with usage notes in [EXAMPLES.md](EXAMPLES.md).

```go
package main

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/janisto/huma-observability/v2"
)

func setup(api huma.API) error {
	profileVersion, err := obs.ResolveGCPProfileVersion(obs.PresetGCP, "")
	if err != nil {
		return err
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset:            obs.PresetGCP,
		GCPProfileVersion: profileVersion,
	})
	if err != nil {
		return err
	}

	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
		Logger:            logger,
		Preset:            obs.PresetGCP,
		TraceContextLevel: obs.TraceContextLevel1,
	}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
		Logger:            logger,
		Preset:            obs.PresetGCP,
		GCPProfileVersion: profileVersion,
	}))

	return nil
}
```

Middleware order is part of the contract: install `RequestContext` before
`AccessLogger`. `RequestContext` installs request metadata and the
request-scoped logger; `AccessLogger` writes the Huma operation-aware access
log.

## HTTP Request Context

For services with both Huma and non-Huma routes, install `HTTPRequestContext` at
the outer router boundary:

```go
handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
})(router)
```

`HTTPRequestContext` installs request IDs, trace correlation metadata, and
response request ID headers for every HTTP request. When `Logger` is
configured, it also installs the request-scoped logger. Huma `RequestContext`
reuses that metadata when a request reaches a Huma route, so one inbound
request keeps one request ID across both layers.

`HTTPRequestContext` does not emit access logs and does not wrap
`http.ResponseWriter`. Non-Huma access logs are application-owned or
router-owned. Huma routes should use `AccessLogger` for operation-aware access
logs with `path_template` and `operation_id`.

## Handler Logging

Use `obs.Logger(ctx)` anywhere you have the request `context.Context`.

```go
func Health(ctx context.Context) {
	logger := obs.Logger(ctx)
	logger.Info("health check",
		zap.String("service_name", "example-service"),
		zap.String("service_version", "1.0.0"),
		zap.String("health_status", "ok"),
	)
	logger.Debug("dependency check",
		zap.String("dependency", "database"),
		zap.String("dependency_status", "ok"),
	)
}
```

Application records and the package-owned access record share the request
logger's correlation fields. `NewLogger` defaults to info level; configure
`LoggerConfig.Level` with `zapcore.DebugLevel` when debug events should be
written. The canonical GCP example and its route-level tests demonstrate both
level settings and decode newline-delimited JSON through the same writer
boundary that defaults to stdout.

`Logger(ctx)` never returns nil. Configure `RequestContextConfig.Logger` for
Huma-only services, or `HTTPRequestContextConfig.Logger` at the router boundary
for mixed Huma and non-Huma services. If a context did not pass through
configured request-context middleware, `Logger(ctx)` returns a no-op logger
instead of using a package-global logger or implicit stdout fallback.

Recovery middleware that logs with `Logger(r.Context())` must run after
`HTTPRequestContext` if it needs request metadata. Logs emitted before
request-context middleware may not have `request_id`; that means the request did
not cross the package boundary yet, the middleware order is wrong, or the log is
intentionally outside an HTTP request.

## Trace Correlation

W3C `traceparent` is the only trace context input parsed by this package. When
the header is valid, the W3C trace ID becomes the request `correlation_id` and
provider-specific trace field source. When the header is missing or invalid,
`correlation_id` falls back to `request_id`. Level 1 is the default. Select
the pinned Level 2 mode explicitly and use the same immutable level for request
context and access logging:

```go
const traceLevel = obs.TraceContextLevel2
api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger, Preset: obs.PresetGCP, TraceContextLevel: traceLevel,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger, Preset: obs.PresetGCP, TraceContextLevel: traceLevel,
}))
```

`ResolveTraceContextLevel(0)` exposes the effective default. Unsupported
levels fail during middleware construction. Exactly one raw `traceparent`
field-line is eligible. Multiple `tracestate` fields are combined in wire
order and validated with the selected level's complete key/value grammar,
unique keys, at most 32 members, and a 512-byte raw ceiling. Invalid
`tracestate` is discarded without discarding a valid `traceparent`.

This means every log line gets a stable grouping key:

- With valid W3C trace context: group by `correlation_id=<trace-id>`.
- Without valid trace context: group by `correlation_id=<request-id>`.

The package also emits common trace fields when a valid trace exists:

- `trace_id`
- `parent_id`
- `trace_flags`
- `trace_sampled`
- `trace_id_random` only for version `00` in explicit Level 2 mode

Provider-specific propagation headers such as `X-Cloud-Trace-Context`,
`X-Amzn-Trace-Id`, and Azure's legacy `Request-Id` header are intentionally not
parsed. If your service must bridge those headers into W3C Trace Context, do
that with cloud SDK instrumentation or OpenTelemetry beside this package.

For real Go HTTP tracing, use OpenTelemetry's `otelhttp` instrumentation in
your application. `otelhttp.NewHandler` wraps HTTP handlers with server spans,
and `otelhttp.NewTransport` instruments HTTP clients and outbound propagation.
This package does not configure OpenTelemetry SDKs, exporters, samplers, or
global tracer providers.

## Cloud Presets

Use the same preset for `NewLogger`, `RequestContext`, `AccessLogger`, and
`HTTPRequestContext` when those pieces are used together.

### Google Cloud

```go
profileVersion, err := obs.ResolveGCPProfileVersion(obs.PresetGCP, "")
if err != nil {
	return err
}
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset:            obs.PresetGCP,
	GCPProfileVersion: profileVersion,
})
if err != nil {
	return err
}

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger:            logger,
	Preset:            obs.PresetGCP,
	GCPProfileVersion: profileVersion,
}))
```

The GCP preset emits Cloud Logging-friendly JSON:

- `severity` instead of `level`.
- `httpRequest` for Huma access logs.
- `logging.googleapis.com/trace` with the raw W3C `TRACE_ID`.
- `logging.googleapis.com/trace_sampled` from the W3C sampled flag.

The middleware does not emit `logging.googleapis.com/spanId` from a W3C
`parent-id`. A log span ID must come from a real current span; the incoming
parent ID is not the same semantic value.

The installed package supports GCP profile `0.1.0`. An omitted version resolves
to the newest supported version during construction; an exact pin uses
`obs.GCPProfileVersionV0_1_0`. `ResolveGCPProfileVersion` exposes the effective
version for diagnostics and coherent logger/access configuration. Resolution
does not query Google Cloud, a registry, or the network. `NewLogger` returns an
error for an invalid selection; `AccessLogger` panics immediately during
middleware construction because its established constructor has no error
return.

### AWS

The AWS preset keeps logs as flat JSON with `timestamp`, `level`, and
`message`. With a valid W3C `traceparent`, it also emits:

- `trace_id`
- `parent_id`
- `trace_flags`
- `trace_sampled`
- `xray_trace_id`, derived from the W3C trace ID in AWS X-Ray format.

The middleware does not create AWS X-Ray segments and does not emit `span_id`
from an incoming W3C parent ID.

### Azure

The Azure preset keeps logs as flat JSON with `timestamp`, `level`, and
`message`. With a valid W3C `traceparent`, it emits:

- `trace_id`
- `parent_id`
- `trace_flags`
- `trace_sampled`
- `operation_Id`, mapped from the W3C trace ID.
- `operation_ParentId`, mapped from the W3C parent ID.

## Field Contract

Package-owned fields use `snake_case`. Provider-required fields keep the names
expected by the target platform.

Request metadata fields:

| Field | Meaning |
| --- | --- |
| `request_id` | The local HTTP request ID for this service. |
| `correlation_id` | The W3C trace ID when valid trace context exists; otherwise the request ID. |
| `trace_id` | The W3C trace ID from `traceparent`. |
| `parent_id` | The W3C parent ID from `traceparent`. |
| `trace_flags` | The W3C trace flags value. |
| `trace_sampled` | Boolean value derived from the sampled flag. |
| `trace_id_random` | Level 2 boolean derived from bit one for version `00`; absent in Level 1 and unknown higher versions. |

Provider-specific fields:

| Preset | Fields |
| --- | --- |
| GCP | `logging.googleapis.com/trace`, `logging.googleapis.com/trace_sampled`, `httpRequest` |
| AWS | `xray_trace_id` |
| Azure | `operation_Id`, `operation_ParentId` |

Huma access log fields:

- `method`
- `path` when `CapturePath` is enabled
- `path_template`
- `operation_id`
- `status`, only when Huma has established it before this middleware returns
- `duration_ms`
- `terminal_reason`, set to `panic` for an escaping panic
- `peer_ip` when `CapturePeerIP` is enabled; this is the direct transport peer
- `user_agent` when `CaptureUserAgent` is enabled
- `httpRequest` for GCP

Huma operation paths already use the portable whole-segment `{name}` form.
Only valid canonical operation paths are emitted. The selected Huma middleware
boundary runs for registered operations, so it does not claim unmatched-route
or router-specific catch-all access records; those remain router-owned.

The three capture options are independent and default to false. Selecting GCP
does not enable them. When path capture is enabled, GCP
`httpRequest.requestUrl` is exactly the sanitized path: it never contains a
scheme, authority, query, or fragment. GCP `remoteIp` and `userAgent` appear
only with their corresponding portable opt-ins.

Captured paths and peers are validated rather than repaired: unavailable and
opaque targets are omitted; absolute-form targets are reduced to their escaped
path without scheme, authority, query, or fragment. Peer fields contain only
canonical unzoned IPv4 or IPv6 address literals. GCP severities always use `DEBUG`,
`INFO`, `WARNING`, `ERROR`, or `CRITICAL`. A custom status mapper returning a
terminal or unknown Zap level falls back to the default status mapping.

Logger keys:

- Generic, AWS, Azure: `timestamp`, `level`, `message`, optional `logger`.
- GCP: `timestamp`, `severity`, `message`, optional `logger`.

`AccessLoggerConfig.ExtraFields` may add application-specific fields to Huma
access logs. Fields using package-owned or provider-reserved keys are ignored to
avoid duplicate core keys in the JSON output. If the returned Zap field slice
repeats a custom key, the first value wins. Inline object marshalers are ignored
because their inner keys cannot be checked safely before they enter the access
record namespace.

That collision guarantee applies to package-controlled context and access
merges. Arbitrary fields passed directly to a raw Zap logger are application
owned; callers must not reuse package-reserved names or emit duplicate keys.

## Request IDs

Default `RequestContext` and `HTTPRequestContext` behavior:

- Request ID header: `X-Request-Id`.
- Trace header: `traceparent`.
- Tracestate header: `tracestate`.
- Trace Context level: W3C Level 1.
- Response request ID header: same as the request ID header.
- Generated request IDs: 16 random bytes encoded as lowercase hex.

Incoming request IDs use 1–128 ASCII letters, digits, `-`, `.`, `_`, and
`~`. A custom validator may further restrict that baseline but cannot admit
an unsafe caller value. It is applied only to caller input, never to generated
or package-fallback IDs. A configured generator is tried exactly twice unless
its first result passes the baseline. Validator and generator panics are
contained as rejection/failure and do not bypass the handler. Multiple raw request-ID or `traceparent` field-lines are
ambiguous and rejected. Invalid input is replaced or ignored while request
processing continues.

`CorrelationID(ctx)` returns the same value written to `correlation_id`: the
W3C trace ID when a valid `traceparent` exists, otherwise the request ID.

Set `DisableResponseHeader` when an upstream gateway owns request ID response
headers and the application should not write one.

Use `RequestContextConfig` for Huma routes and `HTTPRequestContextConfig` for
router-wide HTTP middleware when you need custom request ID or trace header
names.

## Logger Configuration

`NewLogger` creates a JSON Zap logger. By default it writes application logs to
stdout and Zap internal errors to stderr.

Useful options:

- `Preset`: selects generic, GCP, AWS, or Azure field naming.
- `GCPProfileVersion`: optionally pins a supported GCP profile; omission selects
  the newest installed version when `PresetGCP` is selected.
- `Level`: sets the Zap level enabler. Defaults to info.
- `Writer`: overrides the application log destination.
- `ErrorWriter`: overrides Zap's internal error destination.
- `AddCaller`: includes Zap caller fields.
- `Development`: enables Zap development behavior.

`AccessLoggerConfig` separately provides `GCPProfileVersion`,
`TraceContextLevel`, `CapturePath`, `CapturePeerIP`, `CaptureUserAgent`,
the injectable monotonic `Now` clock, status-level mapping, and
collision-filtered extra fields.

Operation defaults are route metadata, not evidence that a response status was
established. Access records therefore omit `status` when `ctx.Status()` is
still zero instead of guessing the operation default or 200. In that normal
status-less case the level is info and the status callback is not invoked.
Handler errors converted by Huma into completed 4xx/5xx responses remain normal
responses and omit `terminal_reason`.

## Panic Behavior

`AccessLogger` logs an error access record with terminal reason `panic` when
downstream Huma middleware or handlers panic, then re-panics. Status is retained
only if Huma had already established one; no 500 is invented. The original
panic remains the propagated value even if access-log enrichment or writing
also panics. On normal handler completion, panicking clock, status-mapper,
enrichment, and writer paths are contained; safe defaults are used when
possible, handler behavior is unchanged, and failed writes are not retried. The
package does not recover the request or hide a downstream panic from upstream
recovery middleware.

## Optional Local Wrapper

Applications that want shorter local logging helpers can add them in
application code. A complete copyable example is available at
[examples/local-wrapper/applog/log.go](examples/local-wrapper/applog/log.go).
It intentionally stays small: `Log`, `Debug`, `Info`, `Warn`, and `Error`.

```go
applog.Info(ctx, "repository loaded", zap.String("repository", "payments"))
applog.Error(ctx, "github request failed", err, zap.Int("status", status))
```

The package itself stays Zap-native and does not add application-specific
`LogWarn` or `LogError` wrappers.

## Validation

Development uses [just](https://github.com/casey/just). On macOS, install the
workflow linters:

```sh
brew install actionlint zizmor
```

Then run the repository gates:

```sh
just install
just qa
just vuln
```

`just qa` runs formatting, lint, build, tests, race tests,
[actionlint](https://github.com/rhysd/actionlint), and
[zizmor](https://docs.zizmor.sh/). `just vuln` runs the Go vulnerability scanner
separately.

## References

- W3C Trace Context Level 1 Recommendation:
  https://www.w3.org/TR/trace-context/
- W3C Trace Context Level 2 Candidate Recommendation pinned by this package:
  https://www.w3.org/TR/2024/CRD-trace-context-2-20240328/
- Google Cloud trace/log linking documents raw `TRACE_ID` as the preferred log
  trace format: https://docs.cloud.google.com/trace/docs/trace-log-integration
- Google Cloud changed raw `TRACE_ID` to the preferred `LogEntry.trace` format
  in January 2026: https://docs.cloud.google.com/trace/docs/release-notes
- Google Cloud structured logging documents special JSON fields such as
  `severity`, `httpRequest`, `logging.googleapis.com/trace`, and
  `logging.googleapis.com/trace_sampled`:
  https://docs.cloud.google.com/logging/docs/structured-logging
- AWS X-Ray documents W3C trace IDs formatted as X-Ray trace IDs:
  https://docs.aws.amazon.com/xray/latest/devguide/xray-api-sendingdata.html
- Azure Application Insights documents telemetry correlation fields including
  `operation_Id` and `operation_ParentId`:
  https://learn.microsoft.com/en-us/azure/azure-monitor/app/data-model-complete
- Azure Application Insights documents W3C Trace Context mapping to
  `Operation_Id` and `Operation_ParentId`:
  https://learn.microsoft.com/en-us/azure/azure-monitor/app/javascript-sdk-configuration
- OpenTelemetry `otelhttp` provides Go HTTP server and client instrumentation:
  https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp

## Mutation Testing

Install [Gremlins](https://github.com/go-gremlins/gremlins) with Homebrew on
macOS:

```sh
brew tap go-gremlins/tap
brew install gremlins
```

Then run its mutation campaign against covered production code with:

```sh
just mutation
```

Gremlins changes expressions and conditions, then checks whether the existing
tests detect each behavioral change. Review `LIVED` mutants as possible test
gaps; equivalent transformations do not need artificial assertions. Mutation
testing intentionally runs outside `just qa` and may take several minutes. The
configured per-mutant safety timeout does not limit the total campaign time.

## Fuzz Testing

This repository uses Go's native fuzzing engine for `FuzzParseTraceparent`.
Run the default ten-second session with:

```sh
just fuzz
```

Pass the target and duration explicitly for a longer run:

```sh
just fuzz FuzzParseTraceparent 1m
```

The equivalent native Go command is:

```sh
go test -fuzz=FuzzParseTraceparent -fuzztime=10s .
```

Go first replays the seed corpus and then generates new inputs. When fuzzing
finds a failure, it minimizes the input and writes it under
`testdata/fuzz/FuzzParseTraceparent`; normal `go test ./...` runs saved corpus
inputs as regression tests. Review and commit a failing input together with the
fix when it represents behavior the parser must preserve.

See the [Go fuzzing documentation](https://go.dev/doc/security/fuzz/) for the
engine's workflow and additional flags.
