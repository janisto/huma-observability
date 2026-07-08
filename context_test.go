package obs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		{name: "empty", value: "", want: false},
		{name: "overlong", value: strings.Repeat("a", 129), want: false},
		{name: "space", value: "abc def", want: false},
		{name: "slash", value: "abc/def", want: false},
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

func TestRequestContextResponseHeaderCanBeDisabled(t *testing.T) {
	t.Parallel()

	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", nil)
	RequestContext(RequestContextConfig{
		DisableResponseHeader: true,
		NewRequestID:          func() string { return "generated" },
	})(ctx, func(huma.Context) {})

	if got := recorder.Header().Get(defaultRequestIDHeader); got != "" {
		t.Fatalf("response request ID header = %q, want empty", got)
	}
}

func TestRequestContextInstallsRequestLogger(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	ctx, _ := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		defaultRequestIDHeader: "req-huma-logger",
	})

	RequestContext(RequestContextConfig{
		Logger: logger,
	})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("huma handler")
	})

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["request_id"]; got != "req-huma-logger" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["correlation_id"]; got != "req-huma-logger" {
		t.Fatalf("correlation_id = %v", got)
	}
	if got := entry["message"]; got != "huma handler" {
		t.Fatalf("message = %v", got)
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
		Logger:        existingLogger.With(zap.String("logger_source", "existing")),
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
		Logger: configLogger.With(zap.String("logger_source", "config")),
	})(ctx, func(next huma.Context) {
		Logger(next.Context()).Info("preserved logger")
	})

	entry := decodeSingleLogLine(t, existingBuffer.String())
	if got := entry["logger_source"]; got != "existing" {
		t.Fatalf("logger_source = %v", got)
	}
	if got := strings.TrimSpace(configBuffer.String()); got != "" {
		t.Fatalf("config logger was used: %s", got)
	}
	if metadata.Logger == nil {
		t.Fatal("existing metadata logger was cleared")
	}
}

func TestRequestContextLoggerInstallDoesNotMutateExistingMetadata(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop()
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
		if got := Logger(next.Context()); got == nil {
			t.Fatal("Logger returned nil")
		}
	})

	if metadata.Logger != nil {
		t.Fatal("RequestContext mutated input metadata")
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

func TestRequestContextTracestateLengthBoundary(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name       string
		tracestate string
		want       string
	}{
		{
			name:       "max length tracestate kept",
			tracestate: strings.Repeat("a", maxTracestateLen),
			want:       strings.Repeat("a", maxTracestateLen),
		},
		{
			name:       "over max length tracestate dropped",
			tracestate: strings.Repeat("b", maxTracestateLen+1),
			want:       "",
		},
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
	})

	var gotContext context.Context
	RequestContext(RequestContextConfig{})(ctx, func(next huma.Context) {
		gotContext = next.Context()
	})

	if got := CorrelationID(gotContext); got != "req-1" {
		t.Fatalf("CorrelationID = %q, want request ID", got)
	}
	if trace := Trace(gotContext); trace.Valid {
		t.Fatalf("Trace.Valid = true for invalid input: %#v", trace)
	}
}

func TestRequestContextCustomHeaders(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	ctx, recorder := newHumaTestContext(http.MethodGet, "/test", map[string]string{
		"X-Correlation-Request": "custom-id",
		"X-Traceparent":         traceparent,
	})

	var gotContext context.Context
	RequestContext(RequestContextConfig{
		RequestIDHeader:   "X-Correlation-Request",
		TraceparentHeader: "X-Traceparent",
		ResponseHeader:    "X-Correlation-Request",
	})(ctx, func(next huma.Context) {
		gotContext = next.Context()
	})

	if got := RequestID(gotContext); got != "custom-id" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if got := recorder.Header().Get("X-Correlation-Request"); got != "custom-id" {
		t.Fatalf("custom response header = %q", got)
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
	if got := CorrelationID(context.Background()); got != "" {
		t.Fatalf("CorrelationID(background) = %q", got)
	}
	if trace := Trace(context.Background()); trace.Valid {
		t.Fatalf("Trace(background).Valid = true")
	}
	if logger := Logger(context.Background()); logger == nil {
		t.Fatal("Logger(background) returned nil")
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

func TestHTTPRequestContextResponseHeaderCanBeDisabled(t *testing.T) {
	t.Parallel()

	handler := HTTPRequestContext(HTTPRequestContextConfig{
		DisableResponseHeader: true,
		NewRequestID:          func() string { return "generated" },
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil),
	)

	if got := recorder.Header().Get(defaultRequestIDHeader); got != "" {
		t.Fatalf("response header = %q, want empty", got)
	}
}

func TestHTTPRequestContextTraceAndCustomHeaders(t *testing.T) {
	t.Parallel()

	traceparent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
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
	req.Header.Set("X-Tracestate", strings.Repeat("a", maxTracestateLen))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if got := RequestID(gotContext); got != "custom-http" {
		t.Fatalf("RequestID = %q", got)
	}
	if got := CorrelationID(gotContext); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if trace := Trace(gotContext); !trace.Valid || trace.Tracestate != strings.Repeat("a", maxTracestateLen) {
		t.Fatalf("Trace = %#v", trace)
	}
	if got := recorder.Header().Get("X-Correlation-Response"); got != "custom-http" {
		t.Fatalf("custom response header = %q", got)
	}
}

func TestHTTPRequestContextDropsOverlongTracestateAndInvalidTrace(t *testing.T) {
	t.Parallel()

	var gotContext context.Context
	handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotContext = r.Context()
		},
	))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/http", nil)
	req.Header.Set(defaultRequestIDHeader, "req-invalid-trace")
	req.Header.Set("Traceparent", "not-a-traceparent")
	req.Header.Set("Tracestate", strings.Repeat("b", maxTracestateLen+1))

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := CorrelationID(gotContext); got != "req-invalid-trace" {
		t.Fatalf("CorrelationID = %q", got)
	}
	if trace := Trace(gotContext); trace.Valid || trace.Tracestate != "" {
		t.Fatalf("Trace = %#v", trace)
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
	req.Header.Set(defaultTraceparentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

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
	if got := entry["message"]; got != "completed context" {
		t.Fatalf("message = %v", got)
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

func newHumaTestContext(method, target string, headers map[string]string) (huma.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
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
