package obs

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const xrayTraceIDTimeLen = 8

// StatusLeveler maps an HTTP response status to a Zap log level.
type StatusLeveler func(status int) zapcore.Level

// AccessLoggerConfig configures AccessLogger middleware.
type AccessLoggerConfig struct {
	Logger      *zap.Logger
	Preset      Preset
	Now         func() time.Time
	StatusLevel StatusLeveler
	ExtraFields func(huma.Context) []zap.Field
}

// AccessLogger returns Huma middleware that installs a request-scoped Zap
// logger and emits one structured access log after the handler completes.
func AccessLogger(config AccessLoggerConfig) func(huma.Context, func(huma.Context)) {
	cfg := normalizeAccessLoggerConfig(config)
	return func(ctx huma.Context, next func(huma.Context)) {
		start := cfg.Now()
		metadata, ctx := ensureRequestMetadata(ctx)

		logger := metadata.Logger
		if logger == nil {
			logger = loggerWithMetadata(cfg.Logger, metadata, cfg.Preset)
			ctx = withRequestLogger(ctx, metadata, logger)
		}

		defer func() {
			panicValue := recover()
			status := responseStatus(ctx)
			if panicValue != nil {
				status = http.StatusInternalServerError
			}

			duration := max(cfg.Now().Sub(start), 0)

			fields := accessLogFields(ctx, status, duration, cfg.Preset)
			if cfg.ExtraFields != nil {
				fields = appendExtraFields(fields, cfg.ExtraFields(ctx))
			}
			logAt(logger, cfg.StatusLevel(status), "request completed", fields...)

			if panicValue != nil {
				panic(panicValue)
			}
		}()

		next(ctx)
	}
}

func normalizeAccessLoggerConfig(config AccessLoggerConfig) AccessLoggerConfig {
	if config.Logger == nil {
		config.Logger = noopLogger
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.StatusLevel == nil {
		config.StatusLevel = DefaultStatusLevel
	}
	return config
}

// DefaultStatusLevel maps 5xx responses to error, 4xx responses to warn, and
// all other responses to info.
func DefaultStatusLevel(status int) zapcore.Level {
	switch {
	case status >= 500:
		return zapcore.ErrorLevel
	case status >= 400:
		return zapcore.WarnLevel
	default:
		return zapcore.InfoLevel
	}
}

func logAt(logger *zap.Logger, level zapcore.Level, msg string, fields ...zap.Field) {
	if logger == nil {
		logger = noopLogger
	}
	if entry := logger.Check(level, msg); entry != nil {
		entry.Write(fields...)
	}
}

func loggerWithMetadata(logger *zap.Logger, metadata *requestMetadata, preset Preset) *zap.Logger {
	if logger == nil {
		logger = noopLogger
	}
	logger = logger.With(requestMetadataFields(metadata)...)
	if metadata == nil {
		return logger
	}
	switch preset {
	case PresetGCP:
		logger = logger.With(gcpTraceFields(metadata.Trace)...)
	case PresetAWS:
		logger = logger.With(awsTraceFields(metadata.Trace)...)
	case PresetAzure:
		logger = logger.With(azureTraceFields(metadata.Trace)...)
	}
	return logger
}

func requestMetadataFields(metadata *requestMetadata) []zap.Field {
	if metadata == nil {
		return nil
	}
	fields := make([]zap.Field, 0, 6)
	if metadata.RequestID != "" {
		fields = append(fields, zap.String("request_id", metadata.RequestID))
	}
	if metadata.CorrelationID != "" {
		fields = append(fields, zap.String("correlation_id", metadata.CorrelationID))
	}
	if metadata.Trace.Valid {
		fields = append(fields,
			zap.String("trace_id", metadata.Trace.TraceID),
			zap.String("parent_id", metadata.Trace.ParentID),
			zap.String("trace_flags", metadata.Trace.Flags),
			zap.Bool("trace_sampled", metadata.Trace.Sampled),
		)
	}
	return fields
}

func accessLogFields(ctx huma.Context, status int, duration time.Duration, preset Preset) []zap.Field {
	method := ctx.Method()
	path := requestPath(ctx)
	remoteIP := remoteIP(ctx.RemoteAddr())
	userAgent := ctx.Header("User-Agent")

	fields := []zap.Field{
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status", status),
		zap.Float64("duration_ms", float64(duration)/float64(time.Millisecond)),
	}

	if op := ctx.Operation(); op != nil {
		if op.Path != "" {
			fields = append(fields, zap.String("path_template", op.Path))
		}
		if op.OperationID != "" {
			fields = append(fields, zap.String("operation_id", op.OperationID))
		}
	}
	if remoteIP != "" {
		fields = append(fields, zap.String("remote_ip", remoteIP))
	}
	if userAgent != "" {
		fields = append(fields, zap.String("user_agent", userAgent))
	}
	if preset == PresetGCP {
		fields = append(fields, zap.Object("httpRequest", gcpHTTPRequest{
			Method:    method,
			URL:       requestURL(ctx),
			Status:    status,
			UserAgent: userAgent,
			RemoteIP:  remoteIP,
			Latency:   duration,
		}))
	}
	return fields
}

func appendExtraFields(fields, extra []zap.Field) []zap.Field {
	for _, field := range extra {
		if isReservedLogField(field.Key) {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func isReservedLogField(key string) bool {
	switch key {
	case "timestamp",
		"level",
		"severity",
		"logger",
		"message",
		"request_id",
		"correlation_id",
		"trace_id",
		"parent_id",
		"trace_flags",
		"trace_sampled",
		"xray_trace_id",
		"operation_Id",
		"operation_ParentId",
		"method",
		"path",
		"path_template",
		"operation_id",
		"status",
		"duration_ms",
		"remote_ip",
		"user_agent",
		"httpRequest",
		"logging.googleapis.com/trace",
		"logging.googleapis.com/trace_sampled",
		"logging.googleapis.com/spanId":
		return true
	default:
		return false
	}
}

func responseStatus(ctx huma.Context) int {
	if status := ctx.Status(); status != 0 {
		return status
	}
	if op := ctx.Operation(); op != nil && op.DefaultStatus != 0 {
		return op.DefaultStatus
	}
	return http.StatusOK
}

func requestPath(ctx huma.Context) string {
	url := ctx.URL()
	if url.Path != "" {
		return url.EscapedPath()
	}
	if url.Opaque != "" {
		return url.Opaque
	}
	return "/"
}

func requestURL(ctx huma.Context) string {
	url := ctx.URL()
	if url.Scheme != "" && url.Host != "" {
		return url.String()
	}

	uri := url.RequestURI()
	if uri == "" {
		uri = requestPath(ctx)
	}

	host := ctx.Host()
	if host == "" {
		return uri
	}
	scheme := "http"
	if ctx.TLS() != nil {
		scheme = "https"
	}
	return scheme + "://" + host + uri
}

func remoteIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return strings.Trim(host, "[]")
	}
	if strings.HasPrefix(remoteAddr, "[") && strings.Contains(remoteAddr, "]") {
		return strings.TrimPrefix(strings.SplitN(remoteAddr, "]", 2)[0], "[")
	}
	return remoteAddr
}

func gcpTraceFields(trace TraceContext) []zap.Field {
	if !trace.Valid {
		return nil
	}

	return []zap.Field{
		zap.String("logging.googleapis.com/trace", trace.TraceID),
		zap.Bool("logging.googleapis.com/trace_sampled", trace.Sampled),
	}
}

func awsTraceFields(trace TraceContext) []zap.Field {
	if !trace.Valid {
		return nil
	}
	return []zap.Field{
		zap.String("xray_trace_id", xrayTraceIDFromW3C(trace.TraceID)),
	}
}

func azureTraceFields(trace TraceContext) []zap.Field {
	if !trace.Valid {
		return nil
	}
	return []zap.Field{
		zap.String("operation_Id", trace.TraceID),
		zap.String("operation_ParentId", trace.ParentID),
	}
}

func xrayTraceIDFromW3C(traceID string) string {
	if len(traceID) != 32 {
		return ""
	}
	return "1-" + traceID[:xrayTraceIDTimeLen] + "-" + traceID[xrayTraceIDTimeLen:]
}

type gcpHTTPRequest struct {
	Method    string
	URL       string
	Status    int
	UserAgent string
	RemoteIP  string
	Latency   time.Duration
}

func (r gcpHTTPRequest) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	if r.Method != "" {
		encoder.AddString("requestMethod", r.Method)
	}
	if r.URL != "" {
		encoder.AddString("requestUrl", r.URL)
	}
	if r.Status != 0 {
		encoder.AddInt("status", r.Status)
	}
	if r.UserAgent != "" {
		encoder.AddString("userAgent", r.UserAgent)
	}
	if r.RemoteIP != "" {
		encoder.AddString("remoteIp", r.RemoteIP)
	}
	encoder.AddString("latency", formatProtoDuration(r.Latency))
	return nil
}

func formatProtoDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}

	seconds := duration / time.Second
	nanos := duration % time.Second
	if nanos == 0 {
		return fmt.Sprintf("%ds", seconds)
	}

	fraction := fmt.Sprintf("%09d", nanos)
	fraction = strings.TrimRight(fraction, "0")
	return fmt.Sprintf("%d.%ss", seconds, fraction)
}
