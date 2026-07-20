package main

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.uber.org/zap"

	"github.com/janisto/huma-observability/v2"
)

func main() {
	logger, err := obs.NewLogger(obs.LoggerConfig{Preset: obs.PresetAzure})
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Example API", "1.0.0"))
	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: obs.PresetAzure}))
	api.UseMiddleware(obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger, Preset: obs.PresetAzure}))
	huma.Get(api, "/health", health)

	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func health(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	obs.Logger(ctx).Info("health check")
	return &healthOutput{Body: healthBody{OK: true}}, nil
}

type healthOutput struct {
	Body healthBody
}

type healthBody struct {
	OK bool `json:"ok"`
}
