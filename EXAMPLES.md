# Examples

This file shows how to wire `huma-observability` into Huma v2 services and how
to configure the same logging contract for generic JSON logs, Google Cloud,
AWS, and Azure.

The runnable examples are:

| Example | Purpose |
| --- | --- |
| [examples/basic/main.go](examples/basic/main.go) | Generic JSON logs for local development and neutral log pipelines. |
| [examples/gcp/main.go](examples/gcp/main.go) | Google Cloud Logging field shape. |
| [examples/aws/main.go](examples/aws/main.go) | AWS CloudWatch-friendly flat JSON and X-Ray trace ID field. |
| [examples/azure/main.go](examples/azure/main.go) | Azure Monitor-friendly flat JSON and Application Insights operation fields. |
| [examples/local-wrapper/applog/log.go](examples/local-wrapper/applog/log.go) | Optional project-local logging helper package. |

The module path is `github.com/janisto/huma-observability`; the Go package name
is `obs`, so code uses the `obs` identifier after importing the module path:

```go
import "github.com/janisto/huma-observability"
```

## Core Wiring

Every service follows the same shape:

1. Create a base logger with the selected preset.
2. Attach stable project fields to the base logger.
3. Install Huma `RequestContext` before Huma `AccessLogger`.
4. For mixed services, install `HTTPRequestContext` at the HTTP router
   boundary.
5. Use `obs.Logger(ctx)` in handlers and lower-level services.

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetDefault,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", envOrDefault("SERVICE_NAME", "example-api")),
	zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
	zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
}))

handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
})(mux)
```

Middleware order matters. `HTTPRequestContext` installs request metadata for
the whole HTTP surface and installs a request-scoped logger when `Logger` is
configured. Huma `RequestContext` reuses that metadata on Huma routes. Huma
`AccessLogger` emits operation-aware access logs for Huma routes only.

`HTTPRequestContext` does not emit access logs and does not wrap
`http.ResponseWriter`. If a project needs access logs for non-Huma routes, keep
that middleware in the application or router layer.

## Shared Environment Fields

Use the same field names in every service so queries and dashboards do not
become project-specific.

| Variable | Log field | Example | Notes |
| --- | --- | --- | --- |
| `SERVICE_NAME` | `service` | `catalog-api` | Stable service or app name. |
| `SERVICE_ENV` | `environment` | `local`, `dev`, `staging`, `prod` | Keep values consistent across projects. |
| `SERVICE_VERSION` | `version` | `v0.3.1`, image tag, commit SHA | Use a deployable artifact identifier. |
| `PORT` | none | `8080` | Used by the runnable examples only. |

Base logger fields appear on handler logs and Huma access logs.
`AccessLoggerConfig.ExtraFields` is access-log-only and should be used for
request/response fields that do not belong on every handler log. Extra fields
using package-owned or provider-reserved keys are ignored to avoid duplicate
JSON keys.

Do not put secrets, tokens, authorization headers, cookies, or raw request
bodies into log fields.

## Run Locally

Run the generic example:

```sh
SERVICE_NAME=example-api SERVICE_ENV=local SERVICE_VERSION=dev \
go run ./examples/basic
```

Call the Huma route with a request ID:

```sh
curl -H 'X-Request-Id: demo-123' http://localhost:8080/health
```

Expected Huma access-log shape:

```json
{"timestamp":"2026-07-07T12:00:00Z","level":"INFO","message":"request completed","service":"example-api","environment":"local","version":"dev","request_id":"demo-123","correlation_id":"demo-123","method":"GET","path":"/health","path_template":"/health","operation_id":"get-health","status":200,"duration_ms":1.2,"remote_ip":"127.0.0.1","user_agent":"curl/8.0.0"}
```

Call the non-Huma readiness route:

```sh
curl -i -H 'X-Request-Id: demo-456' http://localhost:8080/ready
```

The response includes `X-Request-Id: demo-456`, and the handler can log with
`obs.Logger(r.Context())`.

Expected non-Huma handler-log shape:

```json
{"timestamp":"2026-07-07T12:00:00Z","level":"INFO","message":"readiness check","service":"example-api","environment":"local","version":"dev","request_id":"demo-456","correlation_id":"demo-456","method":"GET","path":"/ready","status":204,"duration_ms":1.2}
```

This package does not emit an access log for `/ready`; the example logs the
route event inside the handler.

## Non-Huma Route Logging

For plain `net/http` handlers, log the route event directly. This avoids
response-writer wrapping and is enough for health checks, readiness probes,
redirects, and other small non-Huma routes:

```go
mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.WriteHeader(http.StatusNoContent)
	obs.Logger(r.Context()).Info("readiness check",
		zap.String("method", r.Method),
		zap.String("path", r.URL.EscapedPath()),
		zap.Int("status", http.StatusNoContent),
		zap.Float64("duration_ms", float64(time.Since(start))/float64(time.Millisecond)),
	)
})
```

For Chi applications that want access logs for non-Huma route groups, keep that
middleware in the application and use Chi's response-writer wrapper instead of
adding one to this package:

```go
import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/janisto/huma-observability"
	"go.uber.org/zap"
)

func chiAccessLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		obs.Logger(r.Context()).Info("http request completed",
			zap.String("method", r.Method),
			zap.String("path", r.URL.EscapedPath()),
			zap.Int("status", status),
			zap.Int("bytes_written", ww.BytesWritten()),
			zap.Float64("duration_ms", float64(time.Since(start))/float64(time.Millisecond)),
		)
	})
}

func readyHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func routes(logger *zap.Logger, api huma.API, router chi.Router) http.Handler {
	router.Use(obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
		Logger: logger,
	}))

	router.Group(func(r chi.Router) {
		r.Use(chiAccessLogger)
		r.Get("/ready", readyHandler)
	})

	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
	}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
	}))

	return router
}
```

Apply the Chi access logger only to non-Huma route groups if Huma routes already
use `obs.AccessLogger`; otherwise one request can produce two access log lines.

Send a request with W3C trace context:

```sh
curl \
  -H 'X-Request-Id: demo-123' \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' \
  http://localhost:8080/health
```

With a valid `traceparent`, handler logs and Huma access logs include:

- `request_id`: `demo-123`
- `correlation_id`: `4bf92f3577b34da6a3ce929d0e0e4736`
- `trace_id`: `4bf92f3577b34da6a3ce929d0e0e4736`
- `parent_id`: `00f067aa0ba902b7`
- `trace_flags`: `01`
- `trace_sampled`: `true`

## Handler And Service Logs

Handlers receive a standard `context.Context`. Pass it into lower-level
functions and use `obs.Logger(ctx)` there.

```go
func loadRepository(ctx context.Context, owner, repo string) error {
	obs.Logger(ctx).Info("loading repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)
	return nil
}
```

`obs.Logger(ctx)` never returns nil. Configure `RequestContextConfig.Logger` for
Huma-only services, or `HTTPRequestContextConfig.Logger` at the router boundary
for mixed Huma and non-Huma services. If a context did not pass through
configured request-context middleware, `obs.Logger(ctx)` returns a no-op logger
instead of using a package-global logger or implicit stdout fallback.

Recovery middleware that logs with `obs.Logger(r.Context())` must run after
`HTTPRequestContext` if it needs request metadata. Logs emitted before
request-context middleware may not have `request_id`.

## Request ID And Trace Headers

Defaults:

| Purpose | Header |
| --- | --- |
| Incoming request ID | `X-Request-Id` |
| Response request ID | `X-Request-Id` |
| W3C trace context | `traceparent` |
| W3C trace state | `tracestate` |

Custom ingress example:

```go
api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger:            logger,
	RequestIDHeader:   "X-Correlation-Id",
	ResponseHeader:    "X-Correlation-Id",
	TraceparentHeader: "traceparent",
	TracestateHeader:  "tracestate",
}))
```

For router-wide HTTP middleware, use the same field names on
`HTTPRequestContextConfig`.

Behavior to verify in every project:

- Invalid incoming request IDs are replaced.
- Invalid `traceparent` values are ignored.
- `CorrelationID(ctx)` is the W3C trace ID when a valid trace exists.
- `CorrelationID(ctx)` falls back to the request ID when no valid trace exists.
- Cloud-specific propagation headers are not parsed by this package.

Use OpenTelemetry or the relevant cloud instrumentation beside this package
when you need real spans, trace export, dependency telemetry, or span-level log
correlation. For Go HTTP services, `otelhttp.NewHandler` and
`otelhttp.NewTransport` are the usual OpenTelemetry HTTP primitives.

## Google Cloud

Runnable example: [examples/gcp/main.go](examples/gcp/main.go).

Use `PresetGCP` for the logger, Huma request context, Huma access logger, and
HTTP request context:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetGCP,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", envOrDefault("SERVICE_NAME", "example-api")),
	zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
	zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
	zap.String("cloud_provider", "gcp"),
	zap.String("cloud_project", os.Getenv("GOOGLE_CLOUD_PROJECT")),
	zap.String("cloud_location", os.Getenv("GOOGLE_CLOUD_LOCATION")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
```

Run locally:

```sh
SERVICE_NAME=example-api SERVICE_ENV=local SERVICE_VERSION=dev \
GOOGLE_CLOUD_PROJECT=dev-project GOOGLE_CLOUD_LOCATION=europe-north1 \
go run ./examples/gcp
```

Configure per project:

- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
- Set `GOOGLE_CLOUD_PROJECT` if you want the project ID as a queryable field.
- Set `GOOGLE_CLOUD_LOCATION` to the region or location, for example
  `europe-north1`.
- Do not depend on undocumented runtime environment variables. Set the values
  your application logs explicitly.

Cloud Run deploy example:

```sh
gcloud run deploy SERVICE \
  --image IMAGE_URL \
  --region europe-north1 \
  --set-env-vars SERVICE_NAME=SERVICE,SERVICE_ENV=prod,SERVICE_VERSION=IMAGE_TAG,GOOGLE_CLOUD_PROJECT=PROJECT_ID,GOOGLE_CLOUD_LOCATION=europe-north1
```

`--set-env-vars` replaces previously configured environment variables that are
not included in the new list. For existing services where you only want to add
or change values, use the Cloud Run update flow instead.

GCP log shape:

- Logger key is `severity`, not `level`.
- Huma access logs include Cloud Logging's `httpRequest` object.
- With a valid W3C trace ID, logs include `logging.googleapis.com/trace` using
  the raw `TRACE_ID`.
- When sampling is known, logs include `logging.googleapis.com/trace_sampled`.
- The package does not emit `logging.googleapis.com/spanId` because it does not
  create spans or read a current span from OpenTelemetry.

Logs Explorer query examples:

```text
resource.type="cloud_run_revision"
jsonPayload.service="SERVICE"
severity>=WARNING
```

```text
resource.type="cloud_run_revision"
jsonPayload.request_id="demo-123"
```

```text
resource.type="cloud_run_revision"
jsonPayload.correlation_id="4bf92f3577b34da6a3ce929d0e0e4736"
```

## AWS

Runnable example: [examples/aws/main.go](examples/aws/main.go).

Use `PresetAWS` for the logger, Huma request context, Huma access logger, and
HTTP request context:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAWS,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", envOrDefault("SERVICE_NAME", "example-api")),
	zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
	zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
	zap.String("cloud_provider", "aws"),
	zap.String("cloud_region", os.Getenv("AWS_REGION")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetAWS,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetAWS,
}))
```

Run locally:

```sh
SERVICE_NAME=example-api SERVICE_ENV=local SERVICE_VERSION=dev \
AWS_REGION=eu-north-1 \
go run ./examples/aws
```

Configure per project:

- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
- Set `AWS_REGION` if the runtime does not already provide it, or if you want
  an explicit query field.
- For ECS/Fargate, configure the container `logConfiguration` with the
  `awslogs` log driver.
- For App Runner, application output is streamed to the service's CloudWatch
  application log group.
- Keep each log event as one JSON object. CloudWatch Logs Insights discovers
  JSON fields, but it has a documented field extraction limit.

AWS trace fields:

- With a valid W3C trace, logs include `trace_id`, `parent_id`,
  `trace_flags`, and `trace_sampled`.
- The AWS preset also emits `xray_trace_id`, converting the W3C trace ID into
  X-Ray trace ID format.
- The package does not create AWS X-Ray segments and does not emit `span_id`
  from the incoming W3C parent ID.

CloudWatch Logs Insights query examples:

```sql
fields @timestamp, level, message, request_id, method, path, status, duration_ms
| filter service = "api" and status >= 500
| sort @timestamp desc
| limit 50
```

```sql
fields @timestamp, message, request_id, correlation_id
| filter request_id = "demo-123" or correlation_id = "demo-123"
| sort @timestamp asc
```

```sql
fields @timestamp, message, trace_id, xray_trace_id, trace_flags
| filter trace_id = "5759e988bd862e3fe1be46a994272793"
   or xray_trace_id = "1-5759e988-bd862e3fe1be46a994272793"
| sort @timestamp asc
```

## Azure

Runnable example: [examples/azure/main.go](examples/azure/main.go).

Use `PresetAzure` for the logger, Huma request context, Huma access logger, and
HTTP request context:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAzure,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", envOrDefault("SERVICE_NAME", "example-api")),
	zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
	zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
	zap.String("cloud_provider", "azure"),
	zap.String("cloud_region", os.Getenv("AZURE_REGION")),
	zap.String("azure_resource_group", os.Getenv("AZURE_RESOURCE_GROUP")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	Logger: logger,
	Preset: obs.PresetAzure,
}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetAzure,
}))
```

Run locally:

```sh
SERVICE_NAME=example-api SERVICE_ENV=local SERVICE_VERSION=dev \
AZURE_REGION=westeurope AZURE_RESOURCE_GROUP=dev-rg \
go run ./examples/azure
```

Configure per project:

- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
- Set `AZURE_REGION` and `AZURE_RESOURCE_GROUP` if those are useful query
  dimensions.
- For Azure Container Apps, application logs come from container stdout/stderr
  as console logs.
- Configure Azure Monitor or Log Analytics at the Container Apps
  environment/app level if you need retention, search, dashboards, or alerts.
- Do not put secrets in log fields. Use Azure secrets or Key Vault-backed
  configuration for sensitive values.

Azure trace fields:

- With a valid W3C trace, logs include `trace_id`, `parent_id`,
  `trace_flags`, and `trace_sampled`.
- The Azure preset also emits `operation_Id` from the W3C trace ID.
- It emits `operation_ParentId` from the W3C parent ID.
- Full transaction maps, dependency telemetry, and span-level log correlation
  still require Azure Monitor OpenTelemetry or Application Insights
  instrumentation.

Azure Container Apps update example:

```sh
az containerapp update \
  --name APP_NAME \
  --resource-group RESOURCE_GROUP \
  --set-env-vars SERVICE_NAME=api SERVICE_ENV=prod SERVICE_VERSION=IMAGE_TAG AZURE_REGION=westeurope AZURE_RESOURCE_GROUP=RESOURCE_GROUP
```

Log Analytics query examples:

```kusto
ContainerAppConsoleLogs_CL
| where Log_s has '"service":"api"'
| where Log_s has '"status":500'
| order by TimeGenerated desc
| take 50
```

```kusto
ContainerAppConsoleLogs_CL
| where Log_s has '"correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736"'
| order by TimeGenerated asc
```

If your Azure ingestion path parses JSON into columns, prefer querying parsed
columns or dynamic JSON fields instead of text matching in `Log_s`.

## Per-Project Checklist

Use this checklist for each service adopting the package:

1. Pick one preset: `PresetDefault`, `PresetGCP`, `PresetAWS`, or
   `PresetAzure`.
2. Set stable metadata: `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
3. Set cloud metadata if useful:
   - GCP: `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`.
   - AWS: `AWS_REGION`.
   - Azure: `AZURE_REGION`, `AZURE_RESOURCE_GROUP`.
4. Install Huma middleware in order:
   - `RequestContext(...)`
   - `AccessLogger(...)`
5. For mixed services, wrap the outer HTTP handler with
   `HTTPRequestContext(...)`.
6. Confirm the runtime writes JSON logs to stdout.
7. Confirm the platform routes stdout/stderr to the expected log destination.
8. Send a request with `X-Request-Id: demo-123`.
9. Query the log destination for `request_id=demo-123`.
10. Send a request with a valid W3C `traceparent` header.
11. Confirm `correlation_id` becomes the W3C trace ID.
12. For GCP, confirm `logging.googleapis.com/trace` uses the raw W3C trace ID.
13. For AWS, confirm `xray_trace_id` appears when W3C trace context is present.
14. For Azure, confirm `operation_Id` appears when W3C trace context is present.
15. Build alerts and dashboards on stable fields, not message text.

## Optional Local Wrapper

The package intentionally exposes Zap directly. If a project wants shorter
helpers, keep them in that project instead of adding them to the shared package.

The example wrapper exports only:

- `Log`
- `Debug`
- `Info`
- `Warn`
- `Error`

Use it like this:

```go
applog.Info(ctx, "repository loaded", zap.String("repository", "payments"))
applog.Warn(ctx, "retrying request", zap.Int("attempt", attempt))
applog.Error(ctx, "github request failed", err, zap.Int("status", status))
```

See [examples/local-wrapper/applog/log.go](examples/local-wrapper/applog/log.go).

## References

- Google Cloud trace/log linking documents raw `TRACE_ID` as the preferred log
  trace format: https://docs.cloud.google.com/trace/docs/trace-log-integration
- Google Cloud changed raw `TRACE_ID` to the preferred `LogEntry.trace` format
  in January 2026: https://docs.cloud.google.com/trace/docs/release-notes
- Google Cloud structured logging recognizes special JSON fields:
  https://docs.cloud.google.com/logging/docs/structured-logging
- Cloud Run environment variables:
  https://docs.cloud.google.com/run/docs/configuring/services/environment-variables
- AWS X-Ray documents W3C trace IDs formatted as X-Ray trace IDs:
  https://docs.aws.amazon.com/xray/latest/devguide/xray-api-sendingdata.html
- CloudWatch Logs Insights discovered JSON fields:
  https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CWL_AnalyzeLogData-discoverable-fields.html
- ECS `awslogs` log driver:
  https://docs.aws.amazon.com/AmazonECS/latest/developerguide/specify-log-config.html
- App Runner CloudWatch Logs:
  https://docs.aws.amazon.com/apprunner/latest/dg/monitor-cwl.html
- Azure Container Apps console logging:
  https://learn.microsoft.com/en-us/azure/container-apps/logging
- Azure Container Apps environment variables:
  https://learn.microsoft.com/en-us/azure/container-apps/environment-variables
- Azure Application Insights W3C correlation mapping:
  https://learn.microsoft.com/en-us/azure/azure-monitor/app/javascript-sdk-configuration
- Azure Application Insights telemetry data model:
  https://learn.microsoft.com/en-us/azure/azure-monitor/app/data-model-complete
- OpenTelemetry `otelhttp`:
  https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
