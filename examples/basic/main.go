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
	logger, err := obs.NewLogger(obs.LoggerConfig{})
	if err != nil {
		panic(err)
	}

	server := &http.Server{
		Addr:              ":8080",
		Handler:           newBasicHandler(logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func newBasicHandler(logger *zap.Logger) http.Handler {
	return newConfiguredHandler(
		obs.RequestContextConfig{Logger: logger},
		obs.AccessLoggerConfig{Logger: logger},
	)
}

func newLevel2Handler(logger *zap.Logger) http.Handler {
	const traceContextLevel = obs.TraceContextLevel2
	return newConfiguredHandler(
		obs.RequestContextConfig{Logger: logger, TraceContextLevel: traceContextLevel},
		obs.AccessLoggerConfig{Logger: logger, TraceContextLevel: traceContextLevel},
	)
}

func newConfiguredHandler(
	requestContextConfig obs.RequestContextConfig,
	accessLoggerConfig obs.AccessLoggerConfig,
) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Example API", "1.0.0"))
	api.UseMiddleware(obs.RequestContext(requestContextConfig))
	api.UseMiddleware(obs.AccessLogger(accessLoggerConfig))
	huma.Get(api, "/health", health)
	return mux
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
