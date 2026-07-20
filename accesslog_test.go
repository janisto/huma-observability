package obs

import (
	"bytes"
	"context"
	"crypto/tls"
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

type panickingAccessWriter struct{}

type reservedInlineFields struct{}

func (reservedInlineFields) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("status", "bad-override")
	encoder.AddString("request_id", "bad-request")
	return nil
}

func (panickingAccessWriter) Write([]byte) (int, error) {
	panic("writer failed")
}

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
		Logger:           logger,
		CapturePath:      true,
		CapturePeerIP:    true,
		CaptureUserAgent: true,
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
	assertAccessField(t, access, "peer_ip", "127.0.0.1")
	assertAccessField(t, access, "user_agent", "observability-test")
	assertAccessField(t, access, "tenant_id", "tenant-1")
}

func TestRequestIDScenariosReplaceAmbiguousValuesBeforeTerminalOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawValues []string
		generated string
		duration  time.Duration
		forbidden []string
	}{
		{
			name:      "duplicate",
			rawValues: []string{"caller-one", "caller-two"},
			generated: "duplicate-replaced",
			duration:  2 * time.Millisecond,
			forbidden: []string{"caller-one", "caller-two"},
		},
		{
			name:      "invalid",
			rawValues: []string{"bad value"},
			generated: "generated-safe",
			duration:  time.Millisecond,
			forbidden: []string{"bad value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Writer: &output})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/request-id", nil)
			for _, value := range tt.rawValues {
				request.Header.Add(defaultRequestIDHeader, value)
			}
			response := httptest.NewRecorder()
			ctx := humatest.NewContext(&huma.Operation{
				Method:        http.MethodGet,
				Path:          "/request-id",
				DefaultStatus: http.StatusNoContent,
			}, request, response)
			clockValues := []time.Time{time.Unix(1, 0), time.Unix(1, int64(tt.duration))}
			clockIndex := 0
			now := func() time.Time {
				value := clockValues[clockIndex]
				clockIndex++
				return value
			}

			RequestContext(RequestContextConfig{
				Logger:       logger,
				NewRequestID: func() string { return tt.generated },
			})(ctx, func(next huma.Context) {
				AccessLogger(AccessLoggerConfig{Logger: logger, Now: now})(next, func(inner huma.Context) {
					inner.SetStatus(http.StatusNoContent)
				})
			})

			if got := response.Header().Get(defaultRequestIDHeader); got != tt.generated {
				t.Fatalf("response request ID = %q, want %q", got, tt.generated)
			}
			record := decodeSingleLogLine(t, output.String())
			assertAccessField(t, record, "message", "request completed")
			assertAccessField(t, record, "request_id", tt.generated)
			assertAccessField(t, record, "correlation_id", tt.generated)
			assertAccessField(t, record, "method", http.MethodGet)
			assertAccessField(t, record, "duration_ms", float64(tt.duration.Microseconds())/1000)
			assertAccessField(t, record, "status", float64(http.StatusNoContent))
			assertAccessField(t, record, "path_template", "/request-id")
			for _, forbidden := range tt.forbidden {
				if strings.Contains(output.String(), forbidden) {
					t.Fatalf("terminal output contains rejected request ID %q: %s", forbidden, output.String())
				}
			}
		})
	}
}

func TestCanonicalRouteTemplateCurrentHumaForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		native string
		ok     bool
	}{
		{native: "/health", ok: true},
		{native: "/items/{item_id}", ok: true},
		{native: "/items/{item_id?}"},
		{native: "/files/*"},
		{native: "/items/{item_id}/suffix", ok: true},
	} {
		t.Run(test.native, func(t *testing.T) {
			t.Parallel()
			got, ok := canonicalRouteTemplate(test.native)
			if ok != test.ok || ok && got != test.native {
				t.Fatalf("canonicalRouteTemplate(%q) = (%q, %v), want unchanged=%v", test.native, got, ok, test.ok)
			}
		})
	}
	for _, name := range []string{"A", "Z", "a", "z", "_", "a0", "a9", strings.Repeat("a", 64)} {
		t.Run("valid-name-"+name, func(t *testing.T) {
			t.Parallel()
			native := "/items/{" + name + "}"
			if got, ok := canonicalRouteTemplate(native); !ok || got != native {
				t.Fatalf("canonicalRouteTemplate(%q) = (%q, %v), want unchanged", native, got, ok)
			}
		})
	}
	for _, name := range []string{"", "0a", "9a", "@a", "[a", "`a", "{a", "a.", "a:", strings.Repeat("a", 65)} {
		t.Run("invalid-name-"+name, func(t *testing.T) {
			t.Parallel()
			if got, ok := canonicalRouteTemplate("/items/{" + name + "}"); ok || got != "" {
				t.Fatalf("invalid parameter name %q produced (%q, %v)", name, got, ok)
			}
		})
	}
}

func TestAccessLoggerParameterIdentityHasStableCardinality(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	_, api := humatest.New(t)
	api.UseMiddleware(AccessLogger(AccessLoggerConfig{Logger: logger}))
	huma.Register(api, huma.Operation{
		OperationID: "get_item",
		Method:      http.MethodGet,
		Path:        "/items/{item_id}",
	}, func(context.Context, *struct {
		ItemID string `path:"item_id"`
	},
	) (*testOutput, error) {
		return &testOutput{Body: testBody{OK: true}}, nil
	})

	for _, target := range []string{"/items/tenant-a", "/items/tenant-b"} {
		if response := api.Get(target); response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", target, response.Code)
		}
	}
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 2 {
		t.Fatalf("log line count = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		assertAccessField(t, entry, "path_template", "/items/{item_id}")
		assertAccessField(t, entry, "operation_id", "get_item")
	}
}

func TestAccessMetadataStringsRejectEmptyDuplicateAndControlValues(t *testing.T) {
	t.Parallel()

	if _, ok := singleValidHeaderValue([]string{"agent/1"}); !ok {
		t.Fatal("one valid User-Agent was rejected")
	}
	for _, values := range [][]string{nil, {""}, {"agent/1", "agent/1"}, {"agent/1\nforged"}} {
		if value, ok := singleValidHeaderValue(values); ok || value != "" {
			t.Fatalf("singleValidHeaderValue(%q) = %q, %v; want empty, false", values, value, ok)
		}
	}
	if validMetadataString("get_item\nforged") {
		t.Fatal("operation ID containing a control character was accepted")
	}
	for _, value := range []string{"\x1f", "\x7f"} {
		if validMetadataString(value) {
			t.Fatalf("metadata control boundary %q was accepted", value)
		}
	}
	for _, value := range []string{" ", "~"} {
		if !validMetadataString(value) {
			t.Fatalf("printable metadata boundary %q was rejected", value)
		}
	}
}

func TestAccessLoggerIntegrationLogsHumaHandlerErrors(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	_, api := humatest.New(t)
	api.UseMiddleware(RequestContext(RequestContextConfig{}))
	api.UseMiddleware(AccessLogger(AccessLoggerConfig{
		Logger:      logger,
		CapturePath: true,
		Now: fixedClock(
			time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 15, 12, 0, 0, int(2*time.Millisecond), time.UTC),
		),
	}))
	huma.Register(api, huma.Operation{
		OperationID: "get-missing-widget",
		Method:      http.MethodGet,
		Path:        "/missing-widgets/{id}",
	}, func(context.Context, *struct {
		ID string `path:"id"`
	},
	) (*testOutput, error) {
		return nil, huma.Error404NotFound("widget not found")
	})

	resp := api.Get("/missing-widgets/123", "X-Request-Id: req-not-found")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("response status = %d, want 404", resp.Code)
	}
	lines := decodeLogLines(t, buffer.String())
	if len(lines) != 1 {
		t.Fatalf("log line count = %d, want exactly 1; lines=%#v", len(lines), lines)
	}
	entry := lines[0]
	assertAccessField(t, entry, "level", "WARN")
	assertAccessField(t, entry, "status", float64(http.StatusNotFound))
	assertAccessField(t, entry, "request_id", "req-not-found")
	assertAccessField(t, entry, "path", "/missing-widgets/123")
	assertAccessField(t, entry, "path_template", "/missing-widgets/{id}")
	assertAccessField(t, entry, "operation_id", "get-missing-widget")
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
		{name: "immediately below client error boundary", status: 399, wantLevel: "INFO"},
		{name: "client error boundary", status: 400, wantLevel: "WARN"},
		{name: "immediately below server error boundary", status: 499, wantLevel: "WARN"},
		{name: "server error boundary", status: 500, wantLevel: "ERROR"},
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
			statusLevel := tt.statusLevel
			if statusLevel != nil {
				configuredStatusLevel := statusLevel
				statusLevel = func(status int) zapcore.Level {
					if status != tt.status {
						t.Fatalf("StatusLevel status = %d, want %d", status, tt.status)
					}
					return configuredStatusLevel(status)
				}
			}
			RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger:      logger,
					Now:         fixedClock(time.Unix(0, 0), time.Unix(0, int64(time.Millisecond))),
					StatusLevel: statusLevel,
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

func TestAccessLoggerContainsTelemetryCallbackPanics(t *testing.T) {
	t.Run("status mapper uses default", func(t *testing.T) {
		var buffer bytes.Buffer
		logger, err := NewLogger(LoggerConfig{Writer: &buffer})
		if err != nil {
			t.Fatalf("NewLogger returned error: %v", err)
		}
		ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
			defaultRequestIDHeader: "req-status-panic",
		})

		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			StatusLevel: func(int) zapcore.Level {
				panic("status mapper failed")
			},
		})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusNotFound)
		})

		entry := decodeSingleLogLine(t, buffer.String())
		assertAccessField(t, entry, "level", "WARN")
		assertAccessField(t, entry, "status", float64(http.StatusNotFound))
	})

	t.Run("clock and enrichment use safe values", func(t *testing.T) {
		var buffer bytes.Buffer
		logger, err := NewLogger(LoggerConfig{Writer: &buffer})
		if err != nil {
			t.Fatalf("NewLogger returned error: %v", err)
		}
		ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
			defaultRequestIDHeader: "req-callback-panic",
		})
		clockCalls := 0

		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			Now: func() time.Time {
				clockCalls++
				if clockCalls == 1 {
					return time.Unix(0, 0)
				}
				panic("clock failed")
			},
			ExtraFields: func(huma.Context) []zap.Field {
				panic("enrichment failed")
			},
		})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusAccepted)
		})

		entry := decodeSingleLogLine(t, buffer.String())
		assertAccessField(t, entry, "duration_ms", float64(0))
		assertAccessField(t, entry, "status", float64(http.StatusAccepted))
		assertNoAccessField(t, entry, "tenant_id")
	})
}

func TestSafeStatusLevelRejectsTerminalAndUnknownLevels(t *testing.T) {
	t.Parallel()

	for _, level := range []zapcore.Level{
		zapcore.DPanicLevel,
		zapcore.PanicLevel,
		zapcore.FatalLevel,
		zapcore.Level(99),
	} {
		t.Run(level.String(), func(t *testing.T) {
			t.Parallel()
			got := safeStatusLevel(func(int) zapcore.Level { return level }, http.StatusNotFound)
			if got != zapcore.WarnLevel {
				t.Fatalf("safeStatusLevel returned %s, want WARN fallback", got)
			}
		})
	}
}

func TestAccessLoggerContainsWriterPanicWithoutChangingHandler(t *testing.T) {
	logger, err := NewLogger(LoggerConfig{Writer: panickingAccessWriter{}})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-writer-panic",
	})
	called := false

	AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(ctx huma.Context) {
		called = true
		ctx.SetStatus(http.StatusNoContent)
	})

	if !called {
		t.Fatal("AccessLogger did not preserve handler execution")
	}
}

func TestAccessLoggerSuppressedCustomLevelDoesNotEmit(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-suppressed",
	})
	called := false

	AccessLogger(AccessLoggerConfig{
		Logger: logger,
		StatusLevel: func(int) zapcore.Level {
			return zapcore.DebugLevel
		},
	})(ctx, func(ctx huma.Context) {
		called = true
		ctx.SetStatus(http.StatusOK)
	})

	if !called {
		t.Fatal("AccessLogger did not call downstream for a disabled log level")
	}
	if got := strings.TrimSpace(buffer.String()); got != "" {
		t.Fatalf("disabled debug access log was emitted: %s", got)
	}
}

func TestAccessLoggerOmitsUnobservedOperationDefaultAndImplicitStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation *huma.Operation
	}{
		{
			name: "operation default status",
			operation: &huma.Operation{
				Method:        http.MethodPost,
				Path:          "/widgets",
				DefaultStatus: http.StatusCreated,
			},
		},
		{
			name: "zero operation default falls back to ok",
			operation: &huma.Operation{
				Method: http.MethodGet,
				Path:   "/widgets",
			},
		},
		{
			name:      "missing operation has no observed status",
			operation: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/widgets", nil)
			req.Header.Set(defaultRequestIDHeader, "req-status-fallback")
			ctx := humatest.NewContext(tt.operation, req, httptest.NewRecorder())

			statusLevelCalls := 0
			AccessLogger(AccessLoggerConfig{
				Logger: logger,
				StatusLevel: func(int) zapcore.Level {
					statusLevelCalls++
					return zapcore.DebugLevel
				},
			})(ctx, func(huma.Context) {})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, "level", "INFO")
			assertNoAccessField(t, entry, "status")
			assertNoAccessField(t, entry, "terminal_reason")
			if statusLevelCalls != 0 {
				t.Fatalf("StatusLevel calls = %d, want zero without authoritative status", statusLevelCalls)
			}
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
	assertAccessField(t, entry, "terminal_reason", "panic")
	assertNoAccessField(t, entry, "status")
	assertAccessField(t, entry, "request_id", "req-panic")
}

func TestAccessLoggerPanicPreservesAlreadyObservedStatus(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/panic", nil)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusAccepted)
			panic("boom")
		})
	}()

	if recovered != "boom" {
		t.Fatalf("recovered = %#v, want boom", recovered)
	}
	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "level", "ERROR")
	assertAccessField(t, entry, "status", float64(http.StatusAccepted))
	assertAccessField(t, entry, "terminal_reason", "panic")
}

func TestAccessLoggerPreservesHandlerPanicWhenEnrichmentPanics(t *testing.T) {
	t.Parallel()
	type panicMarker struct{ name string }
	handlerPanic := &panicMarker{name: "handler"}
	enrichmentPanic := &panicMarker{name: "enrichment"}
	ctx, _ := newHumaTestContext(http.MethodGet, "/panic", nil)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		AccessLogger(AccessLoggerConfig{
			ExtraFields: func(huma.Context) []zap.Field {
				panic(enrichmentPanic)
			},
		})(ctx, func(huma.Context) {
			panic(handlerPanic)
		})
	}()

	if recovered != handlerPanic {
		t.Fatalf("recovered panic = %#v, want original %#v", recovered, handlerPanic)
	}
}

func TestContainAccessLogReportsCompletion(t *testing.T) {
	t.Parallel()

	if !containAccessLog(func() {}) {
		t.Fatal("containAccessLog reported failure for a completed write")
	}
	if containAccessLog(func() { panic("writer failed") }) {
		t.Fatal("containAccessLog reported completion for a panicking write")
	}
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

	reservedKeys := []string{
		"timestamp",
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
		"terminal_reason",
		"peer_ip",
		"remote_ip",
		"user_agent",
		"httpRequest",
		"logging.googleapis.com/trace",
		"logging.googleapis.com/trace_sampled",
		"logging.googleapis.com/spanId",
	}
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
				fields := make([]zap.Field, 0, len(reservedKeys)+2)
				for _, key := range reservedKeys {
					fields = append(fields, zap.String(key, "bad-override"))
				}
				return append(
					fields,
					zap.String("tenant_id", "tenant-1"),
					zap.String("tenant_id", "tenant-2"),
				)
			},
		})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusOK)
		})
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
	for _, key := range reservedKeys {
		if got := strings.Count(line, `"`+key+`"`); got > 1 {
			t.Fatalf("reserved key %q count = %d, want at most 1; line=%s", key, got, line)
		}
	}

	entry := decodeSingleLogLine(t, line)
	assertAccessField(t, entry, "request_id", "req-extra")
	assertAccessField(t, entry, "status", float64(http.StatusOK))
	assertAccessField(t, entry, "tenant_id", "tenant-1")
	if got := strings.Count(line, `"tenant_id"`); got != 1 {
		t.Fatalf("tenant_id key count = %d, want 1; line=%s", got, line)
	}
}

func TestAccessLoggerRejectsInlineExtraFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-inline",
	})

	RequestContext(RequestContextConfig{})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			ExtraFields: func(huma.Context) []zap.Field {
				return []zap.Field{zap.Inline(reservedInlineFields{}), zap.String("tenant_id", "tenant-1")}
			},
		})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusOK)
		})
	})

	line := strings.TrimSpace(buffer.String())
	if strings.Contains(line, "bad-override") || strings.Contains(line, "bad-request") {
		t.Fatalf("inline extra fields leaked into access log: %s", line)
	}
	if got := strings.Count(line, `"status"`); got != 1 {
		t.Fatalf("status key count = %d, want 1; line=%s", got, line)
	}
	entry := decodeSingleLogLine(t, line)
	assertAccessField(t, entry, "request_id", "req-inline")
	assertAccessField(t, entry, "status", float64(http.StatusOK))
	assertAccessField(t, entry, "tenant_id", "tenant-1")
}

func TestAccessLoggerOmitsRandomFlagForFutureTraceparentVersion(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader:   "req-future",
		defaultTraceparentHeader: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-03-opaque",
	})

	RequestContext(RequestContextConfig{TraceContextLevel: TraceContextLevel2})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{Logger: logger, TraceContextLevel: TraceContextLevel2})(
			ctx,
			func(huma.Context) {},
		)
	})

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "trace_flags", "03")
	assertAccessField(t, entry, "trace_sampled", true)
	assertNoAccessField(t, entry, "trace_id_random")
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
			Logger(next.Context()).Info("handler used cloned metadata logger")
		})

		if original.Logger != nil {
			t.Fatal("AccessLogger mutated metadata already stored on the incoming context")
		}
	})

	lines := decodeLogLines(t, buffer.String())
	if len(lines) != 2 {
		t.Fatalf("log line count = %d, want handler and access lines; lines=%#v", len(lines), lines)
	}
	assertAccessField(t, lines[0], "message", "handler used cloned metadata logger")
	for _, entry := range lines {
		assertAccessField(t, entry, "request_id", "req-clone")
		assertAccessField(t, entry, "correlation_id", "req-clone")
	}
}

func TestAccessLoggerNilBaseLoggerStillInstallsMetadataAndCallsDownstream(t *testing.T) {
	t.Parallel()

	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
	called := false
	var requestID string
	AccessLogger(AccessLoggerConfig{})(ctx, func(next huma.Context) {
		called = true
		requestID = RequestID(next.Context())
	})
	if !called {
		t.Fatal("AccessLogger with a nil base logger did not call downstream")
	}
	if requestID == "" {
		t.Fatal("AccessLogger with a nil base logger did not install request metadata")
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != requestID {
		t.Fatalf("response request ID header = %q, want %q", got, requestID)
	}
}

func TestAccessLoggerPrivacyFieldsAreDisabledByDefault(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/minimal?secret=yes", nil)
	req.Header.Set(defaultRequestIDHeader, "req-minimal")
	req.Header.Set("User-Agent", "privacy-canary")
	req.RemoteAddr = "203.0.113.17:4321"
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, DefaultStatus: http.StatusOK},
		req,
		httptest.NewRecorder(),
	)

	AccessLogger(AccessLoggerConfig{
		Logger: logger,
		Preset: PresetGCP,
		Now: fixedClock(
			time.Unix(0, 0),
			time.Unix(0, int64(time.Millisecond)),
		),
	})(ctx, func(huma.Context) {})

	entry := decodeSingleLogLine(t, buffer.String())
	assertAccessField(t, entry, "message", "request completed")
	assertAccessField(t, entry, "method", http.MethodGet)
	assertNoAccessField(t, entry, "status")
	assertAccessField(t, entry, "duration_ms", float64(1))
	for _, key := range []string{"path", "path_template", "operation_id", "peer_ip", "remote_ip", "user_agent"} {
		assertNoAccessField(t, entry, key)
	}

	httpRequest, ok := entry["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
	}
	assertAccessField(t, httpRequest, "requestMethod", http.MethodGet)
	assertNoAccessField(t, httpRequest, "status")
	assertAccessField(t, httpRequest, "latency", "0.001s")
	assertNoAccessField(t, httpRequest, "requestUrl")
	assertNoAccessField(t, httpRequest, "userAgent")
	assertNoAccessField(t, httpRequest, "remoteIp")
	if len(httpRequest) != 2 {
		t.Fatalf("httpRequest fields = %#v, want only requestMethod and latency", httpRequest)
	}
}

func TestAccessLoggerPrivacyCaptureOptionsAreIndependent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    AccessLoggerConfig
		portable  string
		provider  string
		wantValue string
	}{
		{
			name:      "path",
			config:    AccessLoggerConfig{CapturePath: true},
			portable:  "path",
			provider:  "requestUrl",
			wantValue: "/private",
		},
		{
			name:      "peer IP",
			config:    AccessLoggerConfig{CapturePeerIP: true},
			portable:  "peer_ip",
			provider:  "remoteIp",
			wantValue: "203.0.113.17",
		},
		{
			name:      "user agent",
			config:    AccessLoggerConfig{CaptureUserAgent: true},
			portable:  "user_agent",
			provider:  "userAgent",
			wantValue: "privacy-canary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/private?secret=yes", nil)
			req.Header.Set(defaultRequestIDHeader, "req-privacy")
			req.Header.Set("User-Agent", "privacy-canary")
			req.RemoteAddr = "203.0.113.17:4321"
			ctx := humatest.NewContext(nil, req, httptest.NewRecorder())
			tt.config.Logger = logger
			tt.config.Preset = PresetGCP

			AccessLogger(tt.config)(ctx, func(huma.Context) {})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, tt.portable, tt.wantValue)
			for _, key := range []string{"path", "peer_ip", "user_agent"} {
				if key != tt.portable {
					assertNoAccessField(t, entry, key)
				}
			}
			httpRequest, ok := entry["httpRequest"].(map[string]any)
			if !ok {
				t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
			}
			assertAccessField(t, httpRequest, tt.provider, tt.wantValue)
			for _, key := range []string{"requestUrl", "remoteIp", "userAgent"} {
				if key != tt.provider {
					assertNoAccessField(t, httpRequest, key)
				}
			}
		})
	}
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
			Logger:           logger,
			Preset:           PresetGCP,
			CapturePath:      true,
			CapturePeerIP:    true,
			CaptureUserAgent: true,
			Now:              fixedClock(time.Unix(0, 0), time.Unix(0, int64(1500*time.Millisecond))),
		})(ctx, func(ctx huma.Context) {
			ctx.SetStatus(http.StatusOK)
		})
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
	assertAccessField(t, entry, "path", "/gcp")
	assertAccessField(t, entry, "peer_ip", "203.0.113.9")
	assertAccessField(t, entry, "user_agent", "gcp-test")
	if _, ok := entry["logging.googleapis.com/spanId"]; ok {
		t.Fatalf("GCP entry must not emit spanId from parent ID: %#v", entry)
	}

	httpRequest, ok := entry["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
	}
	assertAccessField(t, httpRequest, "requestMethod", http.MethodGet)
	assertAccessField(t, httpRequest, "requestUrl", "/gcp")
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
			assertAccessField(t, entry, "request_id", "req-provider-header")
			assertAccessField(t, entry, "correlation_id", "req-provider-header")
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
			assertAccessField(t, entry, "request_id", "req-invalid-trace")
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

func TestDirectPeerIPCanonicalizesValidatedAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "127.0.0.1:1234", want: "127.0.0.1"},
		{in: "[2001:db8::1]:443", want: "2001:db8::1"},
		{in: "[2001:db8::1]", want: "2001:db8::1"},
		{in: "2001:db8::1", want: "2001:db8::1"},
		{in: "2001:0db8:0:0:0:0:0:1", want: "2001:db8::1"},
		{in: "203.0.113.9", want: "203.0.113.9"},
		{in: "example.com:443", want: ""},
		{in: "peer.internal", want: ""},
		{in: "[fe80::1%eth0]:443", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := directPeerIP(tt.in); got != tt.want {
				t.Fatalf("directPeerIP(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRequestPathRejectsUnavailableAndNonOriginForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		url  string
		want string
	}{
		{name: "origin form", url: "/widgets/a%2Fb?secret=true", want: "/widgets/a%2Fb"},
		{name: "empty path", url: "https://example.test", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.url, nil)
			if test.name == "empty path" {
				request.URL.Path = ""
			}
			response := httptest.NewRecorder()
			ctx := humatest.NewContext(
				&huma.Operation{Method: http.MethodGet, Path: "/widgets/{id}"},
				request,
				response,
			)
			if got := requestPath(ctx); got != test.want {
				t.Fatalf("requestPath() = %q, want %q", got, test.want)
			}
		})
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.URL.Path = ""
	request.URL.Opaque = "//authority.example/path"
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/"},
		request,
		httptest.NewRecorder(),
	)
	if got := requestPath(ctx); got != "" {
		t.Fatalf("requestPath() repaired opaque target as %q", got)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/widgets/a", nil)
	request.URL.Path = "/widgets/a/b"
	request.URL.RawPath = "/widgets/a%2G"
	ctx = humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/widgets/{id}"},
		request,
		httptest.NewRecorder(),
	)
	if got := requestPath(ctx); got != "" {
		t.Fatalf("requestPath() repaired malformed raw path as %q", got)
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
	var handlerRequestID string

	AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(ctx huma.Context) {
		handlerRequestID = RequestID(ctx.Context())
		if handlerRequestID == "" {
			t.Fatal("AccessLogger did not install fallback request metadata")
		}
	})

	entry := decodeSingleLogLine(t, buffer.String())
	loggedRequestID, ok := entry["request_id"].(string)
	if !ok || loggedRequestID == "" {
		t.Fatalf("access log request_id missing or not a non-empty string: %#v", entry)
	}
	if loggedRequestID != handlerRequestID {
		t.Fatalf("logged request_id = %q, want handler request ID %q", loggedRequestID, handlerRequestID)
	}
	if got := entry["correlation_id"]; got != handlerRequestID {
		t.Fatalf("correlation_id = %v, want generated request ID %q", got, handlerRequestID)
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != handlerRequestID {
		t.Fatalf("response request ID header = %q, want %q", got, handlerRequestID)
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

func TestAccessLoggerGCPRequestURLUsesCapturedPathOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   string
		mutate   func(*http.Request)
		wantPath string
	}{
		{
			name:   "TLS and query are omitted",
			target: "/secure?x=1",
			mutate: func(req *http.Request) {
				req.TLS = &tls.ConnectionState{}
			},
			wantPath: "/secure",
		},
		{
			name:     "escaped path remains encoded",
			target:   "/segments/a%2Fb?x=1",
			wantPath: "/segments/a%2Fb",
		},
		{
			name:     "absolute scheme and authority are omitted",
			target:   "https://upstream.example/absolute?x=1",
			wantPath: "/absolute",
		},
		{
			name:   "opaque request target is omitted",
			target: "/",
			mutate: func(req *http.Request) {
				req.URL.Path = ""
				req.URL.Opaque = "/opaque"
				req.Host = ""
			},
			wantPath: "",
		},
		{
			name:   "empty request target is omitted",
			target: "/",
			mutate: func(req *http.Request) {
				req.URL.Path = ""
				req.URL.Opaque = ""
				req.Host = ""
			},
			wantPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tt.target, nil)
			req.Header.Set(defaultRequestIDHeader, "req-target")
			if tt.mutate != nil {
				tt.mutate(req)
			}
			ctx := humatest.NewContext(nil, req, httptest.NewRecorder())

			AccessLogger(AccessLoggerConfig{
				Logger:      logger,
				Preset:      PresetGCP,
				CapturePath: true,
			})(ctx, func(huma.Context) {})

			entry := decodeSingleLogLine(t, buffer.String())
			httpRequest, ok := entry["httpRequest"].(map[string]any)
			if !ok {
				t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
			}
			if tt.wantPath == "" {
				assertNoAccessField(t, entry, "path")
				assertNoAccessField(t, httpRequest, "requestUrl")
			} else {
				assertAccessField(t, entry, "path", tt.wantPath)
				assertAccessField(t, httpRequest, "requestUrl", tt.wantPath)
			}
		})
	}
}

func TestAccessLoggerResolvesAndValidatesGCPProfileVersionAtConstruction(t *testing.T) {
	t.Parallel()

	latest := normalizeAccessLoggerConfig(AccessLoggerConfig{Preset: PresetGCP})
	pinned := normalizeAccessLoggerConfig(AccessLoggerConfig{
		Preset:            PresetGCP,
		GCPProfileVersion: GCPProfileVersionV0_1_0,
	})
	if latest.GCPProfileVersion != GCPProfileVersionV0_1_0 {
		t.Fatalf("latest profile = %q, want %q", latest.GCPProfileVersion, GCPProfileVersionV0_1_0)
	}
	if pinned.GCPProfileVersion != GCPProfileVersionV0_1_0 {
		t.Fatalf("pinned profile = %q, want %q", pinned.GCPProfileVersion, GCPProfileVersionV0_1_0)
	}

	tests := []struct {
		name   string
		config AccessLoggerConfig
		want   string
	}{
		{
			name:   "unsupported version",
			config: AccessLoggerConfig{Preset: PresetGCP, GCPProfileVersion: "0.2.0"},
			want:   `observability: unsupported GCP profile version "0.2.0"`,
		},
		{
			name:   "cross-preset version",
			config: AccessLoggerConfig{Preset: PresetAWS, GCPProfileVersion: GCPProfileVersionV0_1_0},
			want:   "observability: GCP profile version requires GCP preset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				recovered := recover()
				err, ok := recovered.(error)
				if !ok {
					t.Fatalf("AccessLogger panic = %#v, want error", recovered)
				}
				if err.Error() != tt.want {
					t.Fatalf("AccessLogger panic = %q, want %q", err, tt.want)
				}
			}()
			AccessLogger(tt.config)
		})
	}
}
