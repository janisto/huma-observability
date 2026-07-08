package applog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability"
)

func TestHelpersUseRequestScopedLoggerAndMetadata(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Writer: &buffer,
		Level:  zapcore.DebugLevel,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	ctx := newWrapperTestContext(map[string]string{
		"X-Request-Id": "req-wrapper",
		"traceparent":  "00-" + traceID + "-00f067aa0ba902b7-01",
	})

	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
	})(ctx, func(ctx huma.Context) {
		obs.AccessLogger(obs.AccessLoggerConfig{
			Logger: logger,
			Now: fixedWrapperClock(
				time.Unix(0, 0),
				time.Unix(0, int64(time.Millisecond)),
			),
		})(ctx, func(ctx huma.Context) {
			reqCtx := ctx.Context()

			Debug(reqCtx, "debug helper", zap.String("debug_field", "yes"))
			Info(reqCtx, "info helper", zap.String("info_field", "yes"))
			Warn(reqCtx, "warn helper", zap.String("warn_field", "yes"))
			Error(reqCtx, "error helper", errors.New("boom"), zap.String("error_field", "yes"))
			Log(reqCtx, zapcore.WarnLevel, "log helper", zap.String("log_field", "yes"))
		})
	})

	logs := decodeWrapperLogLines(t, buffer.String())
	assertWrapperLog(t, logs, "debug helper", "DEBUG", map[string]any{
		"request_id":     "req-wrapper",
		"correlation_id": traceID,
		"trace_id":       traceID,
		"trace_sampled":  true,
		"debug_field":    "yes",
	})
	assertWrapperLog(t, logs, "info helper", "INFO", map[string]any{"info_field": "yes"})
	assertWrapperLog(t, logs, "warn helper", "WARN", map[string]any{"warn_field": "yes"})
	assertWrapperLog(t, logs, "error helper", "ERROR", map[string]any{
		"error":       "boom",
		"error_field": "yes",
	})
	assertWrapperLog(t, logs, "log helper", "WARN", map[string]any{"log_field": "yes"})
}

func TestWithErrorPrependsErrorWithoutMutatingFields(t *testing.T) {
	t.Parallel()

	fields := []zap.Field{zap.String("component", "worker")}
	got := withError(errors.New("boom"), fields)

	if len(got) != 2 {
		t.Fatalf("field count = %d, want 2", len(got))
	}
	if got[0].Key != "error" {
		t.Fatalf("first field key = %q, want error", got[0].Key)
	}
	if got[1].Key != "component" {
		t.Fatalf("second field key = %q, want component", got[1].Key)
	}
	if fields[0].Key != "component" {
		t.Fatalf("input fields were mutated: %#v", fields)
	}

	withoutErr := withError(nil, fields)
	if len(withoutErr) != 1 || withoutErr[0].Key != "component" {
		t.Fatalf("without error = %#v, want original fields", withoutErr)
	}
}

func newWrapperTestContext(headers map[string]string) huma.Context {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wrapper", nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	op := &huma.Operation{
		Method:        http.MethodGet,
		Path:          "/wrapper",
		DefaultStatus: http.StatusOK,
	}
	return humatest.NewContext(op, req, recorder)
}

func fixedWrapperClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

func decodeWrapperLogLines(t *testing.T, output string) []map[string]any {
	t.Helper()

	output = strings.TrimSpace(output)
	if output == "" {
		t.Fatal("expected log output, got empty buffer")
	}

	lines := strings.Split(output, "\n")
	logs := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
		}
		logs = append(logs, entry)
	}
	return logs
}

func assertWrapperLog(t *testing.T, logs []map[string]any, msg, level string, fields map[string]any) {
	t.Helper()

	for _, entry := range logs {
		if entry["message"] != msg {
			continue
		}
		if got := entry["level"]; got != level {
			t.Fatalf("%q level = %v, want %s; entry=%#v", msg, got, level, entry)
		}
		for key, want := range fields {
			if got := entry[key]; got != want {
				t.Fatalf("%q %s = %#v, want %#v; entry=%#v", msg, key, got, want, entry)
			}
		}
		return
	}
	t.Fatalf("log message %q not found in %#v", msg, logs)
}
