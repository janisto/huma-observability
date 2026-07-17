package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability"
)

func TestGCPHealthRouteEmitsCorrelatedApplicationAndAccessRecords(t *testing.T) {
	t.Parallel()

	response, records := exerciseGCPHealthRoute(t, zapcore.DebugLevel)
	assertHealthResponse(t, response)

	if got := len(records); got != 3 {
		t.Fatalf("log record count = %d, want 3; records=%#v", got, records)
	}

	info := records[0]
	assertLogField(t, info, "severity", "INFO")
	assertLogField(t, info, "message", "health check")
	assertLogField(t, info, "service_name", "example-service")
	assertLogField(t, info, "service_version", "1.0.0")
	assertLogField(t, info, "health_status", "ok")

	debug := records[1]
	assertLogField(t, debug, "severity", "DEBUG")
	assertLogField(t, debug, "message", "dependency check")
	assertLogField(t, debug, "dependency", "database")
	assertLogField(t, debug, "dependency_status", "ok")
	assertLogField(t, debug, "check_duration_ms", float64(3))

	for index, record := range records {
		if got := record["request_id"]; got != "health-example" {
			t.Fatalf("record %d request_id = %#v, want health-example", index, got)
		}
		if got := record["correlation_id"]; got != "health-example" {
			t.Fatalf("record %d correlation_id = %#v, want health-example", index, got)
		}
	}

	access := records[2]
	assertLogField(t, access, "severity", "INFO")
	assertLogField(t, access, "message", "request completed")
	assertLogField(t, access, "method", http.MethodGet)
	assertLogField(t, access, "path", "/health")
	assertLogField(t, access, "path_template", "/health")
	assertLogField(t, access, "operation_id", "get-health")
	assertLogField(t, access, "status", float64(http.StatusOK))

	httpRequest, ok := access["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("access httpRequest = %#v, want object", access["httpRequest"])
	}
	assertLogField(t, httpRequest, "requestMethod", http.MethodGet)
	assertLogField(t, httpRequest, "requestUrl", "http://example.com/health")
	assertLogField(t, httpRequest, "status", float64(http.StatusOK))
	if _, ok := httpRequest["latency"].(string); !ok {
		t.Fatalf("access httpRequest latency = %#v, want string", httpRequest["latency"])
	}

	for _, applicationOnly := range []string{
		"service_name",
		"service_version",
		"health_status",
		"dependency",
		"dependency_status",
		"check_duration_ms",
	} {
		if _, ok := access[applicationOnly]; ok {
			t.Fatalf("access record unexpectedly contains application field %q: %#v", applicationOnly, access)
		}
	}
}

func TestGCPHealthRouteRespectsInfoLevel(t *testing.T) {
	t.Parallel()

	response, records := exerciseGCPHealthRoute(t, zapcore.InfoLevel)
	assertHealthResponse(t, response)

	if got := len(records); got != 2 {
		t.Fatalf("log record count = %d, want 2; records=%#v", got, records)
	}
	assertLogField(t, records[0], "message", "health check")
	assertLogField(t, records[1], "message", "request completed")
	for index, record := range records {
		if got := record["request_id"]; got != "health-example" {
			t.Fatalf("record %d request_id = %#v, want health-example", index, got)
		}
	}

	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	containsDebugMessage := bytes.Contains(serialized, []byte("dependency check"))
	containsDebugField := bytes.Contains(serialized, []byte("check_duration_ms"))
	if containsDebugMessage || containsDebugField {
		t.Fatalf("info-level output contains debug-only data: %s", serialized)
	}
}

func exerciseGCPHealthRoute(t *testing.T, level zapcore.Level) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()

	var output bytes.Buffer
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetGCP,
		Level:  level,
		Writer: &output,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-Id", "health-example")
	response := httptest.NewRecorder()
	newGCPHandler(logger).ServeHTTP(response, request)

	return response, decodeLogRecords(t, output.String())
}

func assertHealthResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()

	if got := response.Code; got != http.StatusOK {
		t.Fatalf("response status = %d, want 200", got)
	}
	if got := response.Header().Get("X-Request-Id"); got != "health-example" {
		t.Fatalf("response request ID = %q, want health-example", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if got := body["ok"]; got != true {
		t.Fatalf("response ok = %#v, want true; body=%#v", got, body)
	}
}

func decodeLogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("stdout line is not valid JSON: %v\n%s", err, line)
		}
		records = append(records, record)
	}
	return records
}

func assertLogField(t *testing.T, record map[string]any, key string, want any) {
	t.Helper()

	if got := record[key]; got != want {
		t.Fatalf("%s = %#v, want %#v; record=%#v", key, got, want, record)
	}
}
