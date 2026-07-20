package obs

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

			timestamp, ok := entry["timestamp"].(string)
			if !ok {
				t.Fatalf("timestamp missing or not string: %#v", entry["timestamp"])
			}
			parsedTimestamp, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				t.Fatalf("timestamp = %q, want RFC3339Nano: %v", timestamp, err)
			}
			_, offset := parsedTimestamp.Zone()
			if offset != 0 || !strings.HasSuffix(timestamp, "Z") {
				t.Fatalf("timestamp = %q, want UTC with Z suffix", timestamp)
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
			} else if _, ok := entry["severity"]; ok {
				t.Fatalf("%s log unexpectedly included GCP severity key: %#v", tt.name, entry)
			}
		})
	}
}

func TestNewLoggerWritesLFTerminatedNDJSONRecords(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("first\nlogical message")
	logger.Error("second message")

	output := buffer.String()
	if !strings.HasSuffix(output, "\n") || strings.Contains(output, "\r") {
		t.Fatalf("output is not LF-terminated NDJSON: %q", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("physical line count = %d, want 2; output=%q", len(lines), output)
	}
	wantMessages := []string{"first\nlogical message", "second message"}
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not one JSON object: %v; line=%q", index, err, line)
		}
		if got := record["message"]; got != wantMessages[index] {
			t.Fatalf("line %d message = %#v, want %q", index, got, wantMessages[index])
		}
	}
}

func TestNewLoggerGCPLevelMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level zapcore.Level
		want  string
	}{
		{name: "debug", level: zapcore.DebugLevel, want: "DEBUG"},
		{name: "info", level: zapcore.InfoLevel, want: "INFO"},
		{name: "warn", level: zapcore.WarnLevel, want: "WARNING"},
		{name: "error", level: zapcore.ErrorLevel, want: "ERROR"},
		{name: "dpanic", level: zapcore.DPanicLevel, want: "CRITICAL"},
		{name: "below debug", level: zapcore.Level(-99), want: "DEBUG"},
		{name: "unknown high level", level: zapcore.Level(99), want: "CRITICAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{
				Preset: PresetGCP,
				Writer: &buffer,
				Level:  zapcore.Level(-99),
			})
			if err != nil {
				t.Fatalf("NewLogger returned error: %v", err)
			}
			logger.Log(tt.level, "level")

			entry := decodeSingleLogLine(t, buffer.String())
			if got := entry["severity"]; got != tt.want {
				t.Fatalf("severity = %v, want %s", got, tt.want)
			}
			if _, ok := entry["level"]; ok {
				t.Fatalf("GCP log unexpectedly included generic level key: %#v", entry)
			}
		})
	}
}

func TestGCPLevelEncoderMapsTerminalLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level zapcore.Level
		want  string
	}{
		{name: "panic", level: zapcore.PanicLevel, want: "CRITICAL"},
		{name: "fatal", level: zapcore.FatalLevel, want: "CRITICAL"},
		{name: "below debug", level: zapcore.Level(-2), want: "DEBUG"},
		{name: "debug boundary", level: zapcore.DebugLevel, want: "DEBUG"},
		{name: "info boundary", level: zapcore.InfoLevel, want: "INFO"},
		{name: "warn boundary", level: zapcore.WarnLevel, want: "WARNING"},
		{name: "error boundary", level: zapcore.ErrorLevel, want: "ERROR"},
		{name: "critical boundary", level: zapcore.DPanicLevel, want: "CRITICAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
				LevelKey:    "severity",
				MessageKey:  "message",
				LineEnding:  zapcore.DefaultLineEnding,
				EncodeLevel: gcpLevelEncoder,
			})
			encoded, err := encoder.EncodeEntry(zapcore.Entry{
				Level:   tt.level,
				Message: "terminal level",
			}, nil)
			if err != nil {
				t.Fatalf("EncodeEntry returned error: %v", err)
			}
			output := encoded.String()
			encoded.Free()

			entry := decodeSingleLogLine(t, output)
			if got := entry["severity"]; got != tt.want {
				t.Fatalf("severity = %v, want %s", got, tt.want)
			}
			if got := entry["message"]; got != "terminal level" {
				t.Fatalf("message = %v, want terminal level", got)
			}
		})
	}
}

func TestNewLoggerReportsSinkFailuresToConfiguredErrorWriter(t *testing.T) {
	t.Parallel()

	sinkErr := errors.New("sink unavailable")
	var errorOutput bytes.Buffer
	logger, err := NewLogger(LoggerConfig{
		Writer:      failingLogWriter{err: sinkErr},
		ErrorWriter: &errorOutput,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	logger.Info("cannot persist")

	got := errorOutput.String()
	if !strings.Contains(got, "write error") || !strings.Contains(got, sinkErr.Error()) {
		t.Fatalf("error output = %q, want Zap write failure containing %q", got, sinkErr)
	}
}

func TestNewLoggerLocksErrorWriterForConcurrentSinkFailures(t *testing.T) {
	t.Parallel()

	sinkErr := errors.New("sink unavailable")
	var errorOutput bytes.Buffer
	logger, err := NewLogger(LoggerConfig{
		Writer:      failingLogWriter{err: sinkErr},
		ErrorWriter: &errorOutput,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	const goroutines = 8
	const writesPerGoroutine = 25
	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for range goroutines {
		go func() {
			defer waitGroup.Done()
			for range writesPerGoroutine {
				logger.Info("cannot persist")
			}
		}()
	}
	waitGroup.Wait()

	lines := strings.Split(strings.TrimSpace(errorOutput.String()), "\n")
	if got, want := len(lines), goroutines*writesPerGoroutine; got != want {
		t.Fatalf("error line count = %d, want %d", got, want)
	}
	for index, line := range lines {
		if !strings.Contains(line, "write error") || !strings.Contains(line, sinkErr.Error()) {
			t.Fatalf("error line %d = %q, want Zap write failure containing %q", index, line, sinkErr)
		}
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

func TestNewLoggerAddCallerEmitsCallSite(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer, AddCaller: true})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	logger.Info("caller-enabled")

	entry := decodeSingleLogLine(t, buffer.String())
	caller, ok := entry["caller"].(string)
	if !ok || !strings.Contains(caller, "logger_test.go:") {
		t.Fatalf("caller = %#v, want logger_test.go call site", entry["caller"])
	}
}

func TestNewLoggerDevelopmentMakesDPanicObservable(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer, Development: true})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		logger.DPanic("development invariant failed")
	}()

	if recovered == nil {
		t.Fatal("Development logger did not panic on DPanic")
	}
	entry := decodeSingleLogLine(t, buffer.String())
	if got := entry["level"]; got != "DPANIC" {
		t.Fatalf("level = %v, want DPANIC", got)
	}
	if got := entry["message"]; got != "development invariant failed" {
		t.Fatalf("message = %v", got)
	}
}

func TestNewLoggerRejectsUnknownPreset(t *testing.T) {
	t.Parallel()

	logger, err := NewLogger(LoggerConfig{Preset: Preset("bogus")})
	if err == nil {
		t.Fatal("NewLogger accepted an unknown preset")
	}
	if logger != nil {
		t.Fatalf("NewLogger returned partial logger for unknown preset: %#v", logger)
	}
	if got, want := err.Error(), "observability: unknown logger preset"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestUnknownPresetIsRejectedAtEveryPublicConstructionBoundary(t *testing.T) {
	t.Parallel()

	const want = "observability: unknown logger preset"
	if resolved, err := ResolveGCPProfileVersion(
		Preset("bogus"),
		"",
	); resolved != "" || err == nil ||
		err.Error() != want {
		t.Fatalf("ResolveGCPProfileVersion(bogus) = (%q, %v), want empty and %q", resolved, err, want)
	}

	tests := []struct {
		name      string
		construct func()
	}{
		{name: "access logger", construct: func() { AccessLogger(AccessLoggerConfig{Preset: Preset("bogus")}) }},
		{name: "request context", construct: func() { RequestContext(RequestContextConfig{Preset: Preset("bogus")}) }},
		{
			name: "HTTP request context",
			construct: func() {
				HTTPRequestContext(HTTPRequestContextConfig{Preset: Preset("bogus")})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				recovered := recover()
				err, ok := recovered.(error)
				if !ok || err.Error() != want {
					t.Fatalf("%s panic = %#v, want %q", tt.name, recovered, want)
				}
			}()
			tt.construct()
		})
	}
}

func TestGCPProfileVersionResolutionAndLoggerValidation(t *testing.T) {
	t.Parallel()

	latest, err := ResolveGCPProfileVersion(PresetGCP, "")
	if err != nil {
		t.Fatalf("ResolveGCPProfileVersion latest returned error: %v", err)
	}
	if latest != GCPProfileVersionV0_1_0 {
		t.Fatalf("latest GCP profile = %q, want %q", latest, GCPProfileVersionV0_1_0)
	}
	pinned, err := ResolveGCPProfileVersion(PresetGCP, GCPProfileVersionV0_1_0)
	if err != nil {
		t.Fatalf("ResolveGCPProfileVersion pin returned error: %v", err)
	}
	if pinned != GCPProfileVersionV0_1_0 {
		t.Fatalf("pinned GCP profile = %q, want %q", pinned, GCPProfileVersionV0_1_0)
	}

	tests := []struct {
		name   string
		config LoggerConfig
		want   string
	}{
		{
			name:   "unsupported version",
			config: LoggerConfig{Preset: PresetGCP, GCPProfileVersion: "0.2.0"},
			want:   `observability: unsupported GCP profile version "0.2.0"`,
		},
		{
			name:   "cross-preset version",
			config: LoggerConfig{Preset: PresetAWS, GCPProfileVersion: GCPProfileVersionV0_1_0},
			want:   "observability: GCP profile version requires GCP preset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, err := NewLogger(tt.config)
			if err == nil {
				t.Fatal("NewLogger accepted invalid GCP profile selection")
			}
			if logger != nil {
				t.Fatalf("NewLogger returned partial logger: %#v", logger)
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
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
	seen := make(map[[2]int]struct{}, goroutines*writesPerGoroutine)
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("concurrent log line is invalid JSON: %v\n%s", err, line)
		}
		if got := entry["message"]; got != "concurrent log" {
			t.Fatalf("concurrent log message = %v, want concurrent log; entry=%#v", got, entry)
		}
		workerValue, workerOK := entry["worker"].(float64)
		writeValue, writeOK := entry["write"].(float64)
		worker := int(workerValue)
		write := int(writeValue)
		if !workerOK || !writeOK || workerValue != float64(worker) || writeValue != float64(write) ||
			worker < 0 || worker >= goroutines || write < 0 || write >= writesPerGoroutine {
			t.Fatalf(
				"invalid concurrent log identity worker=%#v write=%#v; entry=%#v",
				entry["worker"], entry["write"], entry,
			)
		}
		identity := [2]int{worker, write}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("duplicate concurrent log identity worker=%d write=%d", worker, write)
		}
		seen[identity] = struct{}{}
	}
	if got, want := len(seen), goroutines*writesPerGoroutine; got != want {
		t.Fatalf("unique concurrent log identities = %d, want %d", got, want)
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

type failingLogWriter struct {
	err error
}

func (w failingLogWriter) Write([]byte) (int, error) {
	return 0, w.err
}
