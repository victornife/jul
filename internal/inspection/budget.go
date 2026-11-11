// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"fmt"
	"math"
	"strings"

	"jul/internal/config"
)

// Static minimum-budget terms (ADR 0032 §4, §7). They are conservative
// startup bounds for one request on one route, used only to reject a budget
// that could never admit that route's worst case. Runtime admission charges
// actual sizes; these figures never become reservations.
const (
	// WAFStateBaseBytes is the fixed per-transaction term of R1.
	WAFStateBaseBytes uint64 = 64 << 10
	// WAFArgumentBytes is the per-argument term of R1, R2 and R4.
	WAFArgumentBytes uint64 = 56 << 10
	// WAFByteFactor multiplies header, body and response bytes in R1–R4.
	WAFByteFactor uint64 = uint64(config.InspectionResponseHeaderEstimateFactor)
	// RequestHeaderSlackBytes is net/http's allowance above
	// max_header_bytes when reading a request header block.
	RequestHeaderSlackBytes uint64 = 4096
	// DefaultRequestHeaderBytes is the listener default for max_header_bytes.
	DefaultRequestHeaderBytes uint64 = 1 << 20
	// PluginRequestStateBytes is the host-side set_request_state ceiling.
	PluginRequestStateBytes uint64 = 4096
)

// WAFFacts are the compiled, request-independent engine limits of the WAF
// guarding a route. They also bound ctl:requestBodyLimit and
// ctl:responseBodyLimit raises: Coraza sizes each transaction's body buffers
// from the compiled limits and refuses bytes beyond them, so a raise lifts
// only the transaction's limit check (pinned by the waf package's
// TestCtlBodyLimitRaiseCannotExceedCompiledBuffers).
type WAFFacts struct {
	RequestBodyLimit uint64
	// RequestBodyInMemoryLimit is the compiled in-memory limit. Zero means it
	// could not be read, so every body byte is treated as buffered.
	RequestBodyInMemoryLimit uint64
	ResponseBodyLimit        uint64
	ArgumentLimit            uint64
}

// PluginFacts describe one plugin occurrence in a route chain: its configured
// limits and the host capabilities its module imports.
type PluginFacts struct {
	Name             string
	MaxRequestBody   uint64
	MaxResponseBody  uint64
	MaxFetchResponse uint64
	MemoryLimit      uint64
	// ReadsBody: the module imports read_request_body.
	ReadsBody bool
	// SetsRequestState: the module imports set_request_state.
	SetsRequestState bool
	// WritesResponseBody: the module imports write_response_body.
	WritesResponseBody bool
	// Fetch: fetch is enabled and the module imports fetch.
	Fetch bool
	// SubscribesResponse: the plugin is in the route's response point
	// (jul-abi/v2 handle_response on a middleware plugin).
	SubscribesResponse bool
	// ReplacesResponseBody: a response subscriber importing resp_body_replace.
	ReplacesResponseBody bool
}

// RouteFacts is everything that can be inspected on one route.
type RouteFacts struct {
	Route   string
	Plugins []PluginFacts
	WAF     *WAFFacts
	// RequestHeaderBytes is the listener's effective max_header_bytes.
	RequestHeaderBytes uint64
}

// Inspected reports whether the route acquires an inspection lease at all.
func (r RouteFacts) Inspected() bool { return r.WAF != nil || len(r.Plugins) > 0 }

// RouteRequirement is one route's static worst-case single-request charge.
type RouteRequirement struct {
	Route string
	Bytes uint64
	// Terms names the components in descending size, for operator messages.
	Terms []BudgetTerm
}

type BudgetTerm struct {
	Name  string
	Bytes uint64
}

// Requirement is the largest route requirement in a configuration. A zero
// Bytes means no route is inspected, so the budget needs no minimum.
type Requirement struct {
	RouteRequirement
	InspectedRoutes int
}

type termSum struct {
	terms []BudgetTerm
	total uint64
	ok    bool
}

func (s *termSum) add(name string, amount uint64) {
	if !s.ok || amount == 0 {
		return
	}
	if !addTo(&s.total, amount) {
		s.ok = false
		return
	}
	for i := range s.terms {
		if s.terms[i].Name == name {
			s.terms[i].Bytes += amount
			return
		}
	}
	s.terms = append(s.terms, BudgetTerm{Name: name, Bytes: amount})
}

func mulAdd(a, b, c uint64) (uint64, bool) {
	if a != 0 && b > (math.MaxUint64-c)/a {
		return 0, false
	}
	return a*b + c, true
}

func mul(a, b uint64) (uint64, bool) { return mulAdd(a, b, 0) }

func plusOne(n uint64) (uint64, bool) {
	if n == math.MaxUint64 {
		return 0, false
	}
	return n + 1, true
}

// RouteMinimum returns the static worst-case single-request charge for a
// route: the base charge, plugin request/response buffers and host copies,
// and the WAF request/response buffers with cumulative R1 + R2 + R3 + R4
// state. An uninspected route needs nothing.
func RouteMinimum(route RouteFacts, baseBytes, maxResponseHeaderBytes uint64) (RouteRequirement, error) {
	out := RouteRequirement{Route: route.Route}
	if !route.Inspected() {
		return out, nil
	}
	s := termSum{ok: true}
	s.add("base", baseBytes)
	overflow := func(what string) (RouteRequirement, error) {
		return RouteRequirement{}, fmt.Errorf("%s: %s overflows the minimum-budget calculation", route.Route, what)
	}

	// Plugin request bodies: each body-reading layer keeps its prepared
	// body (capacity at most limit+1, reachable through the outer request),
	// and the largest one may hold both arrays during its final growth copy.
	var bodySum, bodyMax uint64
	for _, p := range route.Plugins {
		if p.ReadsBody {
			n, ok := plusOne(p.MaxRequestBody)
			if !ok || !addTo(&bodySum, n) {
				return overflow("plugin " + p.Name + " max_request_body")
			}
			bodyMax = max(bodyMax, n)
		}
	}
	if bodySum > 0 {
		s.add("plugin request bodies", bodySum)
		s.add("plugin request bodies", bodyMax)
	}
	for _, p := range route.Plugins {
		if p.SetsRequestState {
			s.add("plugin request state", PluginRequestStateBytes)
		}
		if p.WritesResponseBody {
			// Old and grown host arrays plus the guest-to-host copy.
			n, ok := mul(3, p.MaxResponseBody)
			if !ok {
				return overflow("plugin " + p.Name + " max_response_body")
			}
			s.add("plugin generated responses", n)
		}
		if p.Fetch {
			n, ok := plusOne(p.MaxFetchResponse)
			if !ok || !addTo(&n, p.MemoryLimit) {
				return overflow("plugin " + p.Name + " fetch")
			}
			s.add("plugin fetch", n)
		}
		if p.SubscribesResponse {
			factor := uint64(2)
			if p.ReplacesResponseBody {
				factor = 3
			}
			n, ok := mul(factor, p.MaxResponseBody)
			if !ok {
				return overflow("plugin " + p.Name + " max_response_body")
			}
			s.add("plugin response buffers", n)
		}
	}

	if w := route.WAF; w != nil {
		inMemory := w.RequestBodyLimit
		if w.RequestBodyInMemoryLimit > 0 && w.RequestBodyInMemoryLimit < inMemory {
			inMemory = w.RequestBodyInMemoryLimit
		}
		reqBuf, ok1 := mul(2, inMemory)
		respBuf, ok2 := mul(2, w.ResponseBodyLimit)
		args, ok3 := mul(WAFArgumentBytes, w.ArgumentLimit)
		headers := route.RequestHeaderBytes
		if headers == 0 {
			headers = DefaultRequestHeaderBytes
		}
		ok4 := addTo(&headers, RequestHeaderSlackBytes)
		r1h, ok5 := mul(WAFByteFactor, headers)
		r2b, ok6 := mul(WAFByteFactor, w.RequestBodyLimit)
		r3, ok7 := mul(WAFByteFactor, maxResponseHeaderBytes)
		r4b, ok8 := mul(WAFByteFactor, w.ResponseBodyLimit)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || !ok8 {
			return overflow("WAF limits")
		}
		s.add("WAF request buffer", reqBuf)
		s.add("WAF R1 (headers, ARGS_GET)", WAFStateBaseBytes)
		s.add("WAF R1 (headers, ARGS_GET)", args)
		s.add("WAF R1 (headers, ARGS_GET)", r1h)
		s.add("WAF R2 (body, ARGS_POST)", args)
		s.add("WAF R2 (body, ARGS_POST)", r2b)
		s.add("WAF R3 (response headers)", r3)
		// Response-body access can be switched on per transaction by
		// ctl:responseBodyAccess, so R4 and the response buffer are counted
		// whenever a WAF guards the route.
		s.add("WAF R4 (response body, ResponseArgs)", args)
		s.add("WAF R4 (response body, ResponseArgs)", r4b)
		s.add("WAF response buffer", respBuf)
	}
	if !s.ok {
		return overflow("route chain")
	}
	sortTerms(s.terms)
	out.Bytes = s.total
	out.Terms = s.terms
	return out, nil
}

func sortTerms(terms []BudgetTerm) {
	for i := 1; i < len(terms); i++ {
		for j := i; j > 0 && terms[j].Bytes > terms[j-1].Bytes; j-- {
			terms[j], terms[j-1] = terms[j-1], terms[j]
		}
	}
}

// MinimumBudget returns the requirement of the most demanding inspected
// route. Ties keep the first route in configuration order.
func MinimumBudget(routes []RouteFacts, baseBytes, maxResponseHeaderBytes uint64) (Requirement, error) {
	var out Requirement
	for _, r := range routes {
		if !r.Inspected() {
			continue
		}
		out.InspectedRoutes++
		req, err := RouteMinimum(r, baseBytes, maxResponseHeaderBytes)
		if err != nil {
			return Requirement{}, err
		}
		if req.Bytes > out.Bytes {
			out.RouteRequirement = req
		}
	}
	return out, nil
}

// Describe formats the dominant terms for configuration errors and warnings.
func (r RouteRequirement) Describe() string {
	parts := make([]string, 0, len(r.Terms))
	for _, t := range r.Terms {
		parts = append(parts, fmt.Sprintf("%s %d B", t.Name, t.Bytes))
	}
	return strings.Join(parts, ", ")
}

// CheckBudget compares a requirement with the configured limits. It returns
// a non-nil error when the budget is too small; in observe mode the caller
// reports it as a warning instead of failing.
func CheckBudget(limits Limits, req Requirement) error {
	if req.Bytes == 0 || limits.MaxBytes >= req.Bytes {
		return nil
	}
	return fmt.Errorf("[global.inspection].max_buffered_bytes is %d B but route %s needs at least %d B for one worst-case request (%s); raise max_buffered_bytes, lower that route's plugin or WAF limits, or use mode = \"observe\" while sizing",
		limits.MaxBytes, req.Route, req.Bytes, req.Describe())
}
