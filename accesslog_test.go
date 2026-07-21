package obs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
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

type countingFailingAccessWriter struct {
	calls *int
	err   error
}

type reservedInlineFields struct{}

func (reservedInlineFields) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("status", "bad-override")
	encoder.AddString("request_id", "bad-request")
	return nil
}

func (panickingAccessWriter) Write([]byte) (int, error) {
	panic("writer failed")
}

func (writer countingFailingAccessWriter) Write([]byte) (int, error) {
	(*writer.calls)++
	return 0, writer.err
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

func TestApplicationLoggerDropsReservedFieldsWithoutChangingAccessRecord(t *testing.T) {
	t.Parallel()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const traceparent = "00-" + traceID + "-00f067aa0ba902b7-01"
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetAzure, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/guarded", map[string]string{
		defaultRequestIDHeader:   "canonical-request",
		defaultTraceparentHeader: traceparent,
	})

	AccessLogger(AccessLoggerConfig{
		Logger: logger,
		Preset: PresetAzure,
		Now: fixedClock(
			time.Unix(1, 0),
			time.Unix(1, int64(5*time.Millisecond)),
		),
	})(ctx, func(inner huma.Context) {
		Logger(inner.Context()).Info(
			"guarded application event",
			zap.String("request_id", "spoofed-request"),
			zap.String("trace_id", "00000000000000000000000000000001"),
			zap.String("operation_Id", "spoofed-operation"),
			zap.String("xray_trace_id", "application-xray"),
			zap.String("logging.googleapis.com/trace", "application-gcp-trace"),
			zap.Bool("logging.googleapis.com/trace_sampled", false),
			zap.String("method", "DELETE"),
			zap.Int("status", 599),
			zap.String("message", "spoofed-message"),
			zap.String("logging.googleapis.com/future", "spoofed-provider"),
			zap.Any("logging.googleapis.com/labels", map[string]string{"component": "worker"}),
			zap.Bool("obs.internal", true),
			zap.Bool("_obs_debug", true),
			zap.String("remote_ip", "application-peer"),
			zap.String("logging.googleapis.com/spanId", "application-span"),
			zap.String("error", "controlled application error"),
			zap.String("tenant_id", "tenant-1"),
		)
		inner.SetStatus(http.StatusNoContent)
	})

	rawLines := strings.Split(strings.TrimSuffix(buffer.String(), "\n"), "\n")
	if len(rawLines) != 2 {
		t.Fatalf("log line count = %d, want 2; output=%s", len(rawLines), buffer.String())
	}
	applicationLine := rawLines[0]
	for _, key := range []string{
		"request_id", "trace_id", "operation_Id", "xray_trace_id",
		"logging.googleapis.com/trace", "logging.googleapis.com/trace_sampled", "message", "tenant_id",
	} {
		if count := strings.Count(applicationLine, `"`+key+`"`); count != 1 {
			t.Fatalf("%s key count = %d, want 1; line=%s", key, count, applicationLine)
		}
	}
	for _, key := range []string{
		"method", "status", "logging.googleapis.com/future", "logging.googleapis.com/labels",
		"obs.internal", "_obs_debug",
		"remote_ip", "logging.googleapis.com/spanId",
	} {
		if count := strings.Count(applicationLine, `"`+key+`"`); count != 1 {
			t.Fatalf("%s key count = %d, want 1; line=%s", key, count, applicationLine)
		}
	}
	application := decodeSingleLogLine(t, applicationLine)
	assertAccessField(t, application, "message", "guarded application event")
	assertAccessField(t, application, "request_id", "canonical-request")
	assertAccessField(t, application, "trace_id", traceID)
	assertAccessField(t, application, "operation_Id", traceID)
	assertAccessField(t, application, "xray_trace_id", "application-xray")
	assertAccessField(t, application, "logging.googleapis.com/trace", "application-gcp-trace")
	assertAccessField(t, application, "logging.googleapis.com/trace_sampled", false)
	assertAccessField(t, application, "method", "DELETE")
	assertAccessField(t, application, "status", float64(599))
	assertAccessField(t, application, "logging.googleapis.com/future", "spoofed-provider")
	applicationLabels, ok := application["logging.googleapis.com/labels"].(map[string]any)
	if !ok {
		t.Fatalf("application labels object missing: %#v", application)
	}
	assertAccessField(t, applicationLabels, "component", "worker")
	assertAccessField(t, application, "obs.internal", true)
	assertAccessField(t, application, "_obs_debug", true)
	assertAccessField(t, application, "remote_ip", "application-peer")
	assertAccessField(t, application, "logging.googleapis.com/spanId", "application-span")
	assertAccessField(t, application, "error", "controlled application error")
	assertAccessField(t, application, "tenant_id", "tenant-1")

	access := decodeSingleLogLine(t, rawLines[1])
	assertAccessField(t, access, "message", "request completed")
	assertAccessField(t, access, "method", http.MethodGet)
	assertAccessField(t, access, "status", float64(http.StatusNoContent))
	assertAccessField(t, access, "request_id", "canonical-request")
}

func TestApplicationLoggerPreservesNestedReservedLookingFieldsAcrossWith(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/nested", map[string]string{
		defaultRequestIDHeader: "canonical-request",
	})

	RequestContext(RequestContextConfig{Logger: logger})(ctx, func(next huma.Context) {
		Logger(next.Context()).With(zap.Namespace("job")).Info(
			"nested application event",
			zap.String("status", "running"),
			zap.String("method", "worker"),
			zap.String("request_id", "nested-request"),
		)
		Logger(next.Context()).Info(
			"protected namespace",
			zap.Namespace("request_id"),
			zap.String("forged", "must-not-be-hoisted"),
		)
	})

	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 2 {
		t.Fatalf("log line count = %d, want 2; entries=%#v", len(entries), entries)
	}
	job, ok := entries[0]["job"].(map[string]any)
	if !ok {
		t.Fatalf("nested job object missing: %#v", entries[0])
	}
	assertAccessField(t, job, "status", "running")
	assertAccessField(t, job, "method", "worker")
	assertAccessField(t, job, "request_id", "nested-request")
	assertAccessField(t, entries[0], "request_id", "canonical-request")
	assertAccessField(t, entries[1], "request_id", "canonical-request")
	assertNoAccessField(t, entries[1], "forged")
}

func TestHumaRejectsExtensionMethodRegistrationBeforeAccessMiddleware(t *testing.T) {
	t.Parallel()

	_, api := humatest.New(t)
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Huma accepted an extension method outside its OpenAPI method set")
		} else if !strings.Contains(fmt.Sprint(recovered), "unknown method m-SEARCH") {
			t.Fatalf("unexpected registration panic: %v", recovered)
		}
	}()

	huma.Register(api, huma.Operation{
		OperationID: "extension-search",
		Method:      "m-SEARCH",
		Path:        "/search",
	}, func(context.Context, *struct{}) (*testOutput, error) {
		return &testOutput{Body: testBody{OK: true}}, nil
	})
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
		{native: "/files/*", ok: true},
		{native: "/items/{item_id}/suffix", ok: true},
		{native: "/items/{item#fragment}", ok: true},
	} {
		t.Run(test.native, func(t *testing.T) {
			t.Parallel()
			got, ok := canonicalRouteTemplate(test.native)
			if ok != test.ok || ok && got != test.native {
				t.Fatalf("canonicalRouteTemplate(%q) = (%q, %v), want unchanged=%v", test.native, got, ok, test.ok)
			}
		})
	}
	if got, ok := canonicalRouteTemplate(""); ok || got != "" {
		t.Fatalf("empty route template produced (%q, %v)", got, ok)
	}
}

func TestComposedMiddlewareRejectsTraceContextLevelMismatchInEitherOrder(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		requestLevel TraceContextLevel
		accessLevel  TraceContextLevel
		requestOuter bool
	}{
		{name: "request outer", requestLevel: TraceContextLevel1, accessLevel: TraceContextLevel2, requestOuter: true},
		{name: "access outer", requestLevel: TraceContextLevel1, accessLevel: TraceContextLevel2},
		{name: "reverse levels request outer", requestLevel: TraceContextLevel2, accessLevel: TraceContextLevel1, requestOuter: true},
		{name: "reverse levels access outer", requestLevel: TraceContextLevel2, accessLevel: TraceContextLevel1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", nil)
			request := RequestContext(RequestContextConfig{TraceContextLevel: tt.requestLevel})
			access := AccessLogger(AccessLoggerConfig{TraceContextLevel: tt.accessLevel})
			defer func() {
				if got := recover(); got != "trace context level mismatch between RequestContext and AccessLogger" {
					t.Fatalf("mismatch panic = %v", got)
				}
			}()
			if tt.requestOuter {
				request(ctx, func(next huma.Context) { access(next, func(huma.Context) {}) })
			} else {
				access(ctx, func(next huma.Context) { request(next, func(huma.Context) {}) })
			}
		})
	}
}

func TestComposedMiddlewareRejectsProviderPresetMismatchInEitherOrder(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name          string
		requestPreset Preset
		accessPreset  Preset
		requestOuter  bool
	}{
		{name: "request outer", requestPreset: PresetGCP, accessPreset: PresetAzure, requestOuter: true},
		{name: "access outer", requestPreset: PresetGCP, accessPreset: PresetAzure},
		{name: "reverse providers request outer", requestPreset: PresetAWS, accessPreset: PresetGCP, requestOuter: true},
		{name: "reverse providers access outer", requestPreset: PresetAWS, accessPreset: PresetGCP},
		{name: "core to GCP request outer", requestPreset: PresetDefault, accessPreset: PresetGCP, requestOuter: true},
		{name: "core to GCP access outer", requestPreset: PresetDefault, accessPreset: PresetGCP},
		{name: "GCP to core request outer", requestPreset: PresetGCP, accessPreset: PresetDefault, requestOuter: true},
		{name: "GCP to core access outer", requestPreset: PresetGCP, accessPreset: PresetDefault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", nil)
			request := RequestContext(RequestContextConfig{Preset: tt.requestPreset})
			access := AccessLogger(AccessLoggerConfig{Preset: tt.accessPreset})
			defer func() {
				if got := recover(); got != "provider preset mismatch between RequestContext and AccessLogger" {
					t.Fatalf("mismatch panic = %v", got)
				}
			}()
			if tt.requestOuter {
				request(ctx, func(next huma.Context) { access(next, func(huma.Context) {}) })
			} else {
				access(ctx, func(next huma.Context) { request(next, func(huma.Context) {}) })
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
	huma.Register(api, huma.Operation{
		OperationID: "get_extended",
		Method:      http.MethodGet,
		Path:        "/extended/{item-id}",
	}, func(context.Context, *struct {
		ItemID string `path:"item-id"`
	},
	) (*testOutput, error) {
		return &testOutput{Body: testBody{OK: true}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get_long",
		Method:      http.MethodGet,
		Path:        "/long/{aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}",
	}, func(context.Context, *struct {
		Value string `path:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`
	},
	) (*testOutput, error) {
		return &testOutput{Body: testBody{OK: true}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get\ncontrol",
		Method:      http.MethodGet,
		Path:        "/control/{item}",
	}, func(context.Context, *struct {
		Item string `path:"item"`
	},
	) (*testOutput, error) {
		return &testOutput{Body: testBody{OK: true}}, nil
	})

	for _, target := range []string{
		"/items/tenant-a", "/items/tenant-b", "/extended/value", "/long/value", "/control/value",
	} {
		if response := api.Get(target); response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", target, response.Code)
		}
	}
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 5 {
		t.Fatalf("log line count = %d, want 5", len(entries))
	}
	for _, entry := range entries[:2] {
		assertAccessField(t, entry, "path_template", "/items/{item_id}")
		assertAccessField(t, entry, "operation_id", "get_item")
	}
	assertAccessField(t, entries[2], "path_template", "/extended/{item-id}")
	assertAccessField(t, entries[2], "operation_id", "get_extended")
	assertAccessField(
		t, entries[3], "path_template",
		"/long/{aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}",
	)
	assertAccessField(t, entries[3], "operation_id", "get_long")
	assertAccessField(t, entries[4], "path_template", "/control/{item}")
	assertAccessField(t, entries[4], "operation_id", "get\ncontrol")
}

func TestHumatestPreservesMatchedQuestionAndCompositeLookingTemplates(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	_, api := humatest.New(t)
	api.UseMiddleware(AccessLogger(AccessLoggerConfig{Logger: logger}))
	for _, operation := range []huma.Operation{
		{
			OperationID: "optional-looking",
			Method:      http.MethodGet,
			Path:        "/optional/{item?}",
		},
		{
			OperationID: "composite-looking",
			Method:      http.MethodGet,
			Path:        "/reports/report-{year}.csv",
		},
	} {
		huma.Register(api, operation, func(context.Context, *struct{}) (*testOutput, error) {
			return &testOutput{Body: testBody{OK: true}}, nil
		})
	}

	for _, target := range []string{
		"/optional",
		"/reports/report-2026.csv",
	} {
		if response := api.Get(target); response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, response.Code)
		}
	}
	if response := api.Get("/optional/value"); response.Code != http.StatusOK {
		t.Fatalf("GET /optional/value status = %d, want 200", response.Code)
	}
	if response := api.Get("/reports/report-:year.csv"); response.Code != http.StatusOK {
		t.Fatalf("GET literal composite spelling status = %d, want 200", response.Code)
	}
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 2 {
		t.Fatalf("access log count = %d, want 2; entries=%#v", len(entries), entries)
	}
	assertAccessField(t, entries[0], "path_template", "/optional/{item?}")
	assertAccessField(t, entries[1], "path_template", "/reports/report-{year}.csv")
}

func TestAccessMetadataPreservesStaticOperationIDsAndValidUserAgentWhitespace(t *testing.T) {
	t.Parallel()

	if value, ok := singleValidUserAgent([]string{"agent/1\tcomponent/2"}); !ok || value != "agent/1\tcomponent/2" {
		t.Fatalf("valid tab-separated User-Agent = %q, %v", value, ok)
	}
	for _, value := range []string{"~", "agent/1 component/2", "\u0080", "\u00ff"} {
		if got, ok := singleValidUserAgent([]string{value}); !ok || got != value {
			t.Fatalf("valid User-Agent boundary %q = %q, %v", value, got, ok)
		}
	}
	for _, value := range []string{" ", "\t", " edge", "edge ", "\x00", "\x1f", "\x7f", "\x80", "\xff"} {
		if got, ok := singleValidUserAgent([]string{value}); ok || got != "" {
			t.Fatalf("unsafe User-Agent boundary %q = %q, %v", value, got, ok)
		}
	}
}

func TestAccessLoggerPreservesValidUnicodeUserAgentThroughRouteAndWriter(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "unicode", value: "\u0080\u00ff"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Writer: &buffer})
			if err != nil {
				t.Fatal(err)
			}

			handler, api := humatest.New(t)
			api.UseMiddleware(AccessLogger(AccessLoggerConfig{
				Logger:           logger,
				CaptureUserAgent: true,
			}))
			huma.Register(api, huma.Operation{
				OperationID: "get-user-agent",
				Method:      http.MethodGet,
				Path:        "/user-agent",
			}, func(context.Context, *struct{}) (*testOutput, error) {
				return &testOutput{Body: testBody{OK: true}}, nil
			})

			request := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/user-agent", nil,
			)
			request.Header["User-Agent"] = []string{test.value}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("response status = %d, want 200", response.Code)
			}
			entry := decodeLogLines(t, buffer.String())
			if len(entry) != 1 {
				t.Fatalf("log line count = %d, want 1", len(entry))
			}
			assertAccessField(t, entry[0], "user_agent", test.value)
		})
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
	extraCalls := 0

	AccessLogger(AccessLoggerConfig{
		Logger: logger,
		StatusLevel: func(int) zapcore.Level {
			return zapcore.DebugLevel
		},
		ExtraFields: func(huma.Context) []zap.Field {
			extraCalls++
			return []zap.Field{zap.String("unexpected", "value")}
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
	if extraCalls != 0 {
		t.Fatalf("ExtraFields called %d times for a disabled log level", extraCalls)
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

func TestAccessLoggerPreservesHandlerPanicWhenTelemetryFails(t *testing.T) {
	t.Parallel()
	type panicMarker struct{ name string }
	handlerPanic := &panicMarker{name: "handler"}
	enrichmentPanic := &panicMarker{name: "enrichment"}
	for _, test := range []struct {
		name   string
		config func(t *testing.T) AccessLoggerConfig
	}{
		{
			name: "enrichment panic",
			config: func(*testing.T) AccessLoggerConfig {
				return AccessLoggerConfig{ExtraFields: func(huma.Context) []zap.Field { panic(enrichmentPanic) }}
			},
		},
		{
			name: "writer panic",
			config: func(t *testing.T) AccessLoggerConfig {
				t.Helper()
				logger, err := NewLogger(LoggerConfig{Writer: panickingAccessWriter{}})
				if err != nil {
					t.Fatalf("NewLogger returned error: %v", err)
				}
				return AccessLoggerConfig{Logger: logger}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := newHumaTestContext(http.MethodGet, "/panic", nil)
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				AccessLogger(test.config(t))(ctx, func(huma.Context) { panic(handlerPanic) })
			}()
			if recovered != handlerPanic {
				t.Fatalf("recovered panic = %#v, want original %#v", recovered, handlerPanic)
			}
		})
	}
}

func TestAccessLoggerDoesNotRetryWriterOrRecurseWhenDiagnosticSinkFails(t *testing.T) {
	t.Parallel()
	mainCalls := 0
	diagnosticCalls := 0
	logger, err := NewLogger(LoggerConfig{
		Writer: countingFailingAccessWriter{calls: &mainCalls, err: errors.New("writer failed")},
		ErrorWriter: countingFailingAccessWriter{
			calls: &diagnosticCalls, err: errors.New("diagnostic failed"),
		},
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", nil)
	handlerCalled := false

	AccessLogger(AccessLoggerConfig{Logger: logger})(ctx, func(huma.Context) {
		handlerCalled = true
	})

	if !handlerCalled {
		t.Fatal("telemetry failure changed handler execution")
	}
	if mainCalls != 1 || diagnosticCalls != 1 {
		t.Fatalf("writes = main %d, diagnostic %d; want exactly one each", mainCalls, diagnosticCalls)
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
		"user_agent",
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

func TestAccessLoggerExtraFieldsPreserveContextualAndNestedFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/nested", map[string]string{
		defaultRequestIDHeader: "req-extra-nested",
	})

	AccessLogger(AccessLoggerConfig{
		Logger: logger,
		ExtraFields: func(huma.Context) []zap.Field {
			return []zap.Field{
				zap.String("remote_ip", "application-peer"),
				zap.String("logging.googleapis.com/spanId", "application-span"),
				zap.String("logging.googleapis.com/future", "provider-extension"),
				zap.Any("logging.googleapis.com/labels", map[string]string{"component": "worker"}),
				zap.String("obs.custom", "visible"),
				zap.String("_obs_debug", "visible"),
				zap.Namespace("job"),
				zap.String("status", "running"),
				zap.String("method", "worker"),
				zap.String("request_id", "nested-request"),
			}
		},
	})(ctx, func(next huma.Context) {
		next.SetStatus(http.StatusOK)
	})

	line := strings.TrimSpace(buffer.String())
	if count := strings.Count(line, `"logging.googleapis.com/labels"`); count != 1 {
		t.Fatalf("logging.googleapis.com/labels key count = %d, want 1; line=%s", count, line)
	}
	entry := decodeSingleLogLine(t, line)
	assertAccessField(t, entry, "request_id", "req-extra-nested")
	assertAccessField(t, entry, "status", float64(http.StatusOK))
	assertAccessField(t, entry, "remote_ip", "application-peer")
	assertAccessField(t, entry, "logging.googleapis.com/spanId", "application-span")
	assertAccessField(t, entry, "logging.googleapis.com/future", "provider-extension")
	accessLabels, ok := entry["logging.googleapis.com/labels"].(map[string]any)
	if !ok {
		t.Fatalf("access labels object missing: %#v", entry)
	}
	assertAccessField(t, accessLabels, "component", "worker")
	assertAccessField(t, entry, "obs.custom", "visible")
	assertAccessField(t, entry, "_obs_debug", "visible")
	job, ok := entry["job"].(map[string]any)
	if !ok {
		t.Fatalf("nested job object missing: %#v", entry)
	}
	assertAccessField(t, job, "status", "running")
	assertAccessField(t, job, "method", "worker")
	assertAccessField(t, job, "request_id", "nested-request")
}

func TestAccessLoggerProtectsCallerAndRandomTraceFieldsFromExtraFields(t *testing.T) {
	t.Parallel()

	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-02"
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer, AddCaller: true})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultTraceparentHeader: traceparent,
	})

	RequestContext(RequestContextConfig{TraceContextLevel: TraceContextLevel2})(ctx, func(ctx huma.Context) {
		AccessLogger(AccessLoggerConfig{
			Logger:            logger,
			TraceContextLevel: TraceContextLevel2,
			ExtraFields: func(huma.Context) []zap.Field {
				return []zap.Field{
					zap.String("caller", "forged.go:1"),
					zap.Bool("trace_id_random", false),
				}
			},
		})(ctx, func(huma.Context) {})
	})

	line := strings.TrimSpace(buffer.String())
	if got := strings.Count(line, `"caller"`); got != 1 {
		t.Fatalf("caller key count = %d, want 1; line=%s", got, line)
	}
	if got := strings.Count(line, `"trace_id_random"`); got != 1 {
		t.Fatalf("trace_id_random key count = %d, want 1; line=%s", got, line)
	}
	if strings.Contains(line, "forged.go:1") {
		t.Fatalf("forged caller leaked into access log: %s", line)
	}
	entry := decodeSingleLogLine(t, line)
	if caller, ok := entry["caller"].(string); !ok || !strings.Contains(caller, "accesslog.go:") {
		t.Fatalf("caller = %#v, want package access-log call site", entry["caller"])
	}
	assertAccessField(t, entry, "trace_id_random", true)
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

	RequestContext(RequestContextConfig{Preset: PresetGCP})(ctx, func(ctx huma.Context) {
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
	assertAccessField(t, httpRequest, "latency", "1.500s")
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

			RequestContext(RequestContextConfig{Preset: tt.preset})(ctx, func(ctx huma.Context) {
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

			RequestContext(RequestContextConfig{Preset: tt.preset})(ctx, func(ctx huma.Context) {
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
			RequestContext(RequestContextConfig{Preset: preset})(ctx, func(ctx huma.Context) {
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
		traceLevel TraceContextLevel
		spoofKey   string
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
			spoofKey:  "logging.googleapis.com/trace",
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
			spoofKey:  "xray_trace_id",
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
			spoofKey:  "operation_Id",
		},
		{
			name:       "aws xray id with level two random flag",
			preset:     PresetAWS,
			flags:      "03",
			sampled:    true,
			traceLevel: TraceContextLevel2,
			wantFields: map[string]any{
				"trace_id_random": true,
				"xray_trace_id":   "1-4efaaf4d-1e8720b39541901950019ee5",
			},
			rejectKeys: []string{"operation_Id", "operation_ParentId"},
			wantLevel:  "INFO",
			levelKey:   "level",
			spoofKey:   "xray_trace_id",
		},
		{
			name:       "azure operation fields with level two random flag",
			preset:     PresetAzure,
			flags:      "03",
			sampled:    true,
			traceLevel: TraceContextLevel2,
			wantFields: map[string]any{
				"trace_id_random":    true,
				"operation_Id":       traceID,
				"operation_ParentId": parentID,
			},
			rejectKeys: []string{"xray_trace_id"},
			wantLevel:  "INFO",
			levelKey:   "level",
			spoofKey:   "operation_Id",
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

			RequestContext(RequestContextConfig{
				Preset: tt.preset, TraceContextLevel: tt.traceLevel,
			})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger, Preset: tt.preset, TraceContextLevel: tt.traceLevel,
				})(ctx, func(ctx huma.Context) {
					Logger(ctx.Context()).Info("handler cloud log", zap.String(tt.spoofKey, "spoofed-provider-trace"))
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

func TestAWSAndAzureProfilesOmitDuplicateTraceparentCorrelation(t *testing.T) {
	t.Parallel()
	for _, preset := range []Preset{PresetAWS, PresetAzure} {
		t.Run(string(preset), func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: preset, Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/trace", nil)
			request.Header.Set(defaultRequestIDHeader, "duplicate-trace")
			request.Header[http.CanonicalHeaderKey(defaultTraceparentHeader)] = []string{
				"00-4efaaf4d1e8720b39541901950019ee5-00f067aa0ba902b7-03",
				"00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
			}
			ctx := humatest.NewContext(
				&huma.Operation{
					Method:        http.MethodGet,
					Path:          "/trace",
					OperationID:   "trace",
					DefaultStatus: http.StatusOK,
				},
				request,
				httptest.NewRecorder(),
			)

			RequestContext(RequestContextConfig{
				Preset: preset, TraceContextLevel: TraceContextLevel2,
			})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger, Preset: preset, TraceContextLevel: TraceContextLevel2,
				})(ctx, func(ctx huma.Context) {
					Logger(ctx.Context()).Info("handler")
				})
			})

			for _, entry := range decodeLogLines(t, buffer.String()) {
				assertAccessField(t, entry, "request_id", "duplicate-trace")
				assertAccessField(t, entry, "correlation_id", "duplicate-trace")
				for _, key := range []string{
					"trace_id", "parent_id", "trace_flags", "trace_sampled", "trace_id_random",
					"xray_trace_id", "operation_Id", "operation_ParentId",
				} {
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
		"level",
		"severity",
		"httpRequest",
		"logging.googleapis.com/trace",
		"logging.googleapis.com/trace_sampled",
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

			RequestContext(RequestContextConfig{Preset: preset})(ctx, func(ctx huma.Context) {
				AccessLogger(AccessLoggerConfig{
					Logger: logger,
					Preset: preset,
					ExtraFields: func(huma.Context) []zap.Field {
						return []zap.Field{
							zap.String("level", "bad-level"),
							zap.String("severity", "bad-severity"),
							zap.String("httpRequest", "bad-http-request"),
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
			for _, key := range providerKeys {
				if got := strings.Count(line, `"`+key+`"`); got != 1 {
					t.Fatalf("%s key count = %d, want 1; line=%s", key, got, line)
				}
			}

			entry := decodeSingleLogLine(t, line)
			assertAccessField(t, entry, "tenant_id", "tenant-1")
			assertAccessField(t, entry, "logging.googleapis.com/spanId", "bad-gcp-span")
			want := map[string]any{
				"level":                                "bad-level",
				"severity":                             "bad-severity",
				"httpRequest":                          "bad-http-request",
				"logging.googleapis.com/trace":         "bad-gcp-trace",
				"logging.googleapis.com/trace_sampled": false,
				"xray_trace_id":                        "bad-xray-trace",
				"operation_Id":                         "bad-azure-operation",
				"operation_ParentId":                   "bad-azure-parent",
			}
			switch preset {
			case PresetGCP:
				want["severity"] = "INFO"
				delete(want, "httpRequest")
				want["logging.googleapis.com/trace"] = "4efaaf4d1e8720b39541901950019ee5"
				want["logging.googleapis.com/trace_sampled"] = true
			case PresetAWS:
				want["level"] = "INFO"
				want["xray_trace_id"] = "1-4efaaf4d-1e8720b39541901950019ee5"
			case PresetAzure:
				want["level"] = "INFO"
				want["operation_Id"] = "4efaaf4d1e8720b39541901950019ee5"
				want["operation_ParentId"] = "00f067aa0ba902b7"
			default:
				want["level"] = "INFO"
			}
			for key, value := range want {
				assertAccessField(t, entry, key, value)
			}
			if preset == PresetGCP {
				if _, ok := entry["httpRequest"].(map[string]any); !ok {
					t.Fatalf("GCP httpRequest was displaced by application data: %#v", entry)
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

func TestRequestPathPreservesFrameworkEscapedPathRepresentations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		url  string
		want string
	}{
		{name: "origin form", url: "/widgets/a%2Fb?secret=true", want: "/widgets/a%2Fb"},
		{name: "literal hash path data", url: "/widgets/a#literal", want: "/widgets/a%23literal"},
		{name: "empty path", url: "https://example.test", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.url, nil)
			if test.name == "empty path" && request.URL.Path != "" {
				t.Fatalf("absolute-form request path = %q, want native empty path", request.URL.Path)
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
	if got := requestPath(ctx); got != "/widgets/a/b" {
		t.Fatalf("requestPath() = %q, want framework EscapedPath fallback", got)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "*", nil)
	ctx = humatest.NewContext(
		&huma.Operation{Method: http.MethodOptions, Path: "*"},
		request,
		httptest.NewRecorder(),
	)
	if got := requestPath(ctx); got != "*" {
		t.Fatalf("asterisk-form requestPath() = %q, want *", got)
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
		{in: 10 * time.Millisecond, want: "0.010s"},
		{in: 12_500 * time.Microsecond, want: "0.012500s"},
		{in: 3 * time.Second, want: "3s"},
		{in: 1500 * time.Millisecond, want: "1.500s"},
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

func TestAccessLoggerResolvesAWSAndAzureProfileVersionsAtConstruction(t *testing.T) {
	t.Parallel()
	awsLatest := normalizeAccessLoggerConfig(AccessLoggerConfig{Preset: PresetAWS})
	awsPinned := normalizeAccessLoggerConfig(AccessLoggerConfig{
		Preset: PresetAWS, AWSProfileVersion: AWSProfileVersionV0_1_0,
	})
	if awsLatest.AWSProfileVersion != AWSProfileVersionV0_1_0 ||
		awsPinned.AWSProfileVersion != AWSProfileVersionV0_1_0 {
		t.Fatalf("AWS profiles = %q/%q, want 0.1.0", awsLatest.AWSProfileVersion, awsPinned.AWSProfileVersion)
	}
	azureLatest := normalizeAccessLoggerConfig(AccessLoggerConfig{Preset: PresetAzure})
	azurePinned := normalizeAccessLoggerConfig(AccessLoggerConfig{
		Preset: PresetAzure, AzureProfileVersion: AzureProfileVersionV0_1_0,
	})
	if azureLatest.AzureProfileVersion != AzureProfileVersionV0_1_0 ||
		azurePinned.AzureProfileVersion != AzureProfileVersionV0_1_0 {
		t.Fatalf(
			"Azure profiles = %q/%q, want 0.1.0",
			azureLatest.AzureProfileVersion,
			azurePinned.AzureProfileVersion,
		)
	}
}
