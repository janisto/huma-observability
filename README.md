# huma-observability

`huma-observability` is a small, opinionated companion package for [Huma](https://github.com/danielgtaylor/huma) APIs that need request correlation and structured Zap access logs.

It is not official Huma framework middleware. It is designed to be used by multiple Huma services that want the same production logging contract without copying middleware into each application.

## What It Provides

- W3C `traceparent` parsing with strict validation.
- Request ID extraction, validation, generation, response propagation, and context accessors.
- Request-scoped `*zap.Logger` values available through `obs.Logger(ctx)`.
- Router-agnostic Huma middleware using `func(huma.Context, func(huma.Context))`.
- JSON stdout logger presets for generic, Google Cloud, AWS, and Azure environments.
- Google Cloud Logging support for `severity`, `httpRequest`, and raw Cloud Trace IDs from W3C trace context.
- AWS X-Ray and Azure Application Insights correlation fields derived from incoming trace context.

It does not create tracing spans, export metrics, install OpenTelemetry, or implement RFC 9457 Problem Details behavior.

## W3C Trace Context

W3C `traceparent` is the common trace-correlation input for every cloud preset.
The middleware validates the header once, stores the same trace ID on the
request context, and derives one provider-specific log shape from it:

- Generic logs get `trace_id`, `parent_id`, `trace_flags`, and `trace_sampled`.
- Google Cloud logs get `logging.googleapis.com/trace` using Google's preferred raw `TRACE_ID` format.
- AWS logs get `xray_trace_id`, derived from the W3C trace ID in AWS X-Ray format.
- Azure logs get `operation_Id` and `operation_ParentId`, matching Application Insights' W3C mapping.

Provider-specific propagation headers such as `X-Cloud-Trace-Context` and
`X-Amzn-Trace-Id` are not parsed by this package. If you need full trace
waterfalls, spans, dependency telemetry, or automatic span-level log
correlation, add OpenTelemetry or the relevant cloud instrumentation beside
this middleware.

For Go HTTP tracing, use OpenTelemetry's `otelhttp` instrumentation in your
application when you need real server/client spans and outbound propagation.
Typical setups use `otelhttp.NewHandler` for HTTP handlers and
`otelhttp.NewTransport` for HTTP clients. This package does not configure
OpenTelemetry SDKs, exporters, samplers, or global tracer providers.

Background references:

- Google Cloud trace/log linking documents raw `TRACE_ID` as the preferred format: https://docs.cloud.google.com/trace/docs/trace-log-integration
- AWS X-Ray documents how W3C trace IDs are represented in X-Ray format: https://docs.aws.amazon.com/xray/latest/devguide/xray-api-sendingdata.html
- Azure Application Insights documents W3C Trace Context mapping to operation fields: https://learn.microsoft.com/en-us/azure/azure-monitor/app/javascript-sdk-configuration
- OpenTelemetry `otelhttp` provides Go HTTP server and client instrumentation: https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp

## Install

```sh
go get github.com/janisto/huma-observability
```

The module path remains `github.com/janisto/huma-observability`, but the Go
package name is `obs`. Import the module normally and use `obs.*` at call sites.

This package is currently intended for a `v0.x` release line. Treat public API names and log field names as carefully maintained, but not yet a v1 compatibility promise.

## Basic Setup

More complete examples, including per-cloud deployment notes, are in [EXAMPLES.md](EXAMPLES.md).

```go
package main

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/janisto/huma-observability"
)

func setup(api huma.API) error {
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetDefault,
	})
	if err != nil {
		return err
	}

	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
	}))

	return nil
}
```

Middleware order matters: install `RequestContext` before `AccessLogger` so handlers and lower-level services get request metadata and the request-scoped logger.

## Logging In Handlers And Services

```go
func GetRepository(ctx context.Context, owner, repo string) error {
	obs.Logger(ctx).Info("loading github repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)
	return nil
}
```

`Logger(ctx)` never returns nil. If no request logger has been installed, it returns a no-op logger.

## Google Cloud

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetGCP,
})
if err != nil {
	return err
}

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
```

When a valid W3C trace ID is available, logs include:

- `logging.googleapis.com/trace`: the raw W3C `TRACE_ID`
- `logging.googleapis.com/trace_sampled`: the W3C sampled flag
- `httpRequest`: Cloud Logging's HTTP request object

The middleware does not emit `logging.googleapis.com/spanId` from a W3C parent ID. Those values are not the same contract.

## AWS

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAWS,
})
if err != nil {
	return err
}
```

AWS logs stay flat JSON with `timestamp`, `level`, and `message`. With a valid
W3C `traceparent`, the AWS preset also emits `xray_trace_id`, converting the W3C
trace ID into AWS X-Ray format.

The middleware does not create AWS X-Ray segments or OpenTelemetry spans.
It does not emit `span_id` from an incoming parent ID.

## Azure

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAzure,
})
if err != nil {
	return err
}
```

Azure logs stay flat JSON with `timestamp`, `level`, and `message`. With a valid
W3C `traceparent`, the Azure preset emits Application Insights-compatible
`operation_Id` and `operation_ParentId` fields in addition to the common W3C
trace fields.

## Field Contract

Package-owned fields use `snake_case`.

Request metadata fields:

- `request_id`
- `correlation_id`
- `trace_id`
- `parent_id`
- `trace_flags`
- `trace_sampled`

Provider-specific fields:

- GCP: `logging.googleapis.com/trace`, `logging.googleapis.com/trace_sampled`
- AWS: `xray_trace_id`
- Azure: `operation_Id`, `operation_ParentId`

Access log fields:

- `method`
- `path`
- `path_template`
- `operation_id`
- `status`
- `duration_ms`
- `remote_ip`
- `user_agent`

`AccessLoggerConfig.ExtraFields` may add application-specific fields to the access log. Fields using package-owned or provider-reserved keys are ignored to keep JSON logs from containing duplicate core keys.

Logger keys:

- Generic, AWS, Azure: `timestamp`, `level`, `message`, optional `logger`
- GCP: `timestamp`, `severity`, `message`, optional `logger`

## Request IDs And Trace Correlation

Defaults:

- Request ID header: `X-Request-Id`
- Trace header: `traceparent`
- Tracestate header: `tracestate`
- Response request ID header: same as request ID header
- Generated request IDs: 16 random bytes encoded as lowercase hex

Invalid incoming request IDs are ignored and replaced. Invalid `traceparent` values are ignored for correlation while request processing continues.

`CorrelationID(ctx)` returns the W3C trace ID when a valid `traceparent` exists. Otherwise it returns the request ID.

## Panic Behavior

`AccessLogger` logs a `500` access log when downstream middleware or handlers panic, then re-panics. It does not recover the request or hide the panic from upstream recovery middleware.

## Optional Local Wrapper

Applications that want shorter local logging helpers can add them in application
code. A complete copyable example is available at
[examples/local-wrapper/applog/log.go](examples/local-wrapper/applog/log.go).
It intentionally stays small: `Log`, `Debug`, `Info`, `Warn`, and `Error`.

```go
applog.Info(ctx, "repository loaded", zap.String("repository", "payments"))
applog.Error(ctx, "github request failed", err, zap.Int("status", status))
```

The package itself stays Zap-native and does not add application-specific `LogWarn` or `LogError` wrappers.

## Validation

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
