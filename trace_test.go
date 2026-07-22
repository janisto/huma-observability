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
	longFutureVersion := futureVersion + "-" + strings.Repeat("a", 457)

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
			name:    "future version with printable lower boundary",
			value:   futureVersion + "- ",
			sampled: true,
		},
		{
			name:    "future version with printable upper boundary",
			value:   futureVersion + "-~",
			sampled: true,
		},
		{
			name:    "future version beyond former 512 byte boundary",
			value:   longFutureVersion,
			sampled: true,
		},
		{
			name:    "future version opaque obs text",
			value:   futureVersion + "-opaque-ümlaut",
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
			if trace.Level != TraceContextLevel1 || trace.Random {
				t.Fatalf("default parse level/random = %d/%v", trace.Level, trace.Random)
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
		{name: "future version control", value: futureVersion + "-opaque\x1f"},
		{name: "future version delete", value: futureVersion + "-opaque\x7f"},
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

func TestTraceContextLevelResolutionAndRandomFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		requested TraceContextLevel
		want      TraceContextLevel
		wantErr   string
	}{
		{name: "default", want: TraceContextLevel1},
		{name: "level 1", requested: TraceContextLevel1, want: TraceContextLevel1},
		{name: "level 2", requested: TraceContextLevel2, want: TraceContextLevel2},
		{
			name:      "unsupported",
			requested: 3,
			wantErr:   "unsupported trace context level 3: supported levels are 1 and 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveTraceContextLevel(tt.requested)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || got != 0 {
					t.Fatalf(
						"ResolveTraceContextLevel(%d) = (%d, %v), want (0, %q)",
						tt.requested,
						got,
						err,
						tt.wantErr,
					)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf(
					"ResolveTraceContextLevel(%d) = (%d, %v), want (%d, nil)",
					tt.requested,
					got,
					err,
					tt.want,
				)
			}
		})
	}

	const prefix = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-"
	flags := []struct {
		value   string
		sampled bool
		random  bool
	}{
		{value: "00"},
		{value: "01", sampled: true},
		{value: "02", random: true},
		{value: "03", sampled: true, random: true},
		{value: "04"},
	}
	for _, tt := range flags {
		trace, ok := ParseTraceparentWithLevel(prefix+tt.value, TraceContextLevel2)
		if !ok || trace.Level != TraceContextLevel2 ||
			trace.Version != "00" || trace.Flags != tt.value || trace.Sampled != tt.sampled || trace.Random != tt.random {
			t.Fatalf("Level 2 flags %q parsed as %#v", tt.value, trace)
		}
	}
	for _, tt := range []struct {
		flags   string
		sampled bool
	}{
		{flags: "02"},
		{flags: "03", sampled: true},
	} {
		value := "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-" + tt.flags + "-opaque"
		trace, ok := ParseTraceparentWithLevel(value, TraceContextLevel2)
		if !ok || trace.Version != "01" || trace.Level != TraceContextLevel2 ||
			trace.Sampled != tt.sampled || trace.Random {
			t.Fatalf("future Level 2 flags %q parsed as %#v", tt.flags, trace)
		}
	}
	level1, ok := ParseTraceparentWithLevel(prefix+"03", TraceContextLevel1)
	if !ok || level1.Level != TraceContextLevel1 || !level1.Sampled || level1.Random {
		t.Fatalf("Level 1 flags parsed as %#v", level1)
	}
	if _, ok := ParseTraceparentWithLevel(prefix+"03", 3); ok {
		t.Fatal("unsupported trace context level parsed a traceparent")
	}
}

func TestParseTracestateLevel1Matrix(t *testing.T) {
	t.Parallel()
	valid512 := "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 251)
	tests := []struct {
		name      string
		rawValues []string
		want      string
		valid     bool
	}{
		{name: "missing", valid: true},
		{name: "empty field", rawValues: []string{""}, valid: true},
		{
			name:      "split wire order",
			rawValues: []string{"vendor1=value1", "vendor2=value2"},
			want:      "vendor1=value1,vendor2=value2",
			valid:     true,
		},
		{
			name:      "optional whitespace",
			rawValues: []string{"  vendor1=value1  ", "\tvendor2=value2\t"},
			want:      "vendor1=value1,vendor2=value2",
			valid:     true,
		},
		{
			name:      "separator whitespace",
			rawValues: []string{"vendor1=value1 \t, \tother= value2\t"},
			want:      "vendor1=value1,other= value2",
			valid:     true,
		},
		{name: "space before equals", rawValues: []string{"vendor =value"}},
		{name: "empty key", rawValues: []string{"=value"}},
		{name: "tab inside value", rawValues: []string{"vendor=\tvalue"}},
		{name: "long optional whitespace", rawValues: []string{strings.Repeat(" ", 513)}, valid: true},
		{name: "duplicate key", rawValues: []string{"vendor=value1,vendor=value2"}},
		{
			name:      "empty member",
			rawValues: []string{"vendor=value1,,other=value2"},
			want:      "vendor=value1,,other=value2",
			valid:     true,
		},
		{name: "uppercase key", rawValues: []string{"UPPER=value"}},
		{
			name:      "lower alpha start",
			rawValues: []string{"a=value,z=value"},
			want:      "a=value,z=value",
			valid:     true,
		},
		{name: "before lower alpha start", rawValues: []string{"`=value"}},
		{name: "after lower alpha start", rawValues: []string{"{=value"}},
		{
			name:      "maximum simple key",
			rawValues: []string{"a" + strings.Repeat("b", 255) + "=value"},
			want:      "a" + strings.Repeat("b", 255) + "=value",
			valid:     true,
		},
		{name: "overlong simple key", rawValues: []string{"a" + strings.Repeat("b", 256) + "=value"}},
		{
			name:      "multi tenant",
			rawValues: []string{"tenant@system=value"},
			want:      "tenant@system=value",
			valid:     true,
		},
		{
			name:      "maximum tenant",
			rawValues: []string{"1" + strings.Repeat("a", 240) + "@system=value"},
			want:      "1" + strings.Repeat("a", 240) + "@system=value",
			valid:     true,
		},
		{name: "overlong tenant", rawValues: []string{"1" + strings.Repeat("a", 241) + "@system=value"}},
		{
			name:      "maximum system",
			rawValues: []string{"tenant@s" + strings.Repeat("a", 13) + "=value"},
			want:      "tenant@s" + strings.Repeat("a", 13) + "=value",
			valid:     true,
		},
		{name: "overlong system", rawValues: []string{"tenant@s" + strings.Repeat("a", 14) + "=value"}},
		{name: "invalid tenant remainder", rawValues: []string{"a!@system=value"}},
		{name: "invalid system remainder", rawValues: []string{"tenant@s!=value"}},
		{name: "multiple at", rawValues: []string{"tenant@sub@system=value"}},
		{name: "value equals", rawValues: []string{"vendor=value=extra"}},
		{
			name:      "leading space value",
			rawValues: []string{"vendor= value"},
			want:      "vendor= value",
			valid:     true,
		},
		{name: "space value cannot end", rawValues: []string{"vendor= "}},
		{name: "value lower ASCII boundary", rawValues: []string{"vendor=\x1f"}},
		{
			name:      "value upper ASCII boundary",
			rawValues: []string{"vendor=~"},
			want:      "vendor=~",
			valid:     true,
		},
		{name: "value over ASCII boundary", rawValues: []string{"vendor=\x7f"}},
		{
			name:      "32 members",
			rawValues: []string{tracestateMembers(32)},
			want:      tracestateMembers(32),
			valid:     true,
		},
		{name: "33 members", rawValues: []string{tracestateMembers(33)}},
		{name: "512 bytes", rawValues: []string{valid512}, want: valid512, valid: true},
		{name: "513 bytes", rawValues: []string{valid512 + "w"}, want: valid512 + "w", valid: true},
		{name: "empty value", rawValues: []string{"vendor="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseTracestate(tt.rawValues, TraceContextLevel1)
			if got != tt.want || valid != tt.valid {
				t.Fatalf(
					"parseTracestate(%q, Level 1) = (%q, %v), want (%q, %v)",
					tt.rawValues,
					got,
					valid,
					tt.want,
					tt.valid,
				)
			}
		})
	}
}

func TestParseTracestateLevel2Matrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
		valid bool
	}{
		{name: "single character key", value: "1=value", want: "1=value", valid: true},
		{
			name:  "multiple at characters",
			value: "tenant@sub@system=value",
			want:  "tenant@sub@system=value",
			valid: true,
		},
		{name: "at cannot start key", value: "@vendor=value"},
		{name: "uppercase remains invalid", value: "Vendor=value"},
		{
			name:  "separator whitespace",
			value: "vendor=value \t, \t1@two= leading\t",
			want:  "vendor=value,1@two= leading",
			valid: true,
		},
		{name: "duplicate key", value: "vendor=first,vendor=second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseTracestate([]string{tt.value}, TraceContextLevel2)
			if got != tt.want || valid != tt.valid {
				t.Fatalf(
					"parseTracestate(%q, Level 2) = (%q, %v), want (%q, %v)",
					tt.value,
					got,
					valid,
					tt.want,
					tt.valid,
				)
			}
		})
	}
}

func tracestateMembers(count int) string {
	members := make([]string, count)
	for index := range count {
		members[index] = "v" + strconv.Itoa(index) + "=x"
	}
	return strings.Join(members, ",")
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
		if trace.Level != TraceContextLevel1 || trace.Random {
			t.Fatalf("default parse level/random = %d/%v", trace.Level, trace.Random)
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
