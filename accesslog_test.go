package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestAccessLoggerIntegrationLogsAccessAndHandlerLines(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	_, api := humatest.New(t)
	api.UseMiddleware(RequestContext(RequestContextConfig{}))
	api.UseMiddleware(AccessLogger(AccessLoggerConfig{
		Logger: logger,
		Now: fixedClock(
			time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 12, 0, 0, int(25*time.Millisecond), time.UTC),
		),
		ExtraFields: func(ctx huma.Context) []zap.Field {
			return []zap.Field{zap.String("tenant_id", "tenant-1")}
		},
	}))

	huma.Register(api, huma.Operation{
		OperationID: "get-widget",
		Method:      http.MethodGet,
		Path:        "/widgets/{id}",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	},
	) (*testOutput, error) {
		Logger(ctx).Info("handler log", zap.String("widget_id", input.ID))
		return &testOutput{Body: testBody{OK: true}}, nil
	})

	resp := api.Get("/widgets/123", "X-Request-Id: req-123", "User-Agent: observability-test")
	if resp.Code != http.StatusOK {
		t.Fatalf("response status = %d, want 200", resp.Code)
	}
	if got := resp.Header().Get(defaultRequestIDHeader); got != "req-123" {
		t.Fatalf("response request ID = %q", got)
	}

	lines := decodeLogLines(t, buffer.String())
	if len(lines) != 2 {
		t.Fatalf("log line count = %d, want 2; lines=%#v", len(lines), lines)
	}

	handler := lines[0]
	if got := handler["message"]; got != "handler log" {
		t.Fatalf("handler message = %v", got)
	}
	if got := handler["request_id"]; got != "req-123" {
		t.Fatalf("handler request_id = %v", got)
	}
	if got := handler["correlation_id"]; got != "req-123" {
		t.Fatalf("handler correlation_id = %v", got)
	}
	if got := handler["widget_id"]; got != "123" {
		t.Fatalf("handler widget_id = %v", got)
	}
	if _, ok := handler["tenant_id"]; ok {
		t.Fatalf("handler log unexpectedly included access-only extra field: %#v", handler)
	}

	access := lines[1]
	assertAccessField(t, access, "message", "request completed")
	assertAccessField(t, access, "level", "INFO")
	assertAccessField(t, access, "request_id", "req-123")
	assertAccessField(t, access, "correlation_id", "req-123")
	assertAccessField(t, access, "method", http.MethodGet)
	assertAccessField(t, access, "path", "/widgets/123")
	assertAccessField(t, access, "path_template", "/widgets/{id}")
	assertAccessField(t, access, "operation_id", "get-widget")
	assertAccessField(t, access, "status", float64(http.StatusOK))
	assertAccessField(t, access, "duration_ms", float64(25))
	assertAccessField(t, access, "remote_ip", "127.0.0.1")
	assertAccessField(t, access, "user_agent", "observability-test")
	assertAccessField(t, access, "tenant_id", "tenant-1")
}

func TestAccessLoggerUsesInstalledRequestLoggerBeforeConfigLogger(t *testing.T) {
	t.Parallel()

	var requestBuffer bytes.Buffer
	requestLogger, err := NewLogger(LoggerConfig{Writer: &requestBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	var fallbackBuffer bytes.Buffer
	fallbackLogger, err := NewLogger(LoggerConfig{Writer: &fallbackBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-installed-logger",
	})

	RequestContext(RequestContextConfig{
		Logger: requestLogger.With(zap.String("logger_source", "request-context")),
	})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{
			Logger: fallbackLogger.With(zap.String("logger_source", "access-config")),
			Now: fixedClock(
				time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 7, 12, 0, 0, int(5*time.Millisecond), time.UTC),
			),
		})(ctx, func(ctx huma.Context) {
			Logger(ctx.Context()).Info("handler log")
		})
	})

	lines := decodeLogLines(t, requestBuffer.String())
	if len(lines) != 2 {
		t.Fatalf("request logger line count = %d, want 2; lines=%#v", len(lines), lines)
	}
	for _, entry := range lines {
		assertAccessField(t, entry, "logger_source", "request-context")
		assertAccessField(t, entry, "request_id", "req-installed-logger")
	}
	if got := strings.TrimSpace(fallbackBuffer.String()); got != "" {
		t.Fatalf("AccessLoggerConfig logger was used despite installed request logger: %s", got)
	}
}

func TestAccessLoggerStatusLevelsAndCustomLeveler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		level       zapcore.LevelEnabler
		statusLevel StatusLeveler
		wantLevel   string
	}{
		{name: "success info", status: http.StatusNoContent, wantLevel: "INFO"},
		{name: "client error warn", status: http.StatusNotFound, wantLevel: "WARN"},
		{name: "server error level", status: http.StatusInternalServerError, wantLevel: "ERROR"},
		{
			name:   "custom debug",
			status: http.StatusTeapot,
			level:  zapcore.DebugLevel,
			statusLevel: func(int) zapcore.Level {
				return zapcore.DebugLevel
			},
			wantLevel: "DEBUG",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Writer: &buffer, Level: tt.level})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
				defaultRequestIDHeader: "req-status",
			})
			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger:      logger,
					Now:         fixedClock(time.Unix(0, 0), time.Unix(0, int64(time.Millisecond))),
					StatusLevel: tt.statusLevel,
				})(ctx, func(ctx huma.Context) {
					ctx.SetStatus(tt.status)
				})
			})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, "status", float64(tt.status))
			assertAccessField(t, entry, "level", tt.wantLevel)
		})
	}
}

func TestAccessLoggerPanicLogsAndRethrows(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/panic", map[string]string{
		defaultRequestIDHeader: "req-panic",
	})

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
			AccessLogger(AccessLoggerConfig{
				Logger: logger,
				Now:    fixedClock(time.Unix(0, 0), time.Unix(0, int64(3*time.Millisecond))),
			})(ctx, func(huma.Context) {
				panic("boom")
			})
		})
	}()

	if recovered != "boom" {
		t.Fatalf("recovered panic = %#v, want boom", recovered)
	}
	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "level", "ERROR")
	assertAccessField(t, entry, "status", float64(http.StatusInternalServerError))
	assertAccessField(t, entry, "request_id", "req-panic")
}

func TestAccessLoggerClampsNegativeDuration(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", nil)

	RequestContext(
		RequestContextConfig{NewRequestID: func() string { return "req-negative" }},
	)(
		ctx,
		func(ctx huma.Context) {
			AccessLogger(AccessLoggerConfig{
				Logger: logger,
				Now: fixedClock(
					time.Date(2026, 7, 7, 12, 0, 1, 0, time.UTC),
					time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
				),
			})(ctx, func(huma.Context) {})
		},
	)

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "duration_ms", float64(0))
}

func TestAccessLoggerDoesNotDuplicateOwnedRequestFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-dup",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(huma.Context) {})
	})

	line := strings.TrimSpace(buffer.String())
	if got := strings.Count(line, `"request_id"`); got != 1 {
		t.Fatalf("request_id key count = %d, want 1; line=%s", got, line)
	}
	if got := strings.Count(line, `"correlation_id"`); got != 1 {
		t.Fatalf("correlation_id key count = %d, want 1; line=%s", got, line)
	}
}

func TestAccessLoggerFiltersReservedExtraFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-extra",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			ExtraFields: func(huma.Context) []zap.Field {
				return []zap.Field{
					zap.String("request_id", "bad-override"),
					zap.Int("status", 599),
					zap.String("httpRequest", "bad-override"),
					zap.String("logger", "bad-override"),
					zap.String("tenant_id", "tenant-1"),
				}
			},
		})(ctx, func(huma.Context) {})
	})

	line := strings.TrimSpace(buffer.String())
	if got := strings.Count(line, `"request_id"`); got != 1 {
		t.Fatalf("request_id key count = %d, want 1; line=%s", got, line)
	}
	if got := strings.Count(line, `"status"`); got != 1 {
		t.Fatalf("status key count = %d, want 1; line=%s", got, line)
	}
	if strings.Contains(line, "bad-override") {
		t.Fatalf("reserved extra field override leaked into access log: %s", line)
	}

	entry := decodeSingleLogLine(t, line)
	assertAccessField(t, entry, "request_id", "req-extra")
	assertAccessField(t, entry, "status", float64(http.StatusOK))
	assertAccessField(t, entry, "tenant_id", "tenant-1")
}

func TestAccessLoggerDoesNotMutateExistingMetadata(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-clone",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		original := metadataFromContext(ctx.Context())
		if original == nil {
			t.Fatal("RequestContext did not install metadata")
		}

		AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(next huma.Context) {
			if Logger(next.Context()) == nil {
				t.Fatal("AccessLogger did not install a request logger")
			}
		})

		if original.Logger != nil {
			t.Fatal("AccessLogger mutated metadata already stored on the incoming context")
		}
	})
}

func TestAccessLoggerNilBaseLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	ctx, _ := newHumaTestContext(http.MethodGet, "/test", nil)
	AccessLogger(AccessLoggerConfig{})(ctx, func(huma.Context) {})
}

func TestAccessLoggerGCPFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx, _ := newHumaTestContext(http.MethodGet, "/gcp?x=1", map[string]string{
		defaultRequestIDHeader:   "req-gcp",
		defaultTraceparentHeader: traceparent,
		"User-Agent":             "gcp-test",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			Preset: PresetGCP,
			Now:    fixedClock(time.Unix(0, 0), time.Unix(0, int64(1500*time.Millisecond))),
		})(ctx, func(huma.Context) {})
	})

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "severity", "INFO")
	if _, ok := entry["level"]; ok {
		t.Fatalf("GCP entry unexpectedly has level key: %#v", entry)
	}
	assertAccessField(t, entry, "request_id", "req-gcp")
	assertAccessField(t, entry, "correlation_id", "4bf92f3577b34da6a3ce929d0e0e4736")
	assertAccessField(t, entry, "logging.googleapis.com/trace", "4bf92f3577b34da6a3ce929d0e0e4736")
	assertAccessField(t, entry, "logging.googleapis.com/trace_sampled", true)
	if _, ok := entry["logging.googleapis.com/spanId"]; ok {
		t.Fatalf("GCP entry must not emit spanId from parent ID: %#v", entry)
	}

	httpRequest, ok := entry["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
	}
	assertAccessField(t, httpRequest, "requestMethod", http.MethodGet)
	assertAccessField(t, httpRequest, "requestUrl", "http://example.com/gcp?x=1")
	assertAccessField(t, httpRequest, "status", float64(http.StatusOK))
	assertAccessField(t, httpRequest, "userAgent", "gcp-test")
	assertAccessField(t, httpRequest, "remoteIp", "203.0.113.9")
	assertAccessField(t, httpRequest, "latency", "1.5s")
}

func TestProviderTraceHeadersWithoutW3CAreIgnored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		preset       Preset
		header       string
		value        string
		rejectFields []string
	}{
		{
			name:   "gcp cloud trace header",
			preset: PresetGCP,
			header: "X-Cloud-Trace-Context",
			value:  "cccccccccccccccccccccccccccccccc/123;o=1",
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"logging.googleapis.com/trace",
				"logging.googleapis.com/trace_sampled",
			},
		},
		{
			name:   "aws xray header",
			preset: PresetAWS,
			header: "X-Amzn-Trace-Id",
			value:  "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1",
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"xray_trace_id",
			},
		},
		{
			name:   "azure legacy request-id header",
			preset: PresetAzure,
			header: "Request-Id",
			value:  "|4bf92f3577b34da6a3ce929d0e0e4736.00f067aa0ba902b7.",
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"operation_Id",
				"operation_ParentId",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: tt.preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			headers := map[string]string{
				defaultRequestIDHeader: "req-provider-header",
				tt.header:              tt.value,
			}
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", headers)

			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger,
					Preset: tt.preset,
				})(ctx, func(huma.Context) {})
			})

			entry := decodeSingleLogLine(t, buffer.String())
			for _, field := range tt.rejectFields {
				if _, ok := entry[field]; ok {
					t.Fatalf("provider-specific header produced %s without W3C traceparent: %#v", field, entry)
				}
			}
		})
	}
}

func TestInvalidW3CTraceparentSuppressesCloudCorrelationFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		preset       Preset
		headers      map[string]string
		rejectFields []string
	}{
		{
			name:   "gcp",
			preset: PresetGCP,
			headers: map[string]string{
				"X-Cloud-Trace-Context": "cccccccccccccccccccccccccccccccc/123;o=1",
			},
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"logging.googleapis.com/trace",
				"logging.googleapis.com/trace_sampled",
				"logging.googleapis.com/spanId",
			},
		},
		{
			name:   "aws",
			preset: PresetAWS,
			headers: map[string]string{
				"X-Amzn-Trace-Id": "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1",
			},
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"xray_trace_id",
			},
		},
		{
			name:   "azure",
			preset: PresetAzure,
			headers: map[string]string{
				"Request-Id": "|4bf92f3577b34da6a3ce929d0e0e4736.00f067aa0ba902b7.",
			},
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"operation_Id",
				"operation_ParentId",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: tt.preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			headers := map[string]string{
				defaultRequestIDHeader:   "req-invalid-trace",
				defaultTraceparentHeader: "not-a-traceparent",
			}
			maps.Copy(headers, tt.headers)
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", headers)

			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger,
					Preset: tt.preset,
				})(ctx, func(huma.Context) {})
			})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, "correlation_id", "req-invalid-trace")
			for _, field := range tt.rejectFields {
				assertNoAccessField(t, entry, field)
			}
		})
	}
}

func TestAccessLoggerAWSAndAzureStayFlatJSON(t *testing.T) {
	t.Parallel()

	for _, preset := range []Preset{PresetAWS, PresetAzure} {
		t.Run(string(preset), func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			ctx, _ := newHumaTestContext(http.MethodPost, "/flat", map[string]string{
				defaultRequestIDHeader: "req-flat",
			})
			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{Logger: logger, Preset: preset})(ctx, func(ctx huma.Context) {
					ctx.SetStatus(http.StatusCreated)
				})
			})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, "level", "INFO")
			assertAccessField(t, entry, "method", http.MethodPost)
			assertAccessField(t, entry, "status", float64(http.StatusCreated))
			if _, ok := entry["httpRequest"]; ok {
				t.Fatalf("%s preset unexpectedly emitted GCP httpRequest: %#v", preset, entry)
			}
			if _, ok := entry["severity"]; ok {
				t.Fatalf("%s preset unexpectedly emitted severity: %#v", preset, entry)
			}
		})
	}
}

func TestAccessLoggerProviderTraceFieldsOnHandlerAndAccessLines(t *testing.T) {
	t.Parallel()

	const (
		traceID  = "4efaaf4d1e8720b39541901950019ee5"
		parentID = "00f067aa0ba902b7"
	)

	tests := []struct {
		name       string
		preset     Preset
		flags      string
		sampled    bool
		wantFields map[string]any
		rejectKeys []string
		wantLevel  string
		levelKey   string
	}{
		{
			name:    "gcp raw trace id unsampled",
			preset:  PresetGCP,
			flags:   "00",
			sampled: false,
			wantFields: map[string]any{
				"logging.googleapis.com/trace":         traceID,
				"logging.googleapis.com/trace_sampled": false,
			},
			rejectKeys: []string{
				"logging.googleapis.com/spanId",
				"xray_trace_id",
				"operation_Id",
				"operation_ParentId",
			},
			wantLevel: "INFO",
			levelKey:  "severity",
		},
		{
			name:    "aws xray id from w3c",
			preset:  PresetAWS,
			flags:   "01",
			sampled: true,
			wantFields: map[string]any{
				"xray_trace_id": "1-4efaaf4d-1e8720b39541901950019ee5",
			},
			rejectKeys: []string{
				"logging.googleapis.com/trace",
				"logging.googleapis.com/trace_sampled",
				"logging.googleapis.com/spanId",
				"xray_parent_id",
				"operation_Id",
				"operation_ParentId",
			},
			wantLevel: "INFO",
			levelKey:  "level",
		},
		{
			name:    "azure operation fields from w3c",
			preset:  PresetAzure,
			flags:   "01",
			sampled: true,
			wantFields: map[string]any{
				"operation_Id":       traceID,
				"operation_ParentId": parentID,
			},
			rejectKeys: []string{
				"logging.googleapis.com/trace",
				"logging.googleapis.com/trace_sampled",
				"logging.googleapis.com/spanId",
				"xray_trace_id",
			},
			wantLevel: "INFO",
			levelKey:  "level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: tt.preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			traceparent := "00-" + traceID + "-" + parentID + "-" + tt.flags
			ctx, _ := newHumaTestContext(http.MethodGet, "/cloud", map[string]string{
				defaultRequestIDHeader:   "req-cloud",
				defaultTraceparentHeader: traceparent,
				"Request-Id":             "|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbb.",
				"X-Amzn-Trace-Id":        "Root=1-aaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbb;Parent=1111111111111111;Sampled=0",
				"X-Cloud-Trace-Context":  "cccccccccccccccccccccccccccccccc/123;o=1",
			})

			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{Logger: logger, Preset: tt.preset})(ctx, func(ctx huma.Context) {
					Logger(ctx.Context()).Info("handler cloud log")
				})
			})

			lines := decodeLogLines(t, buffer.String())
			if len(lines) != 2 {
				t.Fatalf("log line count = %d, want 2; lines=%#v", len(lines), lines)
			}
			for _, entry := range lines {
				assertAccessField(t, entry, tt.levelKey, tt.wantLevel)
				assertAccessField(t, entry, "trace_id", traceID)
				assertAccessField(t, entry, "parent_id", parentID)
				assertAccessField(t, entry, "trace_flags", tt.flags)
				assertAccessField(t, entry, "trace_sampled", tt.sampled)
				for key, want := range tt.wantFields {
					assertAccessField(t, entry, key, want)
				}
				for _, key := range tt.rejectKeys {
					assertNoAccessField(t, entry, key)
				}
			}
		})
	}
}

func TestAccessLoggerAWSW3CTraceFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetAWS, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/aws", map[string]string{
		defaultRequestIDHeader:   "req-aws",
		defaultTraceparentHeader: "00-4efaaf4d1e8720b39541901950019ee5-00f067aa0ba902b7-01",
		"X-Amzn-Trace-Id":        "Root=1-aaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbb;Parent=1111111111111111;Sampled=0",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{Logger: logger, Preset: PresetAWS})(ctx, func(huma.Context) {})
	})

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "level", "INFO")
	assertAccessField(t, entry, "trace_id", "4efaaf4d1e8720b39541901950019ee5")
	assertAccessField(t, entry, "parent_id", "00f067aa0ba902b7")
	assertAccessField(t, entry, "trace_flags", "01")
	assertAccessField(t, entry, "trace_sampled", true)
	assertAccessField(t, entry, "xray_trace_id", "1-4efaaf4d-1e8720b39541901950019ee5")
	if _, ok := entry["xray_parent_id"]; ok {
		t.Fatalf("AWS W3C trace unexpectedly emitted X-Ray parent field: %#v", entry)
	}
	if _, ok := entry["httpRequest"]; ok {
		t.Fatalf("AWS preset unexpectedly emitted GCP httpRequest: %#v", entry)
	}
}

func TestAccessLoggerAzureTraceFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetAzure, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/azure", map[string]string{
		defaultRequestIDHeader:   "req-azure",
		defaultTraceparentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{Logger: logger, Preset: PresetAzure})(ctx, func(huma.Context) {})
	})

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "level", "INFO")
	assertAccessField(t, entry, "trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")
	assertAccessField(t, entry, "parent_id", "00f067aa0ba902b7")
	assertAccessField(t, entry, "trace_flags", "00")
	assertAccessField(t, entry, "trace_sampled", false)
	assertAccessField(t, entry, "operation_Id", "4bf92f3577b34da6a3ce929d0e0e4736")
	assertAccessField(t, entry, "operation_ParentId", "00f067aa0ba902b7")
	if _, ok := entry["httpRequest"]; ok {
		t.Fatalf("Azure preset unexpectedly emitted GCP httpRequest: %#v", entry)
	}
	if _, ok := entry["xray_trace_id"]; ok {
		t.Fatalf("Azure preset unexpectedly emitted AWS trace field: %#v", entry)
	}
}

func TestAccessLoggerFiltersProviderReservedExtraFields(t *testing.T) {
	t.Parallel()

	const traceparent = "00-4efaaf4d1e8720b39541901950019ee5-00f067aa0ba902b7-01"
	providerKeys := []string{
		"logging.googleapis.com/trace",
		"logging.googleapis.com/trace_sampled",
		"logging.googleapis.com/spanId",
		"xray_trace_id",
		"operation_Id",
		"operation_ParentId",
	}

	for _, preset := range []Preset{PresetDefault, PresetGCP, PresetAWS, PresetAzure} {
		t.Run(string(preset), func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			ctx, _ := newHumaTestContext(http.MethodGet, "/reserved", map[string]string{
				defaultRequestIDHeader:   "req-reserved",
				defaultTraceparentHeader: traceparent,
			})

			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger,
					Preset: preset,
					ExtraFields: func(huma.Context) []zap.Field {
						return []zap.Field{
							zap.String("logging.googleapis.com/trace", "bad-gcp-trace"),
							zap.Bool("logging.googleapis.com/trace_sampled", false),
							zap.String("logging.googleapis.com/spanId", "bad-gcp-span"),
							zap.String("xray_trace_id", "bad-xray-trace"),
							zap.String("operation_Id", "bad-azure-operation"),
							zap.String("operation_ParentId", "bad-azure-parent"),
							zap.String("tenant_id", "tenant-1"),
						}
					},
				})(ctx, func(huma.Context) {})
			})

			line := strings.TrimSpace(buffer.String())
			if strings.Contains(line, "bad-") {
				t.Fatalf("reserved provider field override leaked into access log: %s", line)
			}
			for _, key := range providerKeys {
				if got := strings.Count(line, `"`+key+`"`); got > 1 {
					t.Fatalf("%s key count = %d, want at most 1; line=%s", key, got, line)
				}
			}

			entry := decodeSingleLogLine(t, line)
			assertAccessField(t, entry, "tenant_id", "tenant-1")
			switch preset {
			case PresetGCP:
				assertAccessField(t, entry, "logging.googleapis.com/trace", "4efaaf4d1e8720b39541901950019ee5")
				assertAccessField(t, entry, "logging.googleapis.com/trace_sampled", true)
				assertNoAccessField(t, entry, "logging.googleapis.com/spanId")
			case PresetAWS:
				assertAccessField(t, entry, "xray_trace_id", "1-4efaaf4d-1e8720b39541901950019ee5")
			case PresetAzure:
				assertAccessField(t, entry, "operation_Id", "4efaaf4d1e8720b39541901950019ee5")
				assertAccessField(t, entry, "operation_ParentId", "00f067aa0ba902b7")
			default:
				for _, key := range providerKeys {
					assertNoAccessField(t, entry, key)
				}
			}
		})
	}
}

func TestXRayTraceIDFromW3CBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "valid w3c trace id",
			in:   "4efaaf4d1e8720b39541901950019ee5",
			want: "1-4efaaf4d-1e8720b39541901950019ee5",
		},
		{name: "empty", in: "", want: ""},
		{name: "short", in: "4efaaf4d1e8720b39541901950019ee", want: ""},
		{name: "long", in: "4efaaf4d1e8720b39541901950019ee500", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := xrayTraceIDFromW3C(tt.in); got != tt.want {
				t.Fatalf("xrayTraceIDFromW3C(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func fixedClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Time{}
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func decodeLogLines(t *testing.T, logs string) []map[string]any {
	t.Helper()

	logs = strings.TrimSpace(logs)
	if logs == "" {
		return nil
	}
	lines := strings.Split(logs, "\n")
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

func assertAccessField(t *testing.T, entry map[string]any, key string, want any) {
	t.Helper()
	if got := entry[key]; got != want {
		t.Fatalf("%s = %#v, want %#v; entry=%#v", key, got, want, entry)
	}
}

func assertNoAccessField(t *testing.T, entry map[string]any, key string) {
	t.Helper()
	if _, ok := entry[key]; ok {
		t.Fatalf("%s unexpectedly present; entry=%#v", key, entry)
	}
}

func TestRemoteIPStripsPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "127.0.0.1:1234", want: "127.0.0.1"},
		{in: "[2001:db8::1]:443", want: "2001:db8::1"},
		{in: "2001:db8::1", want: "2001:db8::1"},
		{in: "203.0.113.9", want: "203.0.113.9"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := remoteIP(tt.in); got != tt.want {
				t.Fatalf("remoteIP(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatProtoDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: -time.Millisecond, want: "0s"},
		{in: 0, want: "0s"},
		{in: 3 * time.Second, want: "3s"},
		{in: 1500 * time.Millisecond, want: "1.5s"},
		{in: time.Second + time.Nanosecond, want: "1.000000001s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := formatProtoDuration(tt.in); got != tt.want {
				t.Fatalf("formatProtoDuration(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAccessLoggerUsesDefaultMetadataWhenRequestContextIsMissing(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)

	AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(ctx huma.Context) {
		if RequestID(ctx.Context()) == "" {
			t.Fatal("AccessLogger did not install fallback request metadata")
		}
	})

	entry := decodeSingleLogLine(t, buffer.String())
	if entry["request_id"] == "" {
		t.Fatalf("access log missing generated request_id: %#v", entry)
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got == "" {
		t.Fatal("AccessLogger fallback metadata did not propagate response header")
	}
}

func TestAccessLoggerFallbackMetadataUsesConfiguredProvider(t *testing.T) {
	t.Parallel()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	req := httptest.NewRequestWithContext(
		contextWithRequestLogger(context.Background(), nil, logger),
		http.MethodGet,
		"/test",
		nil,
	)
	req.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		httptest.NewRecorder(),
	)

	AccessLogger(AccessLoggerConfig{Preset: PresetGCP})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("handler")
	})
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if got := entry["logging.googleapis.com/trace"]; got != traceID {
			t.Fatalf("GCP trace = %v, want %q; entry=%#v", got, traceID, entry)
		}
	}
}

func TestRequestURLUsesHTTPSWhenTLSIsPresent(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/secure?x=1", nil)
	recorder := httptest.NewRecorder()
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/secure", DefaultStatus: http.StatusOK},
		req,
		recorder,
	)
	if got := requestURL(ctx); got != "https://example.com/secure?x=1" {
		t.Fatalf("requestURL = %q", got)
	}
}
