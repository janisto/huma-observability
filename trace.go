package obs

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	maxTraceparentLen = 512
	traceparentLen    = 55
)

// TraceContextLevel selects the W3C Trace Context grammar and flag semantics.
type TraceContextLevel uint8

const (
	// TraceContextLevel1 is the default W3C Trace Context Recommendation.
	TraceContextLevel1 TraceContextLevel = 1
	// TraceContextLevel2 enables the pinned W3C Trace Context Level 2 grammar.
	TraceContextLevel2 TraceContextLevel = 2
)

// ResolveTraceContextLevel resolves an omitted level to Level 1 and rejects
// unsupported levels before request processing starts.
func ResolveTraceContextLevel(level TraceContextLevel) (TraceContextLevel, error) {
	switch level {
	case 0, TraceContextLevel1:
		return TraceContextLevel1, nil
	case TraceContextLevel2:
		return TraceContextLevel2, nil
	default:
		return 0, fmt.Errorf(
			"unsupported trace context level %d: supported levels are 1 and 2",
			level,
		)
	}
}

// TraceContext contains parsed W3C trace context for a request.
type TraceContext struct {
	Version     string
	TraceID     string
	ParentID    string
	Flags       string
	Sampled     bool
	Random      bool
	Level       TraceContextLevel
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
	return ParseTraceparentWithLevel(value, TraceContextLevel1)
}

// ParseTraceparentWithLevel parses a W3C traceparent using an explicit level.
func ParseTraceparentWithLevel(value string, level TraceContextLevel) (TraceContext, bool) {
	resolved, err := ResolveTraceContextLevel(level)
	if err != nil {
		return TraceContext{}, false
	}
	if value == "" || len(value) > maxTraceparentLen || len(value) < traceparentLen {
		return TraceContext{}, false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return TraceContext{}, false
		}
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
		Version:     version,
		TraceID:     traceID,
		ParentID:    parentID,
		Flags:       flags,
		Sampled:     flagValue&0x01 == 0x01,
		Random:      resolved == TraceContextLevel2 && version == "00" && flagValue&0x02 == 0x02,
		Level:       resolved,
		Traceparent: value,
		Valid:       true,
	}
	return trace, true
}

func parseTracestate(rawValues []string, level TraceContextLevel) (string, bool) {
	resolved, err := ResolveTraceContextLevel(level)
	if err != nil {
		return "", false
	}
	if len(rawValues) == 0 {
		return "", true
	}
	combined := strings.Join(rawValues, ",")
	if len(combined) > maxTracestateLen {
		return "", false
	}
	members := strings.Split(combined, ",")
	if len(members) > 32 {
		return "", false
	}

	normalized := make([]string, len(members))
	keys := make(map[string]struct{}, len(members))
	for index, rawMember := range members {
		member := strings.Trim(rawMember, " \t")
		if member == "" {
			continue
		}
		key, value, found := strings.Cut(member, "=")
		if !found {
			return "", false
		}
		if !validTracestateKey(key, resolved) || !validTracestateValue(value) {
			return "", false
		}
		if _, duplicate := keys[key]; duplicate {
			return "", false
		}
		keys[key] = struct{}{}
		normalized[index] = key + "=" + value
	}
	return strings.Join(normalized, ","), true
}

func validTracestateKey(key string, level TraceContextLevel) bool {
	if len(key) == 0 || len(key) > 256 {
		return false
	}
	if level == TraceContextLevel2 {
		if !isLowerAlphaOrDigit(key[0]) {
			return false
		}
		for index := 1; index < len(key); index++ {
			if !isTracestateKeyCharacter(key[index], true) {
				return false
			}
		}
		return true
	}

	if strings.Count(key, "@") == 0 {
		if !isLowerAlpha(key[0]) {
			return false
		}
		for index := 1; index < len(key); index++ {
			if !isTracestateKeyCharacter(key[index], false) {
				return false
			}
		}
		return true
	}
	if strings.Count(key, "@") != 1 {
		return false
	}
	tenant, system, _ := strings.Cut(key, "@")
	if len(tenant) == 0 || len(tenant) > 241 ||
		len(system) == 0 || len(system) > 14 ||
		!isLowerAlphaOrDigit(tenant[0]) || !isLowerAlpha(system[0]) {
		return false
	}
	for index := 1; index < len(tenant); index++ {
		if !isTracestateKeyCharacter(tenant[index], false) {
			return false
		}
	}
	for index := 1; index < len(system); index++ {
		if !isTracestateKeyCharacter(system[index], false) {
			return false
		}
	}
	return true
}

func validTracestateValue(value string) bool {
	if len(value) == 0 || len(value) > 256 || value[len(value)-1] == ' ' {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character < 0x20 || character > 0x7e || character == ',' || character == '=' {
			return false
		}
	}
	return true
}

func isLowerAlpha(character byte) bool {
	return character >= 'a' && character <= 'z'
}

func isLowerAlphaOrDigit(character byte) bool {
	return isLowerAlpha(character) || character >= '0' && character <= '9'
}

func isTracestateKeyCharacter(character byte, allowAt bool) bool {
	return isLowerAlphaOrDigit(character) ||
		character == '_' ||
		character == '-' ||
		character == '*' ||
		character == '/' ||
		allowAt && character == '@'
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
