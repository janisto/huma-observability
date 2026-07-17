# Examples

This guide shows how to wire `huma-observability` into Huma v2 services while
keeping one log contract across Google Cloud, provider-neutral, AWS, and Azure
deployments.

When one configuration is shown, this project uses GCP as the canonical
example. The other runnable applications remain first-class and tested.

| Example | Purpose |
| --- | --- |
| [`examples/gcp`](examples/gcp) | Canonical Google Cloud Logging field shape. |
| [`examples/basic`](examples/basic) | Generic JSON for local or provider-neutral pipelines. |
| [`examples/aws`](examples/aws) | CloudWatch-friendly JSON and a derived X-Ray trace ID. |
| [`examples/azure`](examples/azure) | Azure Monitor and Application Insights operation fields. |
| [`examples/local-wrapper/applog`](examples/local-wrapper/applog) | Optional application-local logging helpers. |

## Core Wiring

Every service follows the same shape:

1. Create one logger with the selected preset.
2. Install `RequestContext` before `AccessLogger` with the same logger and preset.
3. Use `obs.Logger(ctx)` in handlers and services.

The canonical GCP wiring is:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{Preset: obs.PresetGCP})
if err != nil {
	panic(err)
}

mux := http.NewServeMux()
api := humago.New(mux, huma.DefaultConfig("Example API", "1.0.0"))
api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
```

No Google Cloud project ID is required. With valid W3C context,
`logging.googleapis.com/trace` contains the raw trace ID.

## Run The Canonical GCP Example

```bash
go run ./examples/gcp
```

Call the health route with request and trace correlation:

```bash
curl -i \
  -H 'X-Request-ID: demo-123' \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' \
  -H 'tracestate: vendor=value' \
  http://127.0.0.1:8080/health
```

The request ID remains `demo-123`; `correlation_id` becomes the W3C trace ID.
The handler and access records contain the same correlation fields. The access
record also contains `httpRequest`, `/health` as the path template, `get-health`
as Huma's operation ID, and status 200.

The health handler writes service-owned `INFO` and `DEBUG` events before the
package writes the terminal access record. The example enables debug logging,
so stdout contains three newline-delimited JSON objects in this order:

1. `health check` with `service_name`, `service_version`, and `health_status`.
2. `dependency check` with `dependency`, `dependency_status`, and
   `check_duration_ms`.
3. `request completed` with the common HTTP request fields owned by this
   package.

All three records share `request_id` and `correlation_id`. At the default info
level, the debug dependency record is omitted while the health and access
records remain. Tests exercise the real Huma route and decode the JSON output;
Cloud Logging ingestion and trace linking are intentionally outside this
repository's test boundary.

Representative GCP fields:

```json
{"severity":"INFO","message":"request completed","request_id":"demo-123","correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace_sampled":true,"method":"GET","path":"/health","path_template":"/health","operation_id":"get-health","status":200}
```

The package does not create spans and therefore does not manufacture
`logging.googleapis.com/spanId` from the incoming parent ID.

## Provider-Neutral JSON

```bash
go run ./examples/basic
```

The default preset writes `level` and the generic correlation fields without
provider-specific trace aliases.

## AWS

```bash
go run ./examples/aws
```

The AWS preset keeps flat JSON. A valid W3C trace ID is also formatted as
`xray_trace_id`, for example
`1-4bf92f35-77b34da6a3ce929d0e0e4736`. The package does not create X-Ray
segments or parse `X-Amzn-Trace-Id`.

## Azure

```bash
go run ./examples/azure
```

The Azure preset maps valid W3C values to `operation_Id` and
`operation_ParentId`. It does not initialize an Azure SDK or parse legacy
`Request-Id` headers.

## Mixed Huma And `net/http` Routes

Install the same GCP configuration at the outer router boundary when one
service has both Huma and non-Huma routes:

```go
handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
})(mux)
```

Huma `RequestContext` reuses the outer request metadata. Non-Huma access logging
remains application-owned.

## Optional Local Wrapper

[`examples/local-wrapper/applog`](examples/local-wrapper/applog) provides small
`Debug`, `Info`, `Warn`, `Error`, and arbitrary-level helpers around
`obs.Logger(ctx)`. It is a convenience layer, not required package
configuration. Passing `context.Context` keeps request and trace correlation
without coupling helpers to Huma.

```go
applog.Info(ctx, "loading item", zap.String("item_id", itemID))
applog.Error(ctx, "item load failed", err, zap.String("item_id", itemID))
```

Tests verify that the wrapper preserves request metadata, structured fields,
levels, and error information.

## Per-Project Checklist

- Use Go 1.25 or newer and Huma v2.
- Use GCP when documentation needs one representative configuration.
- Keep runnable examples limited to required package wiring.
- Use the same preset for the logger and all observability middleware.
- Install `RequestContext` before `AccessLogger`.
- Group logs by `path_template`, not the concrete request path.
- Keep provider tracing SDKs separate from this correlation package.
- Never place secrets or raw bodies in log fields.
- Run formatting, lint, tests, and race tests.

## References

- [Google Cloud: Link log entries with traces](https://docs.cloud.google.com/trace/docs/trace-log-integration)
- [Google Cloud Trace release notes](https://docs.cloud.google.com/trace/docs/release-notes)
- [Google Cloud structured logging](https://cloud.google.com/logging/docs/structured-logging)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
