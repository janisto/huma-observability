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
}

// RequestContext returns Huma middleware that installs request-scoped
// correlation metadata on the request context.
func RequestContext(config RequestContextConfig) func(huma.Context, func(huma.Context)) {
	cfg := normalizeRequestContextConfig(config)
	return func(ctx huma.Context, next func(huma.Context)) {
		metadata := buildRequestMetadata(ctx, cfg)
		if !cfg.DisableResponseHeader {
			ctx.SetHeader(cfg.ResponseHeader, metadata.RequestID)
		}
		next(huma.WithValue(ctx, contextKey{}, metadata))
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
	requestID := ctx.Header(config.RequestIDHeader)
	if !config.ValidateRequestID(requestID) {
		requestID = newValidRequestID(config.NewRequestID, config.ValidateRequestID)
	}

	trace, ok := ParseTraceparent(ctx.Header(config.TraceparentHeader))
	if ok {
		tracestate := ctx.Header(config.TracestateHeader)
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
	if metadata := metadataFromContext(ctx.Context()); metadata != nil {
		return metadata, ctx
	}
	config := normalizeRequestContextConfig(RequestContextConfig{})
	metadata := buildRequestMetadata(ctx, config)
	if !config.DisableResponseHeader {
		ctx.SetHeader(config.ResponseHeader, metadata.RequestID)
	}
	return metadata, huma.WithValue(ctx, contextKey{}, metadata)
}

func withRequestLogger(ctx huma.Context, metadata *requestMetadata, logger *zap.Logger) huma.Context {
	if metadata == nil {
		metadata = &requestMetadata{}
	}
	next := *metadata
	next.Logger = logger
	return huma.WithValue(ctx, contextKey{}, &next)
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
