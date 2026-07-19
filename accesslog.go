package obs

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const xrayTraceIDTimeLen = 8

// StatusLeveler maps an observed normal HTTP response status to a Zap log level.
type StatusLeveler func(status int) zapcore.Level

// AccessLoggerConfig configures AccessLogger middleware.
type AccessLoggerConfig struct {
	Logger            *zap.Logger
	Preset            Preset
	GCPProfileVersion GCPProfileVersion
	TraceContextLevel TraceContextLevel
	CapturePath       bool
	CapturePeerIP     bool
	CaptureUserAgent  bool
	Now               func() time.Time
	StatusLevel       StatusLeveler
	ExtraFields       func(huma.Context) []zap.Field
}

// AccessLogger returns Huma middleware that installs a request-scoped Zap
// logger and emits one structured access log after the handler completes.
func AccessLogger(config AccessLoggerConfig) func(huma.Context, func(huma.Context)) {
	cfg := normalizeAccessLoggerConfig(config)
	return func(ctx huma.Context, next func(huma.Context)) {
		start, startOK := safeNow(cfg.Now)
		metadata, ctx := ensureRequestMetadata(ctx, cfg.Preset, cfg.TraceContextLevel)

		logger := metadata.Logger
		if logger == nil {
			logger = loggerWithMetadata(cfg.Logger, metadata, cfg.Preset)
			ctx = withRequestLogger(ctx, metadata, logger)
		}

		defer func() {
			panicValue := recover()
			writeAccessLog := func() {
				status := ctx.Status()
				hasStatus := status != 0
				terminalReason := ""
				level := zapcore.InfoLevel
				if panicValue != nil {
					terminalReason = "panic"
					level = zapcore.ErrorLevel
				} else if hasStatus {
					level = safeStatusLevel(cfg.StatusLevel, status)
				}

				duration := safeDuration(cfg.Now, start, startOK)
				fields := accessLogFields(ctx, status, hasStatus, terminalReason, duration, cfg)
				if cfg.ExtraFields != nil {
					fields = appendExtraFields(fields, safeExtraFields(cfg.ExtraFields, ctx))
				}
				logAt(logger, level, "request completed", fields...)
			}
			containAccessLog(writeAccessLog)
			if panicValue != nil {
				panic(panicValue)
			}
		}()

		next(ctx)
	}
}

func normalizeAccessLoggerConfig(config AccessLoggerConfig) AccessLoggerConfig {
	traceLevel, err := ResolveTraceContextLevel(config.TraceContextLevel)
	if err != nil {
		panic(err)
	}
	config.TraceContextLevel = traceLevel
	version, err := ResolveGCPProfileVersion(config.Preset, config.GCPProfileVersion)
	if err != nil {
		panic(err)
	}
	config.GCPProfileVersion = version
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

func safeNow(now func() time.Time) (value time.Time, ok bool) {
	defer func() {
		if recover() != nil {
			value = time.Time{}
			ok = false
		}
	}()
	return now(), true
}

func safeDuration(now func() time.Time, start time.Time, startOK bool) time.Duration {
	if !startOK {
		return 0
	}
	finished, ok := safeNow(now)
	if !ok {
		return 0
	}
	return max(finished.Sub(start), 0)
}

func safeStatusLevel(mapper StatusLeveler, status int) (level zapcore.Level) {
	defer func() {
		if recover() != nil {
			level = DefaultStatusLevel(status)
		}
	}()
	level = mapper(status)
	switch level {
	case zapcore.DebugLevel, zapcore.InfoLevel, zapcore.WarnLevel, zapcore.ErrorLevel:
		return level
	default:
		return DefaultStatusLevel(status)
	}
}

func safeExtraFields(callback func(huma.Context) []zap.Field, ctx huma.Context) (fields []zap.Field) {
	defer func() {
		if recover() != nil {
			fields = nil
		}
	}()
	return callback(ctx)
}

func containAccessLog(write func()) (completed bool) {
	defer func() {
		if recover() != nil {
			completed = false
		}
	}()
	write()
	return true
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
	fields := make([]zap.Field, 0, 7)
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
		if metadata.Trace.Level == TraceContextLevel2 {
			fields = append(fields, zap.Bool("trace_id_random", metadata.Trace.Random))
		}
	}
	return fields
}

func accessLogFields(
	ctx huma.Context,
	status int,
	hasStatus bool,
	terminalReason string,
	duration time.Duration,
	config AccessLoggerConfig,
) []zap.Field {
	method := ctx.Method()

	fields := []zap.Field{
		zap.String("method", method),
		zap.Float64("duration_ms", float64(duration)/float64(time.Millisecond)),
	}
	if hasStatus {
		fields = append(fields, zap.Int("status", status))
	}
	if terminalReason != "" {
		fields = append(fields, zap.String("terminal_reason", terminalReason))
	}
	path := ""
	if config.CapturePath {
		path = requestPath(ctx)
		if path != "" {
			fields = append(fields, zap.String("path", path))
		}
	}

	if op := ctx.Operation(); op != nil {
		if pathTemplate, ok := canonicalRouteTemplate(op.Path); ok {
			fields = append(fields, zap.String("path_template", pathTemplate))
		}
		if validMetadataString(op.OperationID) {
			fields = append(fields, zap.String("operation_id", op.OperationID))
		}
	}
	peerIP := ""
	if config.CapturePeerIP {
		peerIP = remoteIP(ctx.RemoteAddr())
	}
	if peerIP != "" {
		fields = append(fields, zap.String("peer_ip", peerIP))
	}
	userAgent := ""
	if config.CaptureUserAgent {
		userAgent, _ = singleValidHeaderValue(rawHeaderValues(ctx, "User-Agent"))
	}
	if userAgent != "" {
		fields = append(fields, zap.String("user_agent", userAgent))
	}
	if config.Preset == PresetGCP {
		fields = append(fields, zap.Object("httpRequest", gcpHTTPRequest{
			Method:    method,
			URL:       path,
			Status:    status,
			UserAgent: userAgent,
			RemoteIP:  peerIP,
			Latency:   duration,
		}))
	}
	return fields
}

func validMetadataString(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func singleValidHeaderValue(values []string) (string, bool) {
	value, single := singleRawHeaderValue(values)
	if !single || !validMetadataString(value) {
		return "", false
	}
	return value, true
}

func canonicalRouteTemplate(native string) (string, bool) {
	if !strings.HasPrefix(native, "/") || strings.ContainsAny(native, "?#") {
		return "", false
	}
	for segment := range strings.SplitSeq(strings.TrimPrefix(native, "/"), "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			if !isRouteParameterName(name) {
				return "", false
			}
			continue
		}
		if strings.ContainsAny(segment, "{}*") {
			return "", false
		}
	}
	return native, true
}

func isRouteParameterName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, char := range []byte(name) {
		letter := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
		if !letter && char != '_' && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func appendExtraFields(fields, extra []zap.Field) []zap.Field {
	seen := make(map[string]struct{})
	for _, field := range fields {
		seen[field.Key] = struct{}{}
	}
	for _, field := range extra {
		if isReservedLogField(field.Key) {
			continue
		}
		if _, exists := seen[field.Key]; exists {
			continue
		}
		seen[field.Key] = struct{}{}
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
		"trace_id_random",
		"xray_trace_id",
		"operation_Id",
		"operation_ParentId",
		"method",
		"path",
		"path_template",
		"operation_id",
		"status",
		"duration_ms",
		"terminal_reason",
		"peer_ip",
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

func requestPath(ctx huma.Context) string {
	url := ctx.URL()
	if url.Path == "" {
		return ""
	}
	path := url.EscapedPath()
	if url.RawPath != "" && path != url.RawPath {
		return ""
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "#") {
		return ""
	}
	return path
}

func remoteIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host := remoteAddr
	if splitHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = splitHost
	} else if strings.HasPrefix(remoteAddr, "[") && strings.HasSuffix(remoteAddr, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(remoteAddr, "["), "]")
	}
	if strings.Contains(host, "%") {
		return ""
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return address.String()
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
