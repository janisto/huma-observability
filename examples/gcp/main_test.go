package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability/v2"
)

func TestGCPHealthRouteEmitsCorrelatedApplicationAndAccessRecords(t *testing.T) {
	t.Parallel()

	response, records := exerciseHealthRoute(t, obs.PresetGCP, zapcore.DebugLevel)
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
	assertLogField(t, access, "duration_ms", float64(12.5))
	assertLogField(t, access, "path_template", "/health")
	assertLogField(t, access, "operation_id", "health_check")
	assertLogField(t, access, "status", float64(http.StatusOK))
	for _, privateField := range []string{"path", "peer_ip", "remote_ip", "user_agent"} {
		if _, ok := access[privateField]; ok {
			t.Fatalf("access record unexpectedly contains privacy field %q: %#v", privateField, access)
		}
	}

	httpRequest, ok := access["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("access httpRequest = %#v, want object", access["httpRequest"])
	}
	assertLogField(t, httpRequest, "requestMethod", http.MethodGet)
	assertLogField(t, httpRequest, "status", float64(http.StatusOK))
	assertLogField(t, httpRequest, "latency", "0.012500s")
	for _, privateField := range []string{"requestUrl", "remoteIp", "userAgent"} {
		if _, ok := httpRequest[privateField]; ok {
			t.Fatalf("httpRequest unexpectedly contains privacy field %q: %#v", privateField, httpRequest)
		}
	}
	if len(httpRequest) != 3 {
		t.Fatalf("httpRequest = %#v, want exact privacy-safe projection", httpRequest)
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

	response, records := exerciseHealthRoute(t, obs.PresetGCP, zapcore.InfoLevel)
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

func TestCoreHealthRouteHasExactPortableProjection(t *testing.T) {
	for _, test := range []struct {
		name         string
		level        zapcore.Level
		wantMessages []string
	}{
		{name: "debug", level: zapcore.DebugLevel, wantMessages: []string{"health check", "dependency check", "request completed"}},
		{name: "info", level: zapcore.InfoLevel, wantMessages: []string{"health check", "request completed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, records := exerciseHealthRoute(t, obs.PresetDefault, test.level)
			assertHealthResponse(t, response)
			if got := len(records); got != len(test.wantMessages) {
				t.Fatalf("log record count = %d, want %d; records=%#v", got, len(test.wantMessages), records)
			}
			for index, message := range test.wantMessages {
				assertLogField(t, records[index], "message", message)
				assertLogField(t, records[index], "request_id", "health-example")
				assertLogField(t, records[index], "correlation_id", "health-example")
				wantLevel := "INFO"
				if message == "dependency check" {
					wantLevel = "DEBUG"
				}
				assertLogField(t, records[index], "level", wantLevel)
				if _, ok := records[index]["severity"]; ok {
					t.Fatalf("core record contains GCP severity: %#v", records[index])
				}
			}
			assertLogField(t, records[0], "service_name", "example-service")
			assertLogField(t, records[0], "service_version", "1.0.0")
			assertLogField(t, records[0], "health_status", "ok")
			access := records[len(records)-1]
			assertLogField(t, access, "method", http.MethodGet)
			assertLogField(t, access, "duration_ms", float64(12.5))
			assertLogField(t, access, "path_template", "/health")
			assertLogField(t, access, "operation_id", "health_check")
			assertLogField(t, access, "status", float64(http.StatusOK))
			if _, ok := access["httpRequest"]; ok {
				t.Fatalf("core access record contains GCP httpRequest: %#v", access)
			}
			for _, privateField := range []string{"path", "peer_ip", "remote_ip", "user_agent"} {
				if _, ok := access[privateField]; ok {
					t.Fatalf("core access record contains privacy field %q: %#v", privateField, access)
				}
			}
		})
	}
}

func exerciseHealthRoute(
	t *testing.T,
	preset obs.Preset,
	level zapcore.Level,
) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()

	var output bytes.Buffer
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: preset,
		Level:  level,
		Writer: &output,
	})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}

	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-Id", "health-example")
	response := httptest.NewRecorder()
	newHandler(logger, preset, fixedGCPHealthClock()).ServeHTTP(response, request)

	return response, decodeLogRecords(t, output.String())
}

func fixedGCPHealthClock() func() time.Time {
	values := []time.Time{
		time.Unix(1, 0),
		time.Unix(1, int64(12_500*time.Microsecond)),
	}
	index := 0
	return func() time.Time {
		value := values[index]
		index++
		return value
	}
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

	if output == "" || !strings.HasSuffix(output, "\n") {
		t.Fatalf("stdout is not non-empty LF-terminated NDJSON: %q", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" || strings.HasSuffix(line, "\r") {
			t.Fatalf("stdout contains an empty or CRLF NDJSON line: %q", output)
		}
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
