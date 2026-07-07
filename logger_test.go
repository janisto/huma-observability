package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestNewLoggerWritesPresetJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		preset     Preset
		levelKey   string
		levelValue string
	}{
		{name: "default", preset: PresetDefault, levelKey: "level", levelValue: "INFO"},
		{name: "gcp", preset: PresetGCP, levelKey: "severity", levelValue: "INFO"},
		{name: "aws", preset: PresetAWS, levelKey: "level", levelValue: "INFO"},
		{name: "azure", preset: PresetAzure, levelKey: "level", levelValue: "INFO"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{
				Preset: tt.preset,
				Writer: &buffer,
			})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}

			logger.Info("hello", zap.String("component", "test"))
			entry := decodeSingleLogLine(t, buffer.String())

			if _, ok := entry["timestamp"].(string); !ok {
				t.Fatalf("timestamp missing or not string: %#v", entry["timestamp"])
			}
			if got := entry[tt.levelKey]; got != tt.levelValue {
				t.Fatalf("%s = %v, want %q", tt.levelKey, got, tt.levelValue)
			}
			if got := entry["message"]; got != "hello" {
				t.Fatalf("message = %v", got)
			}
			if got := entry["component"]; got != "test" {
				t.Fatalf("component = %v", got)
			}
			if tt.levelKey == "severity" {
				if _, ok := entry["level"]; ok {
					t.Fatalf("GCP log unexpectedly included level key: %#v", entry)
				}
			}
		})
	}
}

func TestNewLoggerGCPWarnMapsToWarning(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	logger.Warn("warning")
	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["severity"]; got != "WARNING" {
		t.Fatalf("severity = %v, want WARNING", got)
	}
}

func TestNewLoggerWritesNamedLoggerField(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	logger.Named("worker").Info("named log")
	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["logger"]; got != "worker" {
		t.Fatalf("logger = %v, want worker", got)
	}
}

func TestNewLoggerRejectsUnknownPreset(t *testing.T) {
	t.Parallel()

	if _, err := NewLogger(LoggerConfig{Preset: Preset("bogus")}); err == nil {
		t.Fatal("NewLogger accepted an unknown preset")
	}
}

func TestNewLoggerLocksWriterForConcurrentUse(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	const goroutines = 8
	const writesPerGoroutine = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(worker int) {
			defer wg.Done()
			for j := range writesPerGoroutine {
				logger.Info("concurrent log", zap.Int("worker", worker), zap.Int("write", j))
			}
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if got, want := len(lines), goroutines*writesPerGoroutine; got != want {
		t.Fatalf("log line count = %d, want %d", got, want)
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("concurrent log line is invalid JSON: %v\n%s", err, line)
		}
	}
}

func TestLoggerAccessorReturnsRequestLogger(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	base, err := NewLogger(LoggerConfig{
		Writer: &buffer,
		Level:  zapcore.DebugLevel,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	metadata := &requestMetadata{
		RequestID:     "req-1",
		CorrelationID: "trace-1",
		Logger:        base.With(zap.String("request_id", "req-1")),
	}
	ctx := context.WithValue(context.Background(), contextKey{}, metadata)
	Logger(ctx).Debug("handler debug")

	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["request_id"]; got != "req-1" {
		t.Fatalf("request_id = %v", got)
	}
	if got := entry["message"]; got != "handler debug" {
		t.Fatalf("message = %v", got)
	}
}

func TestRequestLoggerFieldsIncludeTraceOnlyWhenValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		trace         TraceContext
		wantTraceKeys bool
	}{
		{name: "without trace", trace: TraceContext{}, wantTraceKeys: false},
		{name: "with trace", trace: TraceContext{
			TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			ParentID: "00f067aa0ba902b7",
			Flags:    "01",
			Sampled:  true,
			Valid:    true,
		}, wantTraceKeys: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			base, err := NewLogger(LoggerConfig{Writer: &buffer})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			metadata := &requestMetadata{
				RequestID:     "req-1",
				CorrelationID: "corr-1",
				Trace:         tt.trace,
			}

			base.With(requestMetadataFields(metadata)...).Info("handler")
			entry := decodeSingleLogLine(t, buffer.String())

			if got := entry["request_id"]; got != "req-1" {
				t.Fatalf("request_id = %v", got)
			}
			if got := entry["correlation_id"]; got != "corr-1" {
				t.Fatalf("correlation_id = %v", got)
			}
			_, hasTraceID := entry["trace_id"]
			if hasTraceID != tt.wantTraceKeys {
				t.Fatalf("trace_id present = %v, want %v; entry=%#v", hasTraceID, tt.wantTraceKeys, entry)
			}
			if tt.wantTraceKeys {
				if got := entry["trace_sampled"]; got != true {
					t.Fatalf("trace_sampled = %v", got)
				}
			}
		})
	}
}

func decodeSingleLogLine(t *testing.T, line string) map[string]any {
	t.Helper()
	line = strings.TrimSpace(line)
	if line == "" {
		t.Fatal("expected one log line, got empty buffer")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
	}
	return entry
}
