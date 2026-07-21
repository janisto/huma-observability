package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/janisto/huma-observability/v2"
)

func TestBasicExamplesDemonstrateDistinctTraceContextLevels(t *testing.T) {
	for _, test := range []struct {
		name       string
		newHandler func(*zap.Logger) http.Handler
		wantRandom bool
	}{
		{name: "Level 1 default", newHandler: newBasicHandler},
		{name: "Level 2", newHandler: newLevel2Handler, wantRandom: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := obs.NewLogger(obs.LoggerConfig{Writer: &output})
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
			request.Header.Set("X-Request-ID", "trace-level-example")
			request.Header.Set(
				"Traceparent",
				"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-03",
			)
			response := httptest.NewRecorder()
			test.newHandler(logger).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("response status = %d, want 200", response.Code)
			}
			records := decodeBasicRecords(t, output.String())
			if len(records) != 2 {
				t.Fatalf("record count = %d, want application and access records: %#v", len(records), records)
			}
			for _, record := range records {
				if got := record["trace_flags"]; got != "03" {
					t.Fatalf("trace_flags = %#v, want 03; record=%#v", got, record)
				}
				gotRandom, present := record["trace_id_random"]
				if test.wantRandom {
					if !present || gotRandom != true {
						t.Fatalf("Level 2 trace_id_random = %#v, present=%v; record=%#v", gotRandom, present, record)
					}
				} else if present {
					t.Fatalf("Level 1 record contains trace_id_random: %#v", record)
				}
			}
		})
	}
}

func decodeBasicRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	if output == "" || !strings.HasSuffix(output, "\n") {
		t.Fatalf("output is not LF-terminated NDJSON: %q", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON record: %v", err)
		}
		records = append(records, record)
	}
	return records
}
