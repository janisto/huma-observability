package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
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
