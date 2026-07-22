package obs

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

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
				entry := trustedLogger(logger).Check(level, "request completed")
				if entry == nil {
					return
				}

				duration := safeDuration(cfg.Now, start, startOK)
				fields := accessLogFields(ctx, status, hasStatus, terminalReason, duration, cfg)
				if cfg.ExtraFields != nil {
					fields = appendExtraFields(fields, safeExtraFields(cfg.ExtraFields, ctx), cfg.Preset)
				}
				entry.Write(fields...)
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
	if err := validatePreset(config.Preset); err != nil {
		panic(err)
	}
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

func loggerWithMetadata(logger *zap.Logger, metadata *requestMetadata, preset Preset) *zap.Logger {
	logger, guarded := unwrapApplicationLogger(logger)
	logger = logger.With(requestMetadataFields(metadata)...)
	if metadata == nil {
		if guarded {
			return guardApplicationLogger(logger, preset)
		}
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
	if guarded {
		return guardApplicationLogger(logger, preset)
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
		if metadata.Trace.Level == TraceContextLevel2 && metadata.Trace.Version == "00" {
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
		if op.OperationID != "" {
			fields = append(fields, zap.String("operation_id", op.OperationID))
		}
	}
	peerIP := ""
	if config.CapturePeerIP {
		peerIP = directPeerIP(ctx.RemoteAddr())
	}
	if peerIP != "" {
		fields = append(fields, zap.String("peer_ip", peerIP))
	}
	userAgent := ""
	if config.CaptureUserAgent {
		userAgent, _ = singleValidUserAgent(rawHeaderValues(ctx, "User-Agent"))
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
			PeerIP:    peerIP,
			Latency:   duration,
		}))
	}
	return fields
}

func singleValidUserAgent(values []string) (string, bool) {
	value, single := singleRawHeaderValue(values)
	if !single || value == "" || !utf8.ValidString(value) || value[0] == ' ' || value[0] == '\t' ||
		value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return "", false
	}
	for _, character := range []byte(value) {
		if (character < 0x20 && character != '\t') || character == 0x7f {
			return "", false
		}
	}
	return value, true
}

func canonicalRouteTemplate(native string) (string, bool) {
	return native, native != ""
}

func appendExtraFields(fields, extra []zap.Field, preset Preset) []zap.Field {
	seen := make(map[string]struct{})
	for _, field := range fields {
		seen[field.Key] = struct{}{}
	}
	nested := false
	for _, field := range extra {
		if field.Type == zapcore.InlineMarshalerType {
			continue
		}
		if field.Type == zapcore.NamespaceType {
			if _, exists := seen[field.Key]; exists || !nested && isReservedLogField(field.Key, preset) {
				break
			}
			fields = append(fields, field)
			seen = make(map[string]struct{})
			nested = true
			continue
		}
		if _, exists := seen[field.Key]; exists {
			continue
		}
		if !nested && isReservedLogField(field.Key, preset) {
			continue
		}
		seen[field.Key] = struct{}{}
		fields = append(fields, field)
	}
	return fields
}

func isReservedLogField(key string, preset Preset) bool {
	switch key {
	case "timestamp",
		"logger",
		"caller",
		"stacktrace",
		"message",
		"request_id",
		"correlation_id",
		"trace_id",
		"parent_id",
		"trace_flags",
		"trace_sampled",
		"trace_id_random",
		"method",
		"path",
		"path_template",
		"operation_id",
		"status",
		"duration_ms",
		"terminal_reason",
		"peer_ip",
		"user_agent":
		return true
	}
	if key == "severity" {
		return preset == PresetGCP
	}
	if key == "level" {
		return preset != PresetGCP
	}
	return isSelectedProviderField(key, preset, true)
}

func isSelectedProviderField(key string, preset Preset, access bool) bool {
	switch preset {
	case PresetGCP:
		return key == "logging.googleapis.com/trace" ||
			key == "logging.googleapis.com/trace_sampled" ||
			access && key == "httpRequest"
	case PresetAWS:
		return key == "xray_trace_id"
	case PresetAzure:
		return key == "operation_Id" || key == "operation_ParentId"
	default:
		return false
	}
}

func requestPath(ctx huma.Context) string {
	url := ctx.URL()
	if url.Path == "" {
		return ""
	}
	return url.EscapedPath()
}

func directPeerIP(remoteAddr string) string {
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
	PeerIP    string
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
	if r.PeerIP != "" {
		encoder.AddString("remoteIp", r.PeerIP)
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

	var fraction string
	switch {
	case nanos%1_000_000 == 0:
		fraction = fmt.Sprintf("%03d", nanos/1_000_000)
	case nanos%1_000 == 0:
		fraction = fmt.Sprintf("%06d", nanos/1_000)
	default:
		fraction = fmt.Sprintf("%09d", nanos)
	}
	return fmt.Sprintf("%d.%ss", seconds, fraction)
}
