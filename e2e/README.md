# E2E consumer

This Huma server uses a local module replacement to compile the exact package
checkout into a static, non-root distroless image. It exposes `GET /trace` and
accepts only the five central `OBS_E2E_CASE` values.

```sh
just e2e-image observability-e2e-local:ci
```

Cross-repository assertions remain owned by the central observability project.
