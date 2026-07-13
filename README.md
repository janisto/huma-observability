# huma-observability

`huma-observability` provides request correlation, request-scoped Zap loggers,
and structured Zap access logging middleware for
[Huma v2](https://github.com/danielgtaylor/huma) APIs. It also provides a small
standard `net/http` request-context middleware for services that have non-Huma
routes.

The module path is `github.com/janisto/huma-observability`; the declared Go
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

Do not use this package as a tracing system. It does not create spans, export
metrics, configure OpenTelemetry, create AWS X-Ray segments, or emit generic
`net/http` access logs.

## Requirements

- Go 1.25 or newer.
- Huma v2.
- Zap.

The package is currently intended for a `v0.x` release line. Public API names
and log field names are maintained carefully, but this is not a v1
compatibility promise yet.

## Install

```sh
go get github.com/janisto/huma-observability
```

Import the module path normally. The package name is `obs`, so application code
uses the `obs` identifier:

```go
import "github.com/janisto/huma-observability"
```

## Quick Start

When this documentation shows one configuration, it uses GCP. Complete
runnable GCP, provider-neutral, AWS, and Azure applications are available in
[`examples`](examples), with usage notes in [EXAMPLES.md](EXAMPLES.md).

```go
package main

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/janisto/huma-observability"
)

func setup(api huma.API) error {
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetGCP,
	})
	if err != nil {
		return err
	}

	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
	}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
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
func GetRepository(ctx context.Context, owner, repo string) error {
	obs.Logger(ctx).Info("loading github repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)
	return nil
}
```

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
`correlation_id` falls back to `request_id`.

Multiple `tracestate` header fields are combined in wire order as required by
W3C Trace Context. The combined value is retained only when it is at most 512
bytes.

This means every log line gets a stable grouping key:

- With valid W3C trace context: group by `correlation_id=<trace-id>`.
- Without valid trace context: group by `correlation_id=<request-id>`.

The package also emits common trace fields when a valid trace exists:

- `trace_id`
- `parent_id`
- `trace_flags`
- `trace_sampled`

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
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetGCP,
})
if err != nil {
	return err
}

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
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

Provider-specific fields:

| Preset | Fields |
| --- | --- |
| GCP | `logging.googleapis.com/trace`, `logging.googleapis.com/trace_sampled`, `httpRequest` |
| AWS | `xray_trace_id` |
| Azure | `operation_Id`, `operation_ParentId` |

Huma access log fields:

- `method`
- `path`
- `path_template`
- `operation_id`
- `status`
- `duration_ms`
- `remote_ip`
- `user_agent`
- `httpRequest` for GCP

Logger keys:

- Generic, AWS, Azure: `timestamp`, `level`, `message`, optional `logger`.
- GCP: `timestamp`, `severity`, `message`, optional `logger`.

`AccessLoggerConfig.ExtraFields` may add application-specific fields to Huma
access logs. Fields using package-owned or provider-reserved keys are ignored to
avoid duplicate core keys in the JSON output.

## Request IDs

Default `RequestContext` and `HTTPRequestContext` behavior:

- Request ID header: `X-Request-Id`.
- Trace header: `traceparent`.
- Tracestate header: `tracestate`.
- Response request ID header: same as the request ID header.
- Generated request IDs: 16 random bytes encoded as lowercase hex.

Invalid incoming request IDs are ignored and replaced. Invalid `traceparent`
values are ignored for correlation while request processing continues.

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
- `Level`: sets the Zap level enabler. Defaults to info.
- `Writer`: overrides the application log destination.
- `ErrorWriter`: overrides Zap's internal error destination.
- `AddCaller`: includes Zap caller fields.
- `Development`: enables Zap development behavior.

## Panic Behavior

`AccessLogger` logs a `500` access log when downstream Huma middleware or
handlers panic, then re-panics. It does not recover the request or hide the
panic from upstream recovery middleware.

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

Use the same checks locally that CI runs:

```sh
go test ./...
go test -race ./...
go vet ./...
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
"$(go env GOPATH)/bin/golangci-lint" run ./...
go install golang.org/x/vuln/cmd/govulncheck@v1.5.0
"$(go env GOPATH)/bin/govulncheck" ./...
test -z "$(gofmt -l .)"
```

## References

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
