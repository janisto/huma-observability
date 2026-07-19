package obs

import (
	"net/http"

	"go.uber.org/zap"
)

// HTTPRequestContextConfig configures HTTPRequestContext middleware.
type HTTPRequestContextConfig struct {
	RequestIDHeader       string
	TraceparentHeader     string
	TracestateHeader      string
	TraceContextLevel     TraceContextLevel
	ResponseHeader        string
	DisableResponseHeader bool
	NewRequestID          func() string
	ValidateRequestID     func(string) bool

	Logger *zap.Logger
	Preset Preset
}

// HTTPRequestContext returns net/http middleware that installs request-scoped
// correlation metadata on the request context.
func HTTPRequestContext(config HTTPRequestContextConfig) func(http.Handler) http.Handler {
	cfg := normalizeHTTPRequestContextConfig(config)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			metadata := metadataFromContext(ctx)
			if metadata == nil || metadata.RequestID == "" {
				existing := metadata
				metadata = buildRequestMetadataFromHTTPHeader(r.Header, cfg.requestConfig)
				if existing != nil && existing.Logger != nil {
					metadata.Logger = loggerWithMetadata(existing.Logger, metadata, cfg.preset)
				}
				ctx = contextWithRequestMetadata(ctx, metadata)
			}

			if cfg.logger != nil && metadata.Logger == nil {
				ctx = contextWithRequestLogger(ctx, metadata, loggerWithMetadata(cfg.logger, metadata, cfg.preset))
			}
			if !cfg.requestConfig.DisableResponseHeader {
				w.Header().Set(cfg.requestConfig.ResponseHeader, metadata.RequestID)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type normalizedHTTPRequestContextConfig struct {
	requestConfig RequestContextConfig
	logger        *zap.Logger
	preset        Preset
}

func normalizeHTTPRequestContextConfig(config HTTPRequestContextConfig) normalizedHTTPRequestContextConfig {
	requestConfig := normalizeRequestContextConfig(RequestContextConfig{
		RequestIDHeader:       config.RequestIDHeader,
		TraceparentHeader:     config.TraceparentHeader,
		TracestateHeader:      config.TracestateHeader,
		TraceContextLevel:     config.TraceContextLevel,
		ResponseHeader:        config.ResponseHeader,
		DisableResponseHeader: config.DisableResponseHeader,
		NewRequestID:          config.NewRequestID,
		ValidateRequestID:     config.ValidateRequestID,
	})
	return normalizedHTTPRequestContextConfig{
		requestConfig: requestConfig,
		logger:        config.Logger,
		preset:        config.Preset,
	}
}

func buildRequestMetadataFromHTTPHeader(header http.Header, config RequestContextConfig) *requestMetadata {
	return buildRequestMetadataFromHeaders(
		header.Values(config.RequestIDHeader),
		header.Values(config.TraceparentHeader),
		header.Values(config.TracestateHeader),
		config,
	)
}
