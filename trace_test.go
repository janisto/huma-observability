package obs

import (
	"strings"
	"testing"
)

func TestParseTraceparentValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		sampled bool
	}{
		{
			name:    "sampled version 00",
			value:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			sampled: true,
		},
		{
			name:    "unsampled version 00",
			value:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
			sampled: false,
		},
		{
			name:    "sampled bit survives other flags",
			value:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09",
			sampled: true,
		},
		{
			name:    "future version without extension",
			value:   "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
			sampled: false,
		},
		{
			name:    "future version with extension",
			value:   "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
			sampled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			trace, ok := ParseTraceparent(tt.value)
			if !ok {
				t.Fatalf("ParseTraceparent(%q) rejected a valid value", tt.value)
			}
			if !trace.Valid {
				t.Fatal("trace.Valid = false, want true")
			}
			if trace.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
				t.Fatalf("TraceID = %q", trace.TraceID)
			}
			if trace.ParentID != "00f067aa0ba902b7" {
				t.Fatalf("ParentID = %q", trace.ParentID)
			}
			if trace.Flags != tt.value[53:55] {
				t.Fatalf("Flags = %q", trace.Flags)
			}
			if trace.Sampled != tt.sampled {
				t.Fatalf("Sampled = %v, want %v", trace.Sampled, tt.sampled)
			}
			if trace.Traceparent != tt.value {
				t.Fatalf("Traceparent = %q", trace.Traceparent)
			}
		})
	}
}

func TestParseTraceparentRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "uppercase version", value: "0A-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "uppercase trace id", value: "00-4BF92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "uppercase parent id", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00F067aa0ba902b7-01"},
		{name: "invalid version", value: "0g-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "forbidden version ff", value: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "too short", value: valid[:len(valid)-1]},
		{name: "too long", value: valid + "-" + strings.Repeat("a", maxTraceparentLen)},
		{name: "all-zero trace id", value: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{name: "all-zero parent id", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
		{name: "invalid first separator", value: "00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "invalid second separator", value: "00-4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7-01"},
		{name: "invalid third separator", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7_01"},
		{name: "version 00 extra data", value: valid + "-extra"},
		{
			name:  "future version extension without dash",
			value: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01extra",
		},
		{name: "invalid flags", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			trace, ok := ParseTraceparent(tt.value)
			if ok {
				t.Fatalf("ParseTraceparent(%q) accepted invalid value: %#v", tt.value, trace)
			}
		})
	}
}

func FuzzParseTraceparent(f *testing.F) {
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	f.Add("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00-extra")
	f.Add("")
	f.Add("not-a-traceparent")

	f.Fuzz(func(t *testing.T, value string) {
		trace, ok := ParseTraceparent(value)
		if !ok {
			return
		}
		if !trace.Valid {
			t.Fatal("accepted trace has Valid=false")
		}
		if len(trace.TraceID) != 32 {
			t.Fatalf("TraceID length = %d", len(trace.TraceID))
		}
		if len(trace.ParentID) != 16 {
			t.Fatalf("ParentID length = %d", len(trace.ParentID))
		}
		if len(trace.Flags) != 2 {
			t.Fatalf("Flags length = %d", len(trace.Flags))
		}
	})
}
