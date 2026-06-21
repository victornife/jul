//go:build grpc

// Package transcode implements gRPC<->REST/JSON transcoding: it maps REST
// requests to gRPC calls (unary and, when enabled, server/client/bidi
// streaming) using google.api.http annotations carried in a service's protobuf
// descriptors, calls the gRPC backend with a dynamically built message, and
// renders the reply as JSON (or NDJSON/SSE for streams). It is compiled only
// with the "grpc" build tag.
package transcode

import (
	"fmt"
	"strings"
)

// pathTemplate is a compiled google.api.http path template. Matching a request
// path against it yields the captured path variables (dotted field path -> the
// matched substring).
//
// The supported grammar is the common subset of the google.api.http template
// language: literal segments, single-segment wildcards ("*" and "{field}"),
// a trailing multi-segment wildcard ("**" or "{field=**}"), variables with a
// nested sub-template ("{field=a/*/b}"), and an optional trailing ":verb".
type pathTemplate struct {
	raw      string
	elems    []tmplElem
	captures []tmplCapture
	verb     string // trailing ":verb" literal, "" when absent
	hasRest  bool   // a trailing "**" element is present
}

type tmplKind int

const (
	tmplLiteral tmplKind = iota // exact single-segment match
	tmplSingle                  // "*" or "{field}" — one segment, any value
	tmplRest                    // "**" — the remaining segments (must be last)
)

type tmplElem struct {
	kind tmplKind
	lit  string // for tmplLiteral
}

// tmplCapture records a variable's proto field path and the element index range
// [start,end) it spans. For a trailing "**" variable, end == len(elems) so the
// capture runs to the end of the path.
type tmplCapture struct {
	field []string
	start int
	end   int
}

// parseTemplate compiles a google.api.http path template.
func parseTemplate(raw string) (*pathTemplate, error) {
	if raw == "" || raw[0] != '/' {
		return nil, fmt.Errorf("path template %q must start with '/'", raw)
	}
	t := &pathTemplate{raw: raw}
	s := raw
	if i := lastVerbColon(s); i >= 0 {
		t.verb = s[i+1:]
		s = s[:i]
	}
	for _, seg := range splitSegments(s[1:]) {
		if err := t.addSegment(seg); err != nil {
			return nil, fmt.Errorf("path template %q: %w", raw, err)
		}
	}
	for i, el := range t.elems {
		if el.kind == tmplRest && i != len(t.elems)-1 {
			return nil, fmt.Errorf("path template %q: '**' must be the last segment", raw)
		}
	}
	if n := len(t.elems); n > 0 && t.elems[n-1].kind == tmplRest {
		t.hasRest = true
	}
	return t, nil
}

func (t *pathTemplate) addSegment(seg string) error {
	switch {
	case seg == "*":
		t.elems = append(t.elems, tmplElem{kind: tmplSingle})
		return nil
	case seg == "**":
		t.elems = append(t.elems, tmplElem{kind: tmplRest})
		return nil
	case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
		return t.addVariable(seg[1 : len(seg)-1])
	case seg == "":
		t.elems = append(t.elems, tmplElem{kind: tmplLiteral, lit: ""})
		return nil
	default:
		if strings.ContainsAny(seg, "{}") {
			return fmt.Errorf("malformed segment %q", seg)
		}
		t.elems = append(t.elems, tmplElem{kind: tmplLiteral, lit: seg})
		return nil
	}
}

// addVariable compiles a "{field}" or "{field=sub-template}" capture.
func (t *pathTemplate) addVariable(inner string) error {
	fieldStr, sub, hasSub := strings.Cut(inner, "=")
	fieldStr = strings.TrimSpace(fieldStr)
	if fieldStr == "" {
		return fmt.Errorf("variable has empty field path")
	}
	field := strings.Split(fieldStr, ".")
	for _, f := range field {
		if f == "" {
			return fmt.Errorf("variable %q has an empty field component", inner)
		}
	}
	start := len(t.elems)
	if !hasSub || sub == "" || sub == "*" {
		t.elems = append(t.elems, tmplElem{kind: tmplSingle})
	} else {
		for _, ss := range splitSegments(sub) {
			switch ss {
			case "*":
				t.elems = append(t.elems, tmplElem{kind: tmplSingle})
			case "**":
				t.elems = append(t.elems, tmplElem{kind: tmplRest})
			case "":
				return fmt.Errorf("variable %q has an empty sub-segment", inner)
			default:
				if strings.ContainsAny(ss, "{}*") {
					return fmt.Errorf("variable %q has a malformed sub-template", inner)
				}
				t.elems = append(t.elems, tmplElem{kind: tmplLiteral, lit: ss})
			}
		}
	}
	t.captures = append(t.captures, tmplCapture{field: field, start: start, end: len(t.elems)})
	return nil
}

// match tests path against the template. On success it returns the captured
// variables keyed by dotted field path.
func (t *pathTemplate) match(path string) (map[string]string, bool) {
	if t.verb != "" {
		suffix := ":" + t.verb
		if !strings.HasSuffix(path, suffix) {
			return nil, false
		}
		path = path[:len(path)-len(suffix)]
	}
	if path == "" || path[0] != '/' {
		return nil, false
	}
	segs := strings.Split(path[1:], "/")

	// Record the segment index at which each element begins. Every element
	// consumes exactly one segment except a trailing "**", which consumes the
	// rest.
	segStart := make([]int, len(t.elems)+1)
	si := 0
	for ei, el := range t.elems {
		segStart[ei] = si
		switch el.kind {
		case tmplLiteral:
			if si >= len(segs) || segs[si] != el.lit {
				return nil, false
			}
			si++
		case tmplSingle:
			if si >= len(segs) || segs[si] == "" {
				return nil, false
			}
			si++
		case tmplRest:
			si = len(segs)
		}
	}
	segStart[len(t.elems)] = si
	if si != len(segs) {
		return nil, false
	}

	vars := make(map[string]string, len(t.captures))
	for _, c := range t.captures {
		vars[strings.Join(c.field, ".")] = strings.Join(segs[segStart[c.start]:segStart[c.end]], "/")
	}
	return vars, true
}

// splitSegments splits a template body on '/' at brace depth zero so that a '/'
// inside a "{field=a/*/b}" sub-template is not treated as a separator.
func splitSegments(s string) []string {
	var segs []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '/':
			if depth == 0 {
				segs = append(segs, s[start:i])
				start = i + 1
			}
		}
	}
	return append(segs, s[start:])
}

// lastVerbColon returns the index of the ':' that introduces a trailing verb, or
// -1 when there is none. The verb is the final ':literal' at brace depth zero,
// after the last path separator.
func lastVerbColon(s string) int {
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case '}':
			depth++
		case '{':
			if depth > 0 {
				depth--
			}
		case '/':
			if depth == 0 {
				return -1
			}
		case ':':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
