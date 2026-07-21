package obs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"go.uber.org/zap"
)

func TestDefaultValidateRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "letters digits punctuation", value: "abc-XYZ_123.~", want: true},
		{name: "alphanumeric lower boundaries", value: "0Aa", want: true},
		{name: "alphanumeric upper boundaries", value: "9Zz", want: true},
		{name: "maximum length", value: strings.Repeat("a", 128), want: true},
		{name: "empty", value: "", want: false},
		{name: "overlong", value: strings.Repeat("a", 129), want: false},
		{name: "space", value: "abc def", want: false},
		{name: "slash", value: "abc/def", want: false},
		{name: "after digit range", value: "abc:def", want: false},
		{name: "before uppercase range", value: "abc@def", want: false},
		{name: "after uppercase range", value: "abc[def", want: false},
		{name: "before lowercase range", value: "abc`def", want: false},
		{name: "after lowercase range", value: "abc{def", want: false},
		{name: "non ascii", value: "å", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultValidateRequestID(tt.value); got != tt.want {
				t.Fatalf("DefaultValidateRequestID(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestRequestContextRequestIDLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		headerValue  string
		newID        string
		wantID       string
		wantResponse string
	}{
		{
			name:         "existing valid id reused",
			headerValue:  "client-123",
			newID:        "generated-1",
			wantID:       "client-123",
			wantResponse: "client-123",
		},
		{
			name:         "missing id generated",
			headerValue:  "",
			newID:        "generated-2",
			wantID:       "generated-2",
			wantResponse: "generated-2",
		},
		{
			name:         "unsafe id replaced",
			headerValue:  "bad value",
			newID:        "generated-3",
			wantID:       "generated-3",
			wantResponse: "generated-3",
		},
		{
			name:         "overlong id replaced",
			headerValue:  strings.Repeat("a", 129),
			newID:        "generated-4",
			wantID:       "generated-4",
			wantResponse: "generated-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
				defaultRequestIDHeader: tt.headerValue,
			})
			var gotContext context.Context
			RequestContext(RequestContextConfig{
				NewRequestID: func() string { return tt.newID },
			})(ctx, func(next huma.Context) {
				gotContext = next.Context()
			})

			if got := RequestID(gotContext); got != tt.wantID {
				t.Fatalf("RequestID = %q, want %q", got, tt.wantID)
			}
			if got := CorrelationID(gotContext); got != tt.wantID {
				t.Fatalf("CorrelationID = %q, want %q", got, tt.wantID)
			}
			if got := recorder.Header().Get(defaultRequestIDHeader); got != tt.wantResponse {
				t.Fatalf("response request ID header = %q, want %q", got, tt.wantResponse)
			}
		})
	}
}

func TestRequestContextUsesCustomRequestIDPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		incoming      string
		want          string
		wantNewIDCall bool
	}{
		{
			name:     "custom-valid incoming id is preserved",
			incoming: "tenant-client",
			want:     "tenant-client",
		},
		{
			name:     "custom validator admits colon",
			incoming: "tenant:client",
			want:     "tenant:client",
		},
		{
			name:     "custom validator admits 129 bytes",
			incoming: strings.Repeat("x", 129),
			want:     strings.Repeat("x", 129),
		},
		{
			name:          "custom-invalid incoming id is replaced",
			incoming:      "client-123",
			want:          "tenant-generated",
			wantNewIDCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
				defaultRequestIDHeader: tt.incoming,
			})
			newIDCalls := 0
			RequestContext(RequestContextConfig{
				NewRequestID: func() string {
					newIDCalls++
					return "tenant-generated"
				},
				ValidateRequestID: func(value string) bool {
					return strings.HasPrefix(value, "tenant-") ||
						value == "tenant:client" || len(value) == 129
				},
			})(ctx, func(next huma.Context) {
				if got := RequestID(next.Context()); got != tt.want {
					t.Fatalf("RequestID = %q, want %q", got, tt.want)
				}
			})

			wantCalls := 0
			if tt.wantNewIDCall {
				wantCalls = 1
			}
			if newIDCalls != wantCalls {
				t.Fatalf("NewRequestID calls = %d, want %d", newIDCalls, wantCalls)
			}
			if got := recorder.Header().Get(defaultRequestIDHeader); got != tt.want {
				t.Fatalf("response request ID header = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNativeRequestIDBoundaryAllowsOnlyHTTPFieldText(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"~", "tenant request", "tenant\trequest", "\u0080", "\u00ff"} {
		if !nativeSafeRequestID(value) {
			t.Fatalf("native-safe request ID boundary %q was rejected", value)
		}
	}
	for _, value := range []string{"", " ", "\t", " tenant", "tenant ", "\ttenant", "tenant\t", "\x00", "\x1f", "\x7f", "\x80", "\xff"} {
		if nativeSafeRequestID(value) {
			t.Fatalf("unsafe request ID boundary %q was accepted", value)
		}
	}
}

func TestCustomRequestIDValidatorRunsOnlyForRFCFieldContent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"tenant request", "tenant\trequest", "tenant,request"} {
		calls := 0
		if !validIncomingRequestID(value, func(string) bool { calls++; return true }) || calls != 1 {
			t.Fatalf("valid field content %q: accepted=false or calls=%d", value, calls)
		}
	}
	for _, value := range []string{" tenant", "tenant ", "\x80"} {
		calls := 0
		if validIncomingRequestID(value, func(string) bool { calls++; return true }) || calls != 0 {
			t.Fatalf("unsafe field content %q reached validator %d times", value, calls)
		}
	}
}

func TestRequestContextRetriesInvalidGeneratedIDsAndFallsBack(t *testing.T) {
	t.Parallel()

	t.Run("second generated id is accepted", func(t *testing.T) {
		t.Parallel()

		calls := 0
		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
		var handlerRequestID string
		RequestContext(RequestContextConfig{
			NewRequestID: func() string {
				calls++
				if calls == 1 {
					return "invalid value"
				}
				return "valid-on-retry"
			},
		})(ctx, func(next huma.Context) {
			handlerRequestID = RequestID(next.Context())
		})

		if calls != 2 {
			t.Fatalf("NewRequestID calls = %d, want 2", calls)
		}
		assertRequestIDSurfaces(t, handlerRequestID, "valid-on-retry", recorder)
	})

	t.Run("two invalid generated ids use safe fallback", func(t *testing.T) {
		t.Parallel()

		calls := 0
		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
		var handlerRequestID string
		var handlerCorrelationID string
		RequestContext(RequestContextConfig{
			NewRequestID: func() string {
				calls++
				return "still invalid"
			},
		})(ctx, func(next huma.Context) {
			handlerRequestID = RequestID(next.Context())
			handlerCorrelationID = CorrelationID(next.Context())
		})

		if calls != 2 {
			t.Fatalf("NewRequestID calls = %d, want 2", calls)
		}
		assertGeneratedRequestID(t, handlerRequestID)
		assertRequestIDSurfaces(t, handlerRequestID, handlerRequestID, recorder)
		if handlerCorrelationID != handlerRequestID {
			t.Fatalf("CorrelationID = %q, want fallback request ID %q", handlerCorrelationID, handlerRequestID)
		}
	})

	t.Run("validator applies only to caller input", func(t *testing.T) {
		t.Parallel()

		calls := 0
		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
		var handlerRequestID string
		var handlerCorrelationID string
		RequestContext(RequestContextConfig{
			NewRequestID: func() string {
				calls++
				return "rejected"
			},
			ValidateRequestID: func(string) bool { return false },
		})(ctx, func(next huma.Context) {
			handlerRequestID = RequestID(next.Context())
			handlerCorrelationID = CorrelationID(next.Context())
		})

		if calls != 1 {
			t.Fatalf("NewRequestID calls = %d, want 1", calls)
		}
		assertRequestIDSurfaces(t, handlerRequestID, "rejected", recorder)
		if handlerCorrelationID != "rejected" {
			t.Fatalf("CorrelationID = %q, want generated request ID rejected", handlerCorrelationID)
		}
	})

	t.Run("generator panics are retried then safely replaced", func(t *testing.T) {
		t.Parallel()

		calls := 0
		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
		var handlerCalled bool
		var handlerRequestID string
		RequestContext(RequestContextConfig{
			NewRequestID: func() string {
				calls++
				panic("generator secret")
			},
		})(ctx, func(next huma.Context) {
			handlerCalled = true
			handlerRequestID = RequestID(next.Context())
		})

		if calls != 2 {
			t.Fatalf("NewRequestID calls = %d, want 2", calls)
		}
		if !handlerCalled {
			t.Fatal("handler was not called")
		}
		assertGeneratedRequestID(t, handlerRequestID)
		assertRequestIDSurfaces(t, handlerRequestID, handlerRequestID, recorder)
	})

	t.Run("validator panic rejects caller and preserves traffic", func(t *testing.T) {
		t.Parallel()

		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
			defaultRequestIDHeader: "caller",
		})
		var handlerCalled bool
		RequestContext(RequestContextConfig{
			NewRequestID: func() string { return "generated" },
			ValidateRequestID: func(string) bool {
				panic("validator secret")
			},
		})(ctx, func(next huma.Context) {
			handlerCalled = true
			if got := RequestID(next.Context()); got != "generated" {
				t.Fatalf("RequestID = %q, want generated", got)
			}
		})

		if !handlerCalled {
			t.Fatal("handler was not called")
		}
		if got := recorder.Header().Get(defaultRequestIDHeader); got != "generated" {
			t.Fatalf("response request ID header = %q, want generated", got)
		}
	})
}

func TestRequestContextFallbackIDsAreUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()

	const requests = 64
	middleware := RequestContext(RequestContextConfig{
		NewRequestID: func() string { return "invalid value" },
	})
	type result struct {
		requestID     string
		correlationID string
		responseID    string
	}
	results := make(chan result, requests)
	var waitGroup sync.WaitGroup
	waitGroup.Add(requests)
	for range requests {
		go func() {
			defer waitGroup.Done()
			ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
			var got result
			middleware(ctx, func(next huma.Context) {
				got.requestID = RequestID(next.Context())
				got.correlationID = CorrelationID(next.Context())
			})
			got.responseID = recorder.Header().Get(defaultRequestIDHeader)
			results <- got
		}()
	}
	waitGroup.Wait()
	close(results)

	seen := make(map[string]struct{}, requests)
	for got := range results {
		assertGeneratedRequestID(t, got.requestID)
		if got.correlationID != got.requestID || got.responseID != got.requestID {
			t.Fatalf("request ID surfaces diverged: %#v", got)
		}
		if _, duplicate := seen[got.requestID]; duplicate {
			t.Fatalf("duplicate fallback request ID generated concurrently: %q", got.requestID)
		}
		seen[got.requestID] = struct{}{}
	}
	if len(seen) != requests {
		t.Fatalf("unique fallback request IDs = %d, want %d", len(seen), requests)
	}
}

func TestRequestContextDefaultGeneratorProducesUniqueHexIDs(t *testing.T) {
	t.Parallel()

	const requests = 16
	middleware := RequestContext(RequestContextConfig{})
	seen := make(map[string]struct{}, requests)
	for range requests {
		ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
		var requestID string
		var correlationID string
		middleware(ctx, func(next huma.Context) {
			requestID = RequestID(next.Context())
			correlationID = CorrelationID(next.Context())
		})

		assertGeneratedRequestID(t, requestID)
		if correlationID != requestID {
			t.Fatalf("CorrelationID = %q, want generated request ID %q", correlationID, requestID)
		}
		if got := recorder.Header().Get(defaultRequestIDHeader); got != requestID {
			t.Fatalf("response request ID header = %q, want %q", got, requestID)
		}
		if _, duplicate := seen[requestID]; duplicate {
			t.Fatalf("default generator returned duplicate request ID %q", requestID)
		}
		seen[requestID] = struct{}{}
	}
}

func TestRequestContextResponseHeaderCanBeDisabled(t *testing.T) {
	t.Parallel()

	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
	called := false
	RequestContext(RequestContextConfig{
		DisableResponseHeader: true,
		NewRequestID:          func() string { return "generated" },
	})(ctx, func(next huma.Context) {
		called = true
		if got := RequestID(next.Context()); got != "generated" {
			t.Fatalf("downstream RequestID = %q, want generated", got)
		}
	})

	if got := recorder.Header().Get(defaultRequestIDHeader); got != "" {
		t.Fatalf("response request ID header = %q, want empty", got)
	}
	if !called {
		t.Fatal("RequestContext did not call downstream when the response header was disabled")
	}
}

func TestRequestContextWithoutConfiguredLoggerProvidesDisabledLogger(t *testing.T) {
	t.Parallel()

	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-no-logger",
	})
	called := false
	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		called = true
		logger := Logger(next.Context())
		if logger == nil {
			t.Fatal("Logger returned nil for request metadata without a configured logger")
		}
		if entry := logger.Check(zap.InfoLevel, "discarded"); entry != nil {
			t.Fatal("Logger enabled an entry without a configured request logger")
		}
		logger.Info("safe no-op request logger")

		if got := RequestID(next.Context()); got != "req-no-logger" {
			t.Fatalf("RequestID = %q, want req-no-logger", got)
		}
		if got := CorrelationID(next.Context()); got != "req-no-logger" {
			t.Fatalf("CorrelationID = %q, want req-no-logger", got)
		}
	})

	if !called {
		t.Fatal("RequestContext did not call downstream without a configured logger")
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != "req-no-logger" {
		t.Fatalf("response request ID header = %q, want req-no-logger", got)
	}
}

func TestRequestContextLoggerEmitsTraceFieldsOnlyWhenValid(t *testing.T) {
	t.Parallel()

	const (
		traceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
		parentID    = "00f067aa0ba902b7"
		tracePrefix = "00-" + traceID + "-" + parentID + "-"
	)
	tests := []struct {
		name            string
		traceparent     string
		traceLevel      TraceContextLevel
		wantCorrelation string
		wantTraceFields bool
		wantFlags       string
		wantRandom      bool
	}{
		{name: "without trace", wantCorrelation: "req-huma-logger"},
		{
			name:            "with valid trace",
			traceparent:     tracePrefix + "01",
			wantCorrelation: traceID,
			wantTraceFields: true,
			wantFlags:       "01",
		},
		{
			name:            "with Level 2 random trace",
			traceparent:     tracePrefix + "03",
			traceLevel:      TraceContextLevel2,
			wantCorrelation: traceID,
			wantTraceFields: true,
			wantFlags:       "03",
			wantRandom:      true,
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
			ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
				defaultRequestIDHeader:   "req-huma-logger",
				defaultTraceparentHeader: tt.traceparent,
			})

			RequestContext(RequestContextConfig{
				Logger: logger, TraceContextLevel: tt.traceLevel,
			})(ctx, func(next huma.Context) {
				Logger(next.Context()).Info("huma handler")
			})

			entry := decodeSingleLogLine(t, buffer.String())
			assertAccessField(t, entry, "message", "huma handler")
			assertAccessField(t, entry, "request_id", "req-huma-logger")
			assertAccessField(t, entry, "correlation_id", tt.wantCorrelation)
			traceFields := map[string]any{
				"trace_id":      traceID,
				"parent_id":     parentID,
				"trace_flags":   tt.wantFlags,
				"trace_sampled": true,
			}
			for key, want := range traceFields {
				if !tt.wantTraceFields {
					assertNoAccessField(t, entry, key)
					continue
				}
				assertAccessField(t, entry, key, want)
			}
			if tt.traceLevel == TraceContextLevel2 && tt.wantTraceFields {
				assertAccessField(t, entry, "trace_id_random", tt.wantRandom)
			} else {
				assertNoAccessField(t, entry, "trace_id_random")
			}
		})
	}
}

func TestRequestContextLoggerPresetAddsProviderTraceFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader:   "req-gcp-huma",
		defaultTraceparentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})

	RequestContext(RequestContextConfig{
		Logger: logger,
		Preset: PresetGCP,
	})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("gcp huma handler")
	})

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["severity"]; got != "INFO" {
		t.Fatalf("severity = %v", got)
	}
	if got := entry["logging.googleapis.com/trace"]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("gcp trace = %v", got)
	}
	if got := entry["logging.googleapis.com/trace_sampled"]; got != true {
		t.Fatalf("gcp trace_sampled = %v", got)
	}
}

func TestRequestContextPreservesExistingMetadataLogger(t *testing.T) {
	t.Parallel()

	var existingBuffer bytes.Buffer
	existingLogger, err := NewLogger(LoggerConfig{Writer: &existingBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	var configBuffer bytes.Buffer
	configLogger, err := NewLogger(LoggerConfig{Writer: &configBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	metadata := &requestMetadata{
		RequestID:     "existing-req",
		CorrelationID: "existing-corr",
	}
	metadata.Logger = loggerWithMetadata(
		existingLogger.With(zap.String("logger_source", "existing")),
		metadata,
		PresetDefault,
	)
	originalLogger := metadata.Logger
	req := httptest.NewRequestWithContext(
		contextWithRequestMetadata(context.Background(), metadata),
		http.MethodGet,
		"/test",
		nil,
	)
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		httptest.NewRecorder(),
	)

	RequestContext(RequestContextConfig{
		Logger: configLogger.With(zap.String("logger_source", "config")),
	})(ctx, func(next huma.Context) {
		if got := RequestID(next.Context()); got != "existing-req" {
			t.Fatalf("RequestID = %q, want existing-req", got)
		}
		if got := CorrelationID(next.Context()); got != "existing-corr" {
			t.Fatalf("CorrelationID = %q, want existing-corr", got)
		}
		Logger(next.Context()).Info("preserved logger")
	})

	entry := decodeSingleLogLine(t, existingBuffer.String())
	if got := entry["logger_source"]; got != "existing" {
		t.Fatalf("logger_source = %v", got)
	}
	if got := entry["request_id"]; got != "existing-req" {
		t.Fatalf("request_id = %v, want existing-req", got)
	}
	if got := entry["correlation_id"]; got != "existing-corr" {
		t.Fatalf("correlation_id = %v, want existing-corr", got)
	}
	if got := strings.TrimSpace(configBuffer.String()); got != "" {
		t.Fatalf("config logger was used: %s", got)
	}
	if metadata.Logger != originalLogger {
		t.Fatal("RequestContext replaced the logger on the incoming metadata")
	}
}

func TestRequestContextLoggerInstallDoesNotMutateExistingMetadata(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	metadata := &requestMetadata{
		RequestID:     "existing-req",
		CorrelationID: "existing-corr",
	}
	req := httptest.NewRequestWithContext(
		contextWithRequestMetadata(context.Background(), metadata),
		http.MethodGet,
		"/test",
		nil,
	)
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		httptest.NewRecorder(),
	)

	RequestContext(RequestContextConfig{
		Logger: logger,
	})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("installed without mutation")
	})

	if metadata.Logger != nil {
		t.Fatal("RequestContext mutated input metadata")
	}
	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["message"]; got != "installed without mutation" {
		t.Fatalf("message = %v", got)
	}
	if got := entry["request_id"]; got != "existing-req" {
		t.Fatalf("request_id = %v, want existing-req", got)
	}
	if got := entry["correlation_id"]; got != "existing-corr" {
		t.Fatalf("correlation_id = %v, want existing-corr", got)
	}
}

func TestRequestContextTraceCorrelation(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader:   "req-1",
		defaultTraceparentHeader: traceparent,
		defaultTracestateHeader:  "vendor=value",
	})

	var gotContext context.Context
	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		gotContext = next.Context()
	})

	if got := RequestID(gotContext); got != "req-1" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("CorrelationID = %q", got)
	}
	trace := Trace(gotContext)
	if !trace.Valid {
		t.Fatal("Trace.Valid = false")
	}
	if trace.Tracestate != "vendor=value" {
		t.Fatalf("Tracestate = %q", trace.Tracestate)
	}
	if !trace.Sampled {
		t.Fatal("Sampled = false, want true")
	}
}

func TestRequestContextCombinesTracestateHeaders(t *testing.T) {
	t.Parallel()

	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	req.Header.Set("Traceparent", traceparent)
	req.Header.Add("Tracestate", "vendor=value")
	req.Header.Add("Tracestate", "other=value")
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		httptest.NewRecorder(),
	)

	var got TraceContext
	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		got = Trace(next.Context())
	})
	if got.Tracestate != "vendor=value,other=value" {
		t.Fatalf("tracestate = %q", got.Tracestate)
	}
}

func TestRequestContextRejectsDuplicateRequestIDAndTraceparentFieldLines(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name   string
		header string
		values []string
	}{
		{name: "identical request IDs", header: defaultRequestIDHeader, values: []string{"caller", "caller"}},
		{name: "different request IDs", header: defaultRequestIDHeader, values: []string{"caller", "other"}},
		{name: "identical traceparents", header: defaultTraceparentHeader, values: []string{traceparent, traceparent}},
		{
			name:   "different traceparents",
			header: defaultTraceparentHeader,
			values: []string{traceparent, strings.Replace(traceparent, "-01", "-00", 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
			req.Header.Set(defaultRequestIDHeader, "caller")
			if tt.header == defaultTraceparentHeader {
				req.Header.Set(defaultRequestIDHeader, "request")
			}
			req.Header[http.CanonicalHeaderKey(tt.header)] = append([]string(nil), tt.values...)
			ctx := humatest.NewContext(
				&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
				req,
				httptest.NewRecorder(),
			)
			var requestID string
			var trace TraceContext
			RequestContext(RequestContextConfig{NewRequestID: func() string { return "generated" }})(
				ctx,
				func(next huma.Context) {
					requestID = RequestID(next.Context())
					trace = Trace(next.Context())
				},
			)
			if tt.header == defaultRequestIDHeader {
				if requestID != "generated" || trace.Valid {
					t.Fatalf("duplicate request ID selected: request_id=%q trace=%#v", requestID, trace)
				}
				return
			}
			if requestID != "request" || trace.Valid {
				t.Fatalf("duplicate traceparent selected: request_id=%q trace=%#v", requestID, trace)
			}
		})
	}
}

func TestRequestContextLevel2AndConstructionValidation(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-03"
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultTraceparentHeader: traceparent,
	})
	var got TraceContext
	RequestContext(RequestContextConfig{TraceContextLevel: TraceContextLevel2})(
		ctx,
		func(next huma.Context) {
			got = Trace(next.Context())
		},
	)
	if !got.Valid || got.Level != TraceContextLevel2 || !got.Sampled || !got.Random {
		t.Fatalf("Level 2 request trace = %#v", got)
	}

	defer func() {
		value := recover()
		if fmt.Sprint(value) != "unsupported trace context level 3: supported levels are 1 and 2" {
			t.Fatalf("invalid-level panic = %v", value)
		}
	}()
	_ = RequestContext(RequestContextConfig{TraceContextLevel: 3})
}

func TestRequestContextTracestateLengthBoundary(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	valid512 := "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 251)
	valid513 := strings.Repeat("a", 256) + "=" + strings.Repeat("v", 256)
	tests := []struct {
		name       string
		tracestate string
		want       string
	}{
		{
			name:       "max length tracestate kept",
			tracestate: valid512,
			want:       valid512,
		},
		{name: "513 character tracestate kept", tracestate: valid513, want: valid513},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
				defaultRequestIDHeader:   "req-1",
				defaultTraceparentHeader: traceparent,
				defaultTracestateHeader:  tt.tracestate,
			})

			var gotContext context.Context
			RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
				gotContext = next.Context()
			})

			trace := Trace(gotContext)
			if !trace.Valid {
				t.Fatal("Trace.Valid = false")
			}
			if trace.Tracestate != tt.want {
				t.Fatalf("Tracestate length = %d, want %d", len(trace.Tracestate), len(tt.want))
			}
		})
	}
}

func TestRequestContextInvalidTraceFallsBackToRequestID(t *testing.T) {
	t.Parallel()

	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader:   "req-1",
		defaultTraceparentHeader: "not-a-traceparent",
		defaultTracestateHeader:  "vendor=value",
	})

	var gotContext context.Context
	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		gotContext = next.Context()
	})

	if got := CorrelationID(gotContext); got != "req-1" {
		t.Fatalf("CorrelationID = %q, want request ID", got)
	}
	if trace := Trace(gotContext); trace != (TraceContext{}) {
		t.Fatalf("Trace = %#v, want zero value for invalid traceparent with tracestate", trace)
	}
}

func TestRequestContextCustomHeaders(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		"X-Correlation-Request": "custom-id",
		"X-Traceparent":         traceparent,
		"X-Tracestate":          "custom=value",
	})

	var gotContext context.Context
	RequestContext(RequestContextConfig{
		RequestIDHeader:   "X-Correlation-Request",
		TraceparentHeader: "X-Traceparent",
		TracestateHeader:  "X-Tracestate",
	})(ctx, func(next huma.Context) {
		gotContext = next.Context()
	})

	if got := RequestID(gotContext); got != "custom-id" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if got := Trace(gotContext).Tracestate; got != "custom=value" {
		t.Fatalf("Tracestate = %q", got)
	}
	if got := recorder.Header().Get("X-Correlation-Request"); got != "custom-id" {
		t.Fatalf("response header did not default to the custom request ID header: %q", got)
	}

	defaultCtx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-1",
		"X-Traceparent":        traceparent,
	})
	RequestContext(RequestContextConfig{})(defaultCtx, func(next huma.Context) {
		if trace := Trace(next.Context()); trace.Valid {
			t.Fatalf("default config read custom trace header: %#v", trace)
		}
	})
}

func TestAccessorsAreSafeWhenMetadataIsMissing(t *testing.T) {
	t.Parallel()

	if got := RequestID(nil); got != "" { //nolint:staticcheck // RequestID explicitly supports nil contexts.
		t.Fatalf("RequestID(nil) = %q", got)
	}
	if got := CorrelationID(nil); got != "" { //nolint:staticcheck // CorrelationID explicitly supports nil contexts.
		t.Fatalf("CorrelationID(nil) = %q", got)
	}
	if trace := Trace(nil); trace != (TraceContext{}) { //nolint:staticcheck // Trace explicitly supports nil contexts.
		t.Fatalf("Trace(nil) = %#v, want zero value", trace)
	}
	if logger := Logger(nil); logger == nil { //nolint:staticcheck // Logger explicitly supports nil contexts.
		t.Fatal("Logger(nil) returned nil")
	} else {
		logger.Info("safe no-op logger")
	}
}

func TestRequestContextThroughHumaAdapter(t *testing.T) {
	t.Parallel()

	_, api := humatest.New(t)
	api.UseMiddleware(RequestContext(RequestContextConfig{}))

	var gotRequestID string
	var gotCorrelationID string
	huma.Register(api, huma.Operation{
		OperationID: "get-widget",
		Method:      http.MethodGet,
		Path:        "/widgets/{id}",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	},
	) (*testOutput, error) {
		gotRequestID = RequestID(ctx)
		gotCorrelationID = CorrelationID(ctx)
		return &testOutput{Body: testBody{OK: input.ID == "123"}}, nil
	})

	resp := api.Get("/widgets/123", "X-Request-Id: req-adapter")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if gotRequestID != "req-adapter" {
		t.Fatalf("handler RequestID = %q", gotRequestID)
	}
	if gotCorrelationID != "req-adapter" {
		t.Fatalf("handler CorrelationID = %q", gotCorrelationID)
	}
	if got := resp.Header().Get(defaultRequestIDHeader); got != "req-adapter" {
		t.Fatalf("response header = %q", got)
	}
}

func TestHTTPRequestContextRequestIDLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		headerValue string
		newID       string
		wantID      string
	}{
		{name: "existing valid id reused", headerValue: "client-http", newID: "generated-1", wantID: "client-http"},
		{name: "missing id generated", headerValue: "", newID: "generated-2", wantID: "generated-2"},
		{name: "invalid id replaced", headerValue: "bad value", newID: "generated-3", wantID: "generated-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotContext context.Context
			handler := HTTPRequestContext(HTTPRequestContextConfig{
				NewRequestID: func() string { return tt.newID },
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotContext = r.Context()
			}))
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
			req.Header.Set(defaultRequestIDHeader, tt.headerValue)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if got := RequestID(gotContext); got != tt.wantID {
				t.Fatalf("RequestID = %q, want %q", got, tt.wantID)
			}
			if got := CorrelationID(gotContext); got != tt.wantID {
				t.Fatalf("CorrelationID = %q, want %q", got, tt.wantID)
			}
			if got := recorder.Header().Get(defaultRequestIDHeader); got != tt.wantID {
				t.Fatalf("response header = %q, want %q", got, tt.wantID)
			}
		})
	}
}

type callerContextKey struct{}

func TestRequestContextMiddlewarePreservesCallerValueCancellationAndDeadline(t *testing.T) {
	t.Parallel()

	t.Run("Huma", func(t *testing.T) {
		t.Parallel()
		parent, deadline := canceledCallerContext(t)
		ctx, _ := newHumaTestContextWithParent(parent, http.MethodGet, "/test", nil)
		RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
			assertCallerContextPreserved(t, next.Context(), deadline)
		})
	})

	t.Run("net/http", func(t *testing.T) {
		t.Parallel()
		parent, deadline := canceledCallerContext(t)
		handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
			func(_ http.ResponseWriter, request *http.Request) {
				assertCallerContextPreserved(t, request.Context(), deadline)
			},
		))
		handler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequestWithContext(parent, http.MethodGet, "/http", nil),
		)
	})
}

func canceledCallerContext(t *testing.T) (context.Context, time.Time) {
	t.Helper()
	deadline := time.Now().Add(time.Hour)
	parent, cancel := context.WithDeadline(
		context.WithValue(context.Background(), callerContextKey{}, "sentinel"),
		deadline,
	)
	cancel()
	return parent, deadline
}

func assertCallerContextPreserved(t *testing.T, ctx context.Context, deadline time.Time) {
	t.Helper()
	if got := ctx.Value(callerContextKey{}); got != "sentinel" {
		t.Fatalf("caller context value = %#v, want sentinel", got)
	}
	gotDeadline, ok := ctx.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("caller deadline = (%v, %v), want %v", gotDeadline, ok, deadline)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("caller cancellation = %v, want context.Canceled", ctx.Err())
	}
}

func TestHTTPRequestContextUsesCustomRequestIDPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		incoming      string
		want          string
		wantNewIDCall bool
	}{
		{
			name:     "custom-valid incoming id is preserved",
			incoming: "tenant-http-client",
			want:     "tenant-http-client",
		},
		{
			name:          "custom-invalid incoming id is replaced",
			incoming:      "http-client",
			want:          "tenant-http-generated",
			wantNewIDCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			newIDCalls := 0
			var handlerRequestID string
			var handlerCorrelationID string
			handler := HTTPRequestContext(HTTPRequestContextConfig{
				NewRequestID: func() string {
					newIDCalls++
					return "tenant-http-generated"
				},
				ValidateRequestID: func(value string) bool {
					return strings.HasPrefix(value, "tenant-")
				},
			})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				handlerRequestID = RequestID(r.Context())
				handlerCorrelationID = CorrelationID(r.Context())
			}))
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
			req.Header.Set(defaultRequestIDHeader, tt.incoming)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			wantCalls := 0
			if tt.wantNewIDCall {
				wantCalls = 1
			}
			if newIDCalls != wantCalls {
				t.Fatalf("NewRequestID calls = %d, want %d", newIDCalls, wantCalls)
			}
			if handlerRequestID != tt.want || handlerCorrelationID != tt.want {
				t.Fatalf(
					"handler request identity diverged: request_id=%q correlation_id=%q want=%q",
					handlerRequestID, handlerCorrelationID, tt.want,
				)
			}
			if got := recorder.Header().Get(defaultRequestIDHeader); got != tt.want {
				t.Fatalf("response request ID header = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPRequestContextResponseHeaderCanBeDisabled(t *testing.T) {
	t.Parallel()

	called := false
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		DisableResponseHeader: true,
		NewRequestID:          func() string { return "generated" },
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		if got := RequestID(r.Context()); got != "generated" {
			t.Fatalf("downstream RequestID = %q, want generated", got)
		}
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil),
	)

	if got := recorder.Header().Get(defaultRequestIDHeader); got != "" {
		t.Fatalf("response header = %q, want empty", got)
	}
	if !called {
		t.Fatal("HTTPRequestContext did not call downstream when the response header was disabled")
	}
}

func TestHTTPRequestContextTraceAndCustomHeaders(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tracestate := "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 251)
	var gotContext context.Context
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		RequestIDHeader:   "X-Correlation-Request",
		ResponseHeader:    "X-Correlation-Response",
		TraceparentHeader: "X-Traceparent",
		TracestateHeader:  "X-Tracestate",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContext = r.Context()
	}))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set("X-Correlation-Request", "custom-http")
	req.Header.Set("X-Traceparent", traceparent)
	req.Header.Set("X-Tracestate", tracestate)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if got := RequestID(gotContext); got != "custom-http" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if trace := Trace(gotContext); !trace.Valid || trace.Tracestate != tracestate {
		t.Fatalf("Trace = %#v", trace)
	}
	if got := recorder.Header().Get("X-Correlation-Response"); got != "custom-http" {
		t.Fatalf("custom response header = %q", got)
	}
}

func TestHTTPRequestContextCombinesTracestateHeaders(t *testing.T) {
	t.Parallel()

	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	var got TraceContext
	handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			got = Trace(r.Context())
		},
	))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set("Traceparent", traceparent)
	req.Header.Add("Tracestate", "vendor=value")
	req.Header.Add("Tracestate", "other=value")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got.Tracestate != "vendor=value,other=value" {
		t.Fatalf("tracestate = %q", got.Tracestate)
	}
}

func TestHTTPRequestContextRejectsInvalidTraceAndOverlongTracestate(t *testing.T) {
	t.Parallel()

	const (
		traceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
		traceparent = "00-" + traceID + "-00f067aa0ba902b7-01"
	)
	tests := []struct {
		name            string
		requestID       string
		traceparent     string
		tracestate      string
		wantCorrelation string
		wantValid       bool
	}{
		{
			name:            "valid trace drops tracestate above maximum length",
			requestID:       "req-overlong-tracestate",
			traceparent:     traceparent,
			tracestate:      strings.Repeat("b", 513),
			wantCorrelation: traceID,
			wantValid:       true,
		},
		{
			name:            "invalid trace discards otherwise valid tracestate",
			requestID:       "req-invalid-trace",
			traceparent:     "not-a-traceparent",
			tracestate:      "vendor=value",
			wantCorrelation: "req-invalid-trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotContext context.Context
			handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
				func(_ http.ResponseWriter, r *http.Request) {
					gotContext = r.Context()
				},
			))
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
			req.Header.Set(defaultRequestIDHeader, tt.requestID)
			req.Header.Set("Traceparent", tt.traceparent)
			req.Header.Set("Tracestate", tt.tracestate)

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got := CorrelationID(gotContext); got != tt.wantCorrelation {
				t.Fatalf("CorrelationID = %q, want %q", got, tt.wantCorrelation)
			}
			trace := Trace(gotContext)
			if trace.Valid != tt.wantValid {
				t.Fatalf("Trace.Valid = %v, want %v; trace=%#v", trace.Valid, tt.wantValid, trace)
			}
			if trace.Tracestate != "" {
				t.Fatalf("Trace.Tracestate = %q, want empty", trace.Tracestate)
			}
			if tt.wantValid && trace.Traceparent != traceparent {
				t.Fatalf("Trace.Traceparent = %q, want %q", trace.Traceparent, traceparent)
			}
			if !tt.wantValid && trace != (TraceContext{}) {
				t.Fatalf("Trace = %#v, want zero value for invalid traceparent", trace)
			}
		})
	}
}

func TestHTTPRequestContextInstallsRequestLogger(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		Logger: logger,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Logger(r.Context()).Info("http handler")
	}))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set(defaultRequestIDHeader, "req-http-logger")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["request_id"]; got != "req-http-logger" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "req-http-logger" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := entry["message"]; got != "http handler" {
		t.Fatalf("message = %v", got)
	}
}

func TestHTTPRequestContextLoggerPresetAddsProviderTraceFields(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		Logger: logger,
		Preset: PresetGCP,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Logger(r.Context()).Info("gcp http handler")
	}))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set(defaultRequestIDHeader, "req-http-gcp")
	req.Header.Set(
		"Traceparent",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	)

	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["severity"]; got != "INFO" {
		t.Fatalf("severity = %v", got)
	}
	if got := entry["request_id"]; got != "req-http-gcp" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := entry["logging.googleapis.com/trace"]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("gcp trace = %v", got)
	}
	if got := entry["logging.googleapis.com/trace_sampled"]; got != true {
		t.Fatalf("gcp trace_sampled = %v", got)
	}
}

func TestHTTPRequestContextReusesExistingMetadata(t *testing.T) {
	t.Parallel()

	existing := &requestMetadata{
		RequestID:     "outer-req",
		CorrelationID: "outer-corr",
	}
	var gotContext context.Context
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		NewRequestID: func() string {
			t.Fatal("NewRequestID was called for existing metadata")
			return ""
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContext = r.Context()
	}))
	req := httptest.NewRequestWithContext(
		contextWithRequestMetadata(context.Background(), existing),
		http.MethodGet,
		"/http",
		nil,
	)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if got := RequestID(gotContext); got != "outer-req" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "outer-corr" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != "outer-req" {
		t.Fatalf("response header = %q", got)
	}
}

func TestHTTPRequestContextPreservesExistingMetadataLogger(t *testing.T) {
	t.Parallel()

	var outerBuffer bytes.Buffer
	outerLogger, err := NewLogger(LoggerConfig{Writer: &outerBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	var innerBuffer bytes.Buffer
	innerLogger, err := NewLogger(LoggerConfig{Writer: &innerBuffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	handler := HTTPRequestContext(HTTPRequestContextConfig{
		Logger: outerLogger.With(zap.String("logger_source", "outer")),
	})(HTTPRequestContext(HTTPRequestContextConfig{
		Logger: innerLogger.With(zap.String("logger_source", "inner")),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Logger(r.Context()).Info("nested http handler")
	})))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set(defaultRequestIDHeader, "req-nested")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeSingleLogLine(t, outerBuffer.String())
	if got := entry["logger_source"]; got != "outer" {
		t.Fatalf("logger_source = %v", got)
	}
	if got := entry["request_id"]; got != "req-nested" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "req-nested" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := strings.TrimSpace(innerBuffer.String()); got != "" {
		t.Fatalf("inner logger was used: %s", got)
	}
}

func TestHTTPRequestContextCompletesLoggerOnlyContext(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			Logger(r.Context()).Info("completed context")
		},
	))
	req := httptest.NewRequestWithContext(
		contextWithRequestLogger(context.Background(), nil, logger),
		http.MethodGet,
		"/http",
		nil,
	)
	req.Header.Set(defaultRequestIDHeader, "req-completed")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["request_id"]; got != "req-completed" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "req-completed" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := entry["message"]; got != "completed context" {
		t.Fatalf("message = %v", got)
	}
}

func TestRequestContextCompletesLoggerOnlyContext(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	req := httptest.NewRequestWithContext(
		contextWithRequestLogger(context.Background(), nil, logger),
		http.MethodGet,
		"/test",
		nil,
	)
	req.Header.Set(defaultRequestIDHeader, "req-huma-completed")
	recorder := httptest.NewRecorder()
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		recorder,
	)

	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("completed huma context")
	})

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["request_id"]; got != "req-huma-completed" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "req-huma-completed" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := entry["message"]; got != "completed huma context" {
		t.Fatalf("message = %v", got)
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != "req-huma-completed" {
		t.Fatalf("response request ID header = %q", got)
	}
}

func TestRequestContextReusesHTTPMetadata(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(
		contextWithRequestMetadata(context.Background(), &requestMetadata{
			RequestID:     "http-req",
			CorrelationID: "http-corr",
		}),
		http.MethodGet,
		"/test",
		nil,
	)
	recorder := httptest.NewRecorder()
	ctx := humatest.NewContext(
		&huma.Operation{Method: http.MethodGet, Path: "/test", DefaultStatus: http.StatusOK},
		req,
		recorder,
	)

	RequestContext(RequestContextConfig{
		NewRequestID: func() string {
			t.Fatal("NewRequestID was called for existing HTTP metadata")
			return ""
		},
	})(ctx, func(next huma.Context) {
		if got := RequestID(next.Context()); got != "http-req" {
			t.Fatalf("RequestID = %q", got)
		}
		if got := CorrelationID(next.Context()); got != "http-corr" {
			t.Fatalf("CorrelationID = %q", got)
		}
	})

	if got := recorder.Header().Get(defaultRequestIDHeader); got != "http-req" {
		t.Fatalf("response header = %q", got)
	}
}

func assertRequestIDSurfaces(
	t *testing.T,
	handlerRequestID string,
	want string,
	recorder *httptest.ResponseRecorder,
) {
	t.Helper()
	if handlerRequestID != want {
		t.Fatalf("handler RequestID = %q, want %q", handlerRequestID, want)
	}
	if got := recorder.Header().Get(defaultRequestIDHeader); got != want {
		t.Fatalf("response request ID header = %q, want %q", got, want)
	}
}

func assertGeneratedRequestID(t *testing.T, requestID string) {
	t.Helper()
	decoded, err := hex.DecodeString(requestID)
	if err != nil {
		t.Fatalf("generated request ID = %q, want lowercase hexadecimal: %v", requestID, err)
	}
	if len(decoded) != 16 || requestID != strings.ToLower(requestID) || !DefaultValidateRequestID(requestID) {
		t.Fatalf("generated request ID = %q, want 16 lowercase hexadecimal bytes", requestID)
	}
	if requestID == strings.Repeat("0", 32) {
		t.Fatal("generated request ID is the all-zero last-resort value")
	}
}

func newHumaTestContext(method, target string, headers map[string]string) (huma.Context, *httptest.ResponseRecorder) {
	return newHumaTestContextWithParent(context.Background(), method, target, headers)
}

func newHumaTestContextWithParent(
	parent context.Context,
	method string,
	target string,
	headers map[string]string,
) (huma.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequestWithContext(parent, method, target, nil)
	req.RemoteAddr = "203.0.113.9:4321"
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	op := &huma.Operation{
		Method:        method,
		Path:          "/test",
		OperationID:   "test-operation",
		DefaultStatus: http.StatusOK,
	}
	return humatest.NewContext(op, req, recorder), recorder
}

type testBody struct {
	OK bool `json:"ok"`
}

type testOutput struct {
	Body testBody
}
