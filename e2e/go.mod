module github.com/janisto/huma-observability-e2e

go 1.26.0

require (
	github.com/danielgtaylor/huma/v2 v2.30.0
	github.com/janisto/huma-observability/v2 v2.0.0
	go.uber.org/zap v1.28.0
)

require go.uber.org/multierr v1.11.0 // indirect

replace github.com/janisto/huma-observability/v2 => ..
