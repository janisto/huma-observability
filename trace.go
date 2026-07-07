package obs

import "strconv"

const (
	maxTraceparentLen = 512
	traceparentLen    = 55
)

// TraceContext contains the parsed W3C traceparent value for a request.
type TraceContext struct {
	TraceID     string
	ParentID    string
	Flags       string
	Sampled     bool
	Traceparent string
	Tracestate  string
	Valid       bool
}

// ParseTraceparent parses a W3C traceparent header value.
//
// It accepts version 00 exactly as specified and follows W3C forward
// compatibility rules for future versions: base fields must be parseable and
// any extension data must follow the flags field after a dash.
func ParseTraceparent(value string) (TraceContext, bool) {
	if value == "" || len(value) > maxTraceparentLen || len(value) < traceparentLen {
		return TraceContext{}, false
	}
	if value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return TraceContext{}, false
	}

	version := value[:2]
	if !isLowerHex(version) || version == "ff" {
		return TraceContext{}, false
	}

	if version == "00" && len(value) != traceparentLen {
		return TraceContext{}, false
	}
	if version != "00" && len(value) > traceparentLen && value[traceparentLen] != '-' {
		return TraceContext{}, false
	}

	traceID := value[3:35]
	parentID := value[36:52]
	flags := value[53:55]
	if !isLowerHex(traceID) || !isLowerHex(parentID) || !isLowerHex(flags) {
		return TraceContext{}, false
	}
	if isAllZero(traceID) || isAllZero(parentID) {
		return TraceContext{}, false
	}

	flagValue, err := strconv.ParseUint(flags, 16, 8)
	if err != nil {
		return TraceContext{}, false
	}

	trace := TraceContext{
		TraceID:     traceID,
		ParentID:    parentID,
		Flags:       flags,
		Sampled:     flagValue&0x01 == 0x01,
		Traceparent: value,
		Valid:       true,
	}
	return trace, true
}

func isLowerHex(value string) bool {
	for i := range len(value) {
		c := value[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func isAllZero(value string) bool {
	for i := range len(value) {
		if value[i] != '0' {
			return false
		}
	}
	return true
}
