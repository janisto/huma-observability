package obs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync/atomic"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/zap"
)

const (
	defaultRequestIDHeader   = "X-Request-Id"
	defaultTraceparentHeader = "traceparent"
	defaultTracestateHeader  = "tracestate"
)

var fallbackRequestIDCounter atomic.Uint64

type contextKey struct{}

type requestMetadata struct {
	RequestID         string
	CorrelationID     string
	Trace             TraceContext
	TraceContextLevel TraceContextLevel
	Logger            *zap.Logger
}

// RequestContextConfig configures RequestContext middleware.
type RequestContextConfig struct {
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
		} else {
			requireMatchingTraceContextLevel(metadata, cfg.TraceContextLevel)
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
	if err := validatePreset(config.Preset); err != nil {
		panic(err)
	}
	level, err := ResolveTraceContextLevel(config.TraceContextLevel)
	if err != nil {
		panic(err)
	}
	config.TraceContextLevel = level
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
		rawHeaderValues(ctx, config.RequestIDHeader),
		rawHeaderValues(ctx, config.TraceparentHeader),
		rawHeaderValues(ctx, config.TracestateHeader),
		config,
	)
}

func rawHeaderValues(ctx huma.Context, name string) []string {
	var values []string
	ctx.EachHeader(func(headerName, value string) {
		if strings.EqualFold(headerName, name) {
			values = append(values, value)
		}
	})
	return values
}

func buildRequestMetadataFromHeaders(
	requestIDValues []string,
	traceparentValues []string,
	tracestateValues []string,
	config RequestContextConfig,
) *requestMetadata {
	requestID, singleRequestID := singleRawHeaderValue(requestIDValues)
	if !singleRequestID || !validIncomingRequestID(requestID, config.ValidateRequestID) {
		requestID = newValidRequestID(config.NewRequestID)
	}

	var trace TraceContext
	if traceparent, singleTraceparent := singleRawHeaderValue(traceparentValues); singleTraceparent {
		trace, _ = ParseTraceparentWithLevel(traceparent, config.TraceContextLevel)
	}
	if trace.Valid {
		if tracestate, valid := parseTracestate(tracestateValues, config.TraceContextLevel); valid {
			trace.Tracestate = tracestate
		}
	}

	correlationID := requestID
	if trace.Valid {
		correlationID = trace.TraceID
	}

	return &requestMetadata{
		RequestID:         requestID,
		CorrelationID:     correlationID,
		Trace:             trace,
		TraceContextLevel: config.TraceContextLevel,
	}
}

func requireMatchingTraceContextLevel(metadata *requestMetadata, expected TraceContextLevel) {
	actual, err := ResolveTraceContextLevel(metadata.TraceContextLevel)
	if err != nil || actual != expected {
		panic("trace context level mismatch between RequestContext and AccessLogger")
	}
}

func singleRawHeaderValue(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func ensureRequestMetadata(
	ctx huma.Context,
	preset Preset,
	traceContextLevel TraceContextLevel,
) (*requestMetadata, huma.Context) {
	config := normalizeRequestContextConfig(RequestContextConfig{TraceContextLevel: traceContextLevel})
	if metadata := metadataFromContext(ctx.Context()); metadata != nil && metadata.RequestID != "" {
		requireMatchingTraceContextLevel(metadata, config.TraceContextLevel)
		return metadata, ctx
	}
	existing := metadataFromContext(ctx.Context())
	metadata := buildRequestMetadata(ctx, config)
	if existing != nil && existing.Logger != nil {
		metadata.Logger = loggerWithMetadata(existing.Logger, metadata, preset)
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

func newValidRequestID(newRequestID func() string) string {
	for range 2 {
		if id, ok := callRequestIDGenerator(newRequestID); ok {
			return id
		}
	}

	id := fallbackRequestID()
	if DefaultValidateRequestID(id) {
		return id
	}
	return "00000000000000000000000000000000"
}

func validIncomingRequestID(value string, validate func(string) bool) (valid bool) {
	if !nativeSafeRequestID(value) {
		return false
	}
	defer func() {
		if recover() != nil {
			valid = false
		}
	}()
	return validate(value)
}

func nativeSafeRequestID(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < 0x20 && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
}

func callRequestIDGenerator(newRequestID func() string) (id string, valid bool) {
	defer func() {
		if recover() != nil {
			id = ""
			valid = false
		}
	}()
	id = newRequestID()
	return id, DefaultValidateRequestID(id)
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
