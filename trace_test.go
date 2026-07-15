package obs

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

func TestParseTraceparentValid(t *testing.T) {
	t.Parallel()

	futureVersion := "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	maxLengthFutureVersion := futureVersion + "-" + strings.Repeat("a", maxTraceparentLen-len(futureVersion)-1)

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
		{
			name:    "future version at maximum accepted length",
			value:   maxLengthFutureVersion,
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
	futureVersion := "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	overlongFutureVersion := futureVersion + "-" + strings.Repeat("a", maxTraceparentLen-len(futureVersion))
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
		{name: "future version over maximum length", value: overlongFutureVersion},
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
			if trace != (TraceContext{}) {
				t.Fatalf("ParseTraceparent(%q) returned partial data for invalid input: %#v", tt.value, trace)
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
		if trace.Traceparent != value {
			t.Fatalf("Traceparent = %q, want accepted input %q", trace.Traceparent, value)
		}
		assertLowerHexBytes(t, "TraceID", trace.TraceID, 16)
		assertLowerHexBytes(t, "ParentID", trace.ParentID, 8)
		assertLowerHexBytes(t, "Flags", trace.Flags, 1)
		if strings.Trim(trace.TraceID, "0") == "" {
			t.Fatal("accepted trace has all-zero TraceID")
		}
		if strings.Trim(trace.ParentID, "0") == "" {
			t.Fatal("accepted trace has all-zero ParentID")
		}
		flags, err := strconv.ParseUint(trace.Flags, 16, 8)
		if err != nil {
			t.Fatalf("accepted flags %q are not one hexadecimal byte: %v", trace.Flags, err)
		}
		if wantSampled := flags&1 == 1; trace.Sampled != wantSampled {
			t.Fatalf("Sampled = %v, want %v for flags %q", trace.Sampled, wantSampled, trace.Flags)
		}

		version := value[:2]
		assertLowerHexBytes(t, "version", version, 1)
		if version == "ff" {
			t.Fatal("accepted forbidden version ff")
		}
		if version == "00" && len(value) != traceparentLen {
			t.Fatalf("accepted version 00 length = %d, want %d", len(value), traceparentLen)
		}
		if version != "00" && len(value) > traceparentLen && value[traceparentLen] != '-' {
			t.Fatalf("accepted future version without extension separator: %q", value)
		}
	})
}

func assertLowerHexBytes(t *testing.T, name, value string, wantBytes int) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("%s = %q, want lowercase hexadecimal: %v", name, value, err)
	}
	if len(decoded) != wantBytes || value != strings.ToLower(value) {
		t.Fatalf("%s = %q, want %d lowercase hexadecimal bytes", name, value, wantBytes)
	}
}
