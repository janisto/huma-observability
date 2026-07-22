# Consumer image

This Huma server uses a local module replacement to compile the exact package
checkout into a static, non-root distroless image. Building the image verifies
packaging and integration only; it does not run the server or inspect logs.

## Build

```sh
just e2e-image observability-e2e-local:ci
```

The recipe uses Podman when available and falls back to Docker.

## Runtime contract

The container requires these environment variables:

- `OBS_E2E_CASE` selects exactly one supported configuration:
  `common_level1`, `common_level2`, `aws_level1`, `azure_level1`, or
  `gcp_level1`.
- `OBS_E2E_SECRET_CANARY` must be nonempty and supplies the bearer-token value.

`PORT` is optional, defaults to `8080`, and must be an integer from 1 through
65535. Invalid or missing required configuration causes a nonzero exit.

Run a representative case with Podman:

```sh
podman run --rm --publish 8080:8080 \
  --env OBS_E2E_CASE=common_level1 \
  --env OBS_E2E_SECRET_CANARY=audit-canary \
  observability-e2e-local:ci
```

If Podman is unavailable, the equivalent Docker command may be used.

Send `GET /trace` with `Authorization: Bearer <OBS_E2E_SECRET_CANARY>`.
`X-Request-ID` and W3C `traceparent` may be supplied to exercise request and
trace correlation:

```sh
curl --fail-with-body http://127.0.0.1:8080/trace \
  -H 'Authorization: Bearer audit-canary' \
  -H 'X-Request-ID: audit-request-1' \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
```

A matching bearer token returns HTTP 200 with JSON containing `ok: true`, a
nonempty `request_id`, and `canary_received: true`. A missing or incorrect token
returns HTTP 401. The canary value itself is authentication input and must not
appear in emitted logs.

Normal application and access events are written to stdout as one JSON object
per line. Startup and configuration failures, plus logger-internal diagnostics,
use stderr; stderr is not part of the structured event stream.

Independent auditors may run this image and compare its output with this
package's public logging contract. Cross-implementation conclusions belong to
the auditor. External audit results are optional and do not constitute release
approval.
