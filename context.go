package obs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync/atomic"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/zap"
)

const (
	defaultRequestIDHeader   = "X-Request-Id"
	defaultTraceparentHeader = "traceparent"
	defaultTracestateHeader  = "tracestate"
	maxTracestateLen         = 512
)

var fallbackRequestIDCounter atomic.Uint64

type contextKey struct{}

type requestMetadata struct {
	RequestID     string
	CorrelationID string
	Trace         TraceContext
	Logger        *zap.Logger
}

// RequestContextConfig configures RequestContext middleware.
type RequestContextConfig struct {
	RequestIDHeader       string
	TraceparentHeader     string
	TracestateHeader      string
	ResponseHeader        string
	DisableResponseHeader bool
	NewRequestID          func() string
	ValidateRequestID     func(string) bool

	Logger *zap.Logger
	Preset Preset
}

// RequestContext returns Huma middleware that installs request-scoped
// correlation metadata on the request context.
func RequestContext(config RequestContextConfig) func(huma.Context, func(huma.Context)) {
	cfg := normalizeRequestContextConfig(config)
	return func(ctx huma.Context, next func(huma.Context)) {
		metadata := metadataFromContext(ctx.Context())
		if metadata == nil || metadata.RequestID == "" {
			existing := metadata
			metadata = buildRequestMetadata(ctx, cfg)
			if existing != nil && existing.Logger != nil {
				metadata.Logger = loggerWithMetadata(existing.Logger, metadata, cfg.Preset)
			}
			ctx = huma.WithValue(ctx, contextKey{}, metadata)
		}
		if cfg.Logger != nil && metadata.Logger == nil {
			ctx = withRequestLogger(ctx, metadata, loggerWithMetadata(cfg.Logger, metadata, cfg.Preset))
			metadata = metadataFromContext(ctx.Context())
		}
		if !cfg.DisableResponseHeader {
			ctx.SetHeader(cfg.ResponseHeader, metadata.RequestID)
		}
		next(ctx)
	}
}

// RequestID returns the validated or generated request ID for the context.
func RequestID(ctx context.Context) string {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return ""
	}
	return metadata.RequestID
}

// CorrelationID returns the trace ID when a W3C trace exists, otherwise the
// request ID.
func CorrelationID(ctx context.Context) string {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return ""
	}
	return metadata.CorrelationID
}

// Trace returns the parsed W3C trace context for the context, if one exists.
func Trace(ctx context.Context) TraceContext {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return TraceContext{}
	}
	return metadata.Trace
}

func normalizeRequestContextConfig(config RequestContextConfig) RequestContextConfig {
	if config.RequestIDHeader == "" {
		config.RequestIDHeader = defaultRequestIDHeader
	}
	if config.TraceparentHeader == "" {
		config.TraceparentHeader = defaultTraceparentHeader
	}
	if config.TracestateHeader == "" {
		config.TracestateHeader = defaultTracestateHeader
	}
	if config.ResponseHeader == "" {
		config.ResponseHeader = config.RequestIDHeader
	}
	if config.NewRequestID == nil {
		config.NewRequestID = defaultNewRequestID
	}
	if config.ValidateRequestID == nil {
		config.ValidateRequestID = DefaultValidateRequestID
	}
	return config
}

func buildRequestMetadata(ctx huma.Context, config RequestContextConfig) *requestMetadata {
	return buildRequestMetadataFromHeaders(
		ctx.Header(config.RequestIDHeader),
		ctx.Header(config.TraceparentHeader),
		ctx.Header(config.TracestateHeader),
		config,
	)
}

func buildRequestMetadataFromHeaders(
	requestID string,
	traceparent string,
	tracestate string,
	config RequestContextConfig,
) *requestMetadata {
	if !config.ValidateRequestID(requestID) {
		requestID = newValidRequestID(config.NewRequestID, config.ValidateRequestID)
	}

	trace, ok := ParseTraceparent(traceparent)
	if ok {
		if len(tracestate) <= maxTracestateLen {
			trace.Tracestate = tracestate
		}
	}

	correlationID := requestID
	if trace.Valid {
		correlationID = trace.TraceID
	}

	return &requestMetadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
		Trace:         trace,
	}
}

func ensureRequestMetadata(ctx huma.Context) (*requestMetadata, huma.Context) {
	if metadata := metadataFromContext(ctx.Context()); metadata != nil && metadata.RequestID != "" {
		return metadata, ctx
	}
	existing := metadataFromContext(ctx.Context())
	config := normalizeRequestContextConfig(RequestContextConfig{})
	metadata := buildRequestMetadata(ctx, config)
	if existing != nil && existing.Logger != nil {
		metadata.Logger = loggerWithMetadata(existing.Logger, metadata, PresetDefault)
	}
	if !config.DisableResponseHeader {
		ctx.SetHeader(config.ResponseHeader, metadata.RequestID)
	}
	return metadata, huma.WithValue(ctx, contextKey{}, metadata)
}

func withRequestLogger(ctx huma.Context, metadata *requestMetadata, logger *zap.Logger) huma.Context {
	return huma.WithContext(ctx, contextWithRequestLogger(ctx.Context(), metadata, logger))
}

func contextWithRequestMetadata(ctx context.Context, metadata *requestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, metadata)
}

func contextWithRequestLogger(ctx context.Context, metadata *requestMetadata, logger *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = noopLogger
	}
	if metadata == nil {
		metadata = metadataFromContext(ctx)
	}
	if metadata == nil {
		metadata = &requestMetadata{}
	}
	next := *metadata
	next.Logger = logger
	return contextWithRequestMetadata(ctx, &next)
}

func metadataFromContext(ctx context.Context) *requestMetadata {
	if ctx == nil {
		return nil
	}
	metadata, ok := ctx.Value(contextKey{}).(*requestMetadata)
	if !ok {
		return nil
	}
	return metadata
}

func newValidRequestID(newRequestID func() string, validate func(string) bool) string {
	for range 2 {
		id := newRequestID()
		if validate(id) {
			return id
		}
	}

	id := fallbackRequestID()
	if validate(id) {
		return id
	}
	return "00000000000000000000000000000000"
}

func defaultNewRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fallbackRequestID()
	}
	return hex.EncodeToString(bytes[:])
}

func fallbackRequestID() string {
	var bytes [16]byte
	counter := fallbackRequestIDCounter.Add(1)
	binary.BigEndian.PutUint64(bytes[8:], counter)
	return hex.EncodeToString(bytes[:])
}

// DefaultValidateRequestID validates incoming request IDs accepted by the
// default middleware configuration.
func DefaultValidateRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}
