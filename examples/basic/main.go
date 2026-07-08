package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.uber.org/zap"

	"github.com/janisto/huma-observability"
)

func setup(api huma.API) (*zap.Logger, error) {
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetDefault,
	})
	if err != nil {
		return nil, err
	}
	logger = logger.With(projectFields()...)

	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
	}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
	}))

	registerRoutes(api)
	return logger, nil
}

func main() {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Example API", "1.0.0"))
	logger, err := setup(api)
	if err != nil {
		panic(err)
	}
	registerHTTPRoutes(mux)
	handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
		Logger: logger,
	})(mux)
	server := &http.Server{
		Addr:              ":" + envOrDefault("PORT", "8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	panic(server.ListenAndServe())
}

func registerRoutes(api huma.API) {
	huma.Get(api, "/health", func(ctx context.Context, input *struct{}) (*healthOutput, error) {
		obs.Logger(ctx).Info("health check")
		return &healthOutput{Body: healthBody{OK: true}}, nil
	})
}

func registerHTTPRoutes(mux *http.ServeMux) {
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
}

func projectFields() []zap.Field {
	return []zap.Field{
		zap.String("service", envOrDefault("SERVICE_NAME", "example-api")),
		zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
		zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type healthOutput struct {
	Body healthBody
}

type healthBody struct {
	OK bool `json:"ok"`
}
