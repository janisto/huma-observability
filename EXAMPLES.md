# Examples

This file shows complete setup patterns for `huma-observability` in real Huma services. The runnable examples are:

- [examples/basic/main.go](examples/basic/main.go): generic JSON logs.
- [examples/gcp/main.go](examples/gcp/main.go): Google Cloud Logging shape and trace correlation.
- [examples/aws/main.go](examples/aws/main.go): AWS CloudWatch-friendly flat JSON.
- [examples/azure/main.go](examples/azure/main.go): Azure Monitor-friendly flat JSON.
- [examples/local-wrapper/applog/log.go](examples/local-wrapper/applog/log.go): optional application-local logging helpers.

The middleware is router-agnostic Huma middleware. Install `RequestContext` before `AccessLogger` so handlers and lower-level services can call `obs.Logger(ctx)`.

The module path is `github.com/janisto/huma-observability`; the declared Go
package name is `obs`, so examples use `obs.*`.

W3C `traceparent` is the common trace-correlation input for all presets. The package validates it once, emits the common W3C fields, and then adds exactly one modern provider shape per cloud:

- GCP: raw `logging.googleapis.com/trace`.
- AWS: `xray_trace_id` derived from the W3C trace ID.
- Azure: `operation_Id` and `operation_ParentId`.

Cloud-specific propagation headers are intentionally outside this package. Use OpenTelemetry or cloud SDK instrumentation when you need spans, trace export, dependency telemetry, or automatic span-level log correlation.

## Shared Project Configuration

For each service, decide the project-owned fields once and attach them to the base logger:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetDefault,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", os.Getenv("SERVICE_NAME")),
	zap.String("environment", os.Getenv("SERVICE_ENV")),
	zap.String("version", os.Getenv("SERVICE_VERSION")),
)
```

Use stable names across projects:

- `SERVICE_NAME`: the deployed service name, for example `catalog-api`.
- `SERVICE_ENV`: `local`, `dev`, `staging`, or `prod`.
- `SERVICE_VERSION`: release tag, image tag, or commit SHA.
- `PORT`: HTTP listen port. Local examples default to `8080`.

Base logger fields appear on both handler logs and access logs. `AccessLoggerConfig.ExtraFields` is access-log-only and is best for request-specific or response-specific fields. Extra fields using package-owned or provider-reserved keys are ignored to avoid duplicate JSON keys.

## Generic JSON

Use the default preset for local development, self-hosted deployments, and log pipelines that expect plain structured JSON.

```go
api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
}))
```

Run the example:

```sh
SERVICE_NAME=example-api SERVICE_ENV=local SERVICE_VERSION=dev \
go run ./examples/basic
```

Call it:

```sh
curl -H 'X-Request-Id: demo-123' http://localhost:8080/health
```

Expected log shape:

```json
{"timestamp":"2026-07-07T12:00:00Z","level":"INFO","message":"request completed","service":"example-api","environment":"local","version":"dev","request_id":"demo-123","correlation_id":"demo-123","method":"GET","path":"/health","path_template":"/health","operation_id":"get-health","status":200,"duration_ms":1.2,"remote_ip":"127.0.0.1","user_agent":"curl/8.0.0"}
```

## Logging Inside Handlers And Services

Handlers receive the standard `context.Context`. Use `obs.Logger(ctx)` anywhere downstream:

```go
func loadRepository(ctx context.Context, owner, repo string) error {
	obs.Logger(ctx).Info("loading repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)
	return nil
}
```

Request-scoped logs include `request_id`, `correlation_id`, and W3C trace fields when a valid `traceparent` header exists.

## Request ID And Trace Headers

Default headers:

- Incoming request ID: `X-Request-Id`
- Response request ID: `X-Request-Id`
- W3C trace context: `traceparent`
- W3C tracestate: `tracestate`

Custom ingress example:

```go
api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
	RequestIDHeader:   "X-Correlation-Id",
	ResponseHeader:    "X-Correlation-Id",
	TraceparentHeader: "traceparent",
	TracestateHeader:  "tracestate",
}))
```

Validation policy:

- Invalid request IDs are replaced.
- Invalid `traceparent` values are ignored.
- `CorrelationID(ctx)` is the W3C trace ID when a valid trace exists, otherwise the request ID.

## Google Cloud

Runnable example: [examples/gcp/main.go](examples/gcp/main.go).

Use `PresetGCP` in both `NewLogger` and `AccessLogger`:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetGCP,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", os.Getenv("SERVICE_NAME")),
	zap.String("environment", os.Getenv("SERVICE_ENV")),
	zap.String("version", os.Getenv("SERVICE_VERSION")),
	zap.String("cloud_provider", "gcp"),
	zap.String("cloud_project", os.Getenv("GOOGLE_CLOUD_PROJECT")),
	zap.String("cloud_location", os.Getenv("GOOGLE_CLOUD_LOCATION")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
}))
```

Configure per project:

- Set `GOOGLE_CLOUD_PROJECT` if you want the project ID as a queryable metadata field.
- Set `GOOGLE_CLOUD_LOCATION` to the region or location, for example `europe-north1`.
- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION` for queryable service metadata.
- Do not rely on undocumented ambient environment variables. Cloud Run docs recommend not depending on environment variables that you did not set explicitly.

Cloud Run deploy example:

```sh
gcloud run deploy SERVICE \
  --image IMAGE_URL \
  --region europe-north1 \
  --set-env-vars GOOGLE_CLOUD_PROJECT=PROJECT_ID,GOOGLE_CLOUD_LOCATION=europe-north1,SERVICE_NAME=SERVICE,SERVICE_ENV=prod,SERVICE_VERSION=IMAGE_TAG
```

`--set-env-vars` replaces the configured environment variable set on deploy. For existing services where you only want to add or change values, use the Cloud Run update flow instead.

GCP output details:

- Logger key is `severity`, not `level`.
- Access logs include `httpRequest`.
- If a W3C trace ID is available, logs include `logging.googleapis.com/trace` as the raw `TRACE_ID`.
- If trace sampling is known, logs include `logging.googleapis.com/trace_sampled` from the W3C sampled flag.
- The package does not emit `logging.googleapis.com/spanId` because it does not create spans.

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

Official references:

- Google Cloud trace/log linking documents raw `TRACE_ID` as the preferred format: https://docs.cloud.google.com/trace/docs/trace-log-integration
- Google Cloud structured logging recognizes special JSON fields such as `severity`, `httpRequest`, `logging.googleapis.com/trace`, and `logging.googleapis.com/trace_sampled`: https://docs.cloud.google.com/logging/docs/structured-logging
- Cloud Logging `LogEntry` includes `httpRequest`, `trace`, `spanId`, and `traceSampled`: https://docs.cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry
- Cloud Run environment variables can be set with console, `gcloud`, or YAML: https://docs.cloud.google.com/run/docs/configuring/services/environment-variables

## AWS

Runnable example: [examples/aws/main.go](examples/aws/main.go).

Use `PresetAWS`:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAWS,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", os.Getenv("SERVICE_NAME")),
	zap.String("environment", os.Getenv("SERVICE_ENV")),
	zap.String("version", os.Getenv("SERVICE_VERSION")),
	zap.String("cloud_provider", "aws"),
	zap.String("cloud_region", os.Getenv("AWS_REGION")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetAWS,
}))
```

Configure per project:

- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
- Set `AWS_REGION` if the runtime does not already provide it or if you want an explicit query field.
- For ECS/Fargate, configure the container `logConfiguration` with the `awslogs` log driver.
- For existing App Runner services, application output is streamed to the service's CloudWatch application log group.
- Keep the log line as one JSON object per line. CloudWatch Logs Insights can discover fields in JSON log events, but has a field extraction limit, so avoid dumping large arbitrary objects into every access log.
- With a valid W3C `traceparent`, AWS logs include the common `trace_id`, `parent_id`, `trace_flags`, and `trace_sampled` fields, plus `xray_trace_id`.
- For span-level trace-to-log correlation in Application Signals, run a real OpenTelemetry or X-Ray instrumentation path. This package does not create spans or X-Ray segments.

ECS task definition fragment:

```json
{
  "containerDefinitions": [
    {
      "name": "api",
      "image": "ACCOUNT.dkr.ecr.REGION.amazonaws.com/api:TAG",
      "portMappings": [{ "containerPort": 8080 }],
      "environment": [
        { "name": "SERVICE_NAME", "value": "api" },
        { "name": "SERVICE_ENV", "value": "prod" },
        { "name": "SERVICE_VERSION", "value": "TAG" },
        { "name": "AWS_REGION", "value": "eu-north-1" }
      ],
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "awslogs-group": "/ecs/api",
          "awslogs-region": "eu-north-1",
          "awslogs-stream-prefix": "api"
        }
      }
    }
  ]
}
```

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

Official references:

- ECS containers need the `awslogs` log driver configured to send logs to CloudWatch Logs: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/specify-log-config.html
- CloudWatch Logs Insights discovers fields from JSON log events, with documented field extraction limits: https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CWL_AnalyzeLogData-discoverable-fields.html
- App Runner streams application output to CloudWatch Logs: https://docs.aws.amazon.com/apprunner/latest/dg/monitor-cwl.html
- CloudWatch Application Signals trace/log correlation uses trace context fields such as `trace_id`, `span_id`, and `trace_flags`: https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Application-Signals-TraceLogCorrelation.html
- AWS X-Ray documents W3C trace IDs formatted as X-Ray trace IDs: https://docs.aws.amazon.com/xray/latest/devguide/xray-api-sendingdata.html
- X-Ray accepts W3C-format trace ID lookup and displays the X-Ray equivalent: https://docs.aws.amazon.com/xray/latest/devguide/xray-console-traces.html

## Azure

Runnable example: [examples/azure/main.go](examples/azure/main.go).

Use `PresetAzure`:

```go
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset: obs.PresetAzure,
})
if err != nil {
	return err
}

logger = logger.With(
	zap.String("service", os.Getenv("SERVICE_NAME")),
	zap.String("environment", os.Getenv("SERVICE_ENV")),
	zap.String("version", os.Getenv("SERVICE_VERSION")),
	zap.String("cloud_provider", "azure"),
	zap.String("cloud_region", os.Getenv("AZURE_REGION")),
	zap.String("azure_resource_group", os.Getenv("AZURE_RESOURCE_GROUP")),
)

api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{}))
api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	Preset: obs.PresetAzure,
}))
```

Configure per project:

- Set `SERVICE_NAME`, `SERVICE_ENV`, and `SERVICE_VERSION`.
- Set `AZURE_REGION` and `AZURE_RESOURCE_GROUP` if those are useful query dimensions.
- For Azure Container Apps, logs come from container stdout/stderr as console logs.
- Configure Azure Monitor or Log Analytics at the Container Apps environment/app level if you need retention, search, dashboards, or alerts.
- With a valid W3C `traceparent`, Azure logs include common W3C trace fields plus Application Insights-style `operation_Id` and `operation_ParentId`.
- Full transaction maps, dependency telemetry, and span-level log correlation still require Azure Monitor OpenTelemetry or Application Insights instrumentation. This package emits structured correlation fields only.
- Do not put secrets in log fields. Use Azure secrets or Key Vault-backed configuration for sensitive values.

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
| where Log_s has '"operation_Id":"4bf92f3577b34da6a3ce929d0e0e4736"'
| order by TimeGenerated asc
```

If your Azure ingestion path parses JSON into columns, prefer querying the parsed columns or dynamic JSON fields from that table instead of text matching.

Official references:

- Azure Container Apps console logs originate from container stdout/stderr: https://learn.microsoft.com/en-us/azure/container-apps/logging
- Azure Container Apps logging options include Azure Monitor configuration: https://learn.microsoft.com/en-us/azure/container-apps/log-options
- Azure Container Apps environment variables can be set at creation or by creating a new revision: https://learn.microsoft.com/en-us/azure/container-apps/environment-variables
- Application Insights maps W3C `traceparent` trace IDs to `Operation_Id` and parent IDs to `Operation_ParentId`: https://learn.microsoft.com/en-us/azure/azure-monitor/app/javascript-sdk-configuration
- Azure Monitor `AppTraces` exposes `OperationId` and `ParentId` columns for Application Insights traces: https://learn.microsoft.com/en-us/azure/azure-monitor/reference/tables/apptraces
- Azure Monitor OpenTelemetry documentation treats log records with a valid span ID as part of a trace: https://learn.microsoft.com/en-us/azure/azure-monitor/app/opentelemetry-configuration

## Per-Project Setup Checklist

Use this checklist for each project adopting the package:

1. Pick the preset: `PresetDefault`, `PresetGCP`, `PresetAWS`, or `PresetAzure`.
2. Set project metadata env vars: `SERVICE_NAME`, `SERVICE_ENV`, `SERVICE_VERSION`.
3. Set cloud metadata env vars:
   - GCP: `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`.
   - AWS: `AWS_REGION`.
   - Azure: `AZURE_REGION`, `AZURE_RESOURCE_GROUP`.
4. Install middleware in order:
   - `RequestContext(...)`
   - `AccessLogger(...)`
5. Confirm every deployment writes JSON logs to stdout.
6. Confirm your platform routes stdout/stderr to the intended log destination.
7. Send a request with `X-Request-Id: demo-123`.
8. Query the log destination for `request_id=demo-123`.
9. Send a request with a valid W3C `traceparent` header.
10. Confirm `correlation_id` becomes the trace ID.
11. For GCP, confirm `logging.googleapis.com/trace` uses the raw W3C trace ID.
12. For AWS, confirm `xray_trace_id` appears when W3C trace context is present.
13. For Azure, confirm `operation_Id` appears when W3C trace context is present.
14. Add alerts or dashboards using stable fields, not message text.

## Optional Local Wrapper

The package intentionally exposes Zap directly. If a project wants local policy,
keep it in that project. The example wrapper only adds the helpers that tend to
pay for themselves across application code:

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
