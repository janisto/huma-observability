package main

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability"
)

func main() {
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetGCP,
		Level:  zapcore.DebugLevel,
	})
	if err != nil {
		panic(err)
	}

	server := &http.Server{Addr: ":8080", Handler: newGCPHandler(logger), ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func newGCPHandler(logger *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Example API", "1.0.0"))
	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: obs.PresetGCP}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger, Preset: obs.PresetGCP}))
	huma.Get(api, "/health", health)
	return mux
}

func health(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	logger := obs.Logger(ctx)
	logger.Info("health check",
		zap.String("service_name", "example-service"),
		zap.String("service_version", "1.0.0"),
		zap.String("health_status", "ok"),
	)
	logger.Debug("dependency check",
		zap.String("dependency", "database"),
		zap.String("dependency_status", "ok"),
		zap.Int64("check_duration_ms", 3),
	)
	return &healthOutput{Body: healthBody{OK: true}}, nil
}

type healthOutput struct {
	Body healthBody
}

type healthBody struct {
	OK bool `json:"ok"`
}
