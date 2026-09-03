package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
//
//	THE POLICY for a request parameter the server will not use as sent
//	(aihub#255, aihub#267, aihub#340)
//
// ─────────────────────────────────────────────────────────────────────────────
//
// This file is the single place that decides what happens to a bad query
// parameter, and every handler in this package reads its parameters through the
// helpers below. That is enforced, not requested: TestQueryParamsGoThroughThe
// SharedHelpers in queryparam_gate_test.go walks the AST of every non-test file
// here and fails when a request value reaches a conversion, or is compared
// against a non-empty string literal, outside this file.
//
// ⚠️ What "enforced" does and does not mean, because the gate is an AST walk and
// not a type system. It follows a request value from any of echo's readers
// (QueryParam, QueryParams, FormValue, FormParams, and a bare Get) and from this
// package's own wrappers, through `:=` and `var` bindings, into any function body
// including literals — all ten of those shapes verified by injecting each one and
// watching it go red, not by reading the code. What it does NOT follow is a value
// laundered through a NEW helper function of your own, or stored on a struct
// field. Closing those needs type-checked dataflow; the cost is not repaid in a
// package whose whole idiom is read-then-parse, and the limit is restated at the
// gate itself rather than only here.
//
// ─── Why this file exists ───────────────────────────────────────────────────
//
// It did not, and each handler invented its own answer. Measured on origin/main
// 3753044 by running the code (not by reading it), four list endpoints had four
// different behaviours for one over-large page size:
//
//	endpoint                  non-positive     above ceiling   ceiling  disclosed
//	GET /v1/work_items        -> 50 (default)  -> 50 (default)  200     yes
//	GET /v1/memories          -> 20 (default)  -> 200 (ceiling) 200     yes
//	GET /v1/work_items/ready  -> 10 (default)  -> 200 (ceiling) 200     no
//	GET /v1/events            -> 50 (default)  honoured in full none    n/a
//
// and malformed input was worse than inconsistent, it was invisible:
// `?similarity_threshold=notanumber` left the field at 0, which is exactly the
// value that means "filter off", so the caller got an unfiltered page believing
// it was filtered. `?limit=12abc` was read by fmt.Sscanf as 12 — a page size the
// caller never wrote, silently substituted for one they did.
//
// ─── The two rules ──────────────────────────────────────────────────────────
//
// The distinction that matters is not "is the value good?" but "whose mistake is
// it?", because that decides who can fix it.
//
//	RULE 1 — MALFORMED means the server cannot interpret the value at all: a
//	         number that does not parse, a token outside a closed vocabulary.
//	         That is a CALLER bug, and only the caller can fix it, so it gets a
//	         400 naming the parameter, the offending value, and what is legal.
//
//	         Never substitute a default. A default is byte-identical to "the
//	         caller did not send this parameter", so substituting one converts
//	         the caller's mistake into the server's silence and the caller never
//	         learns. Precedent, all of it in this direction: the boolean and
//	         sort/order readers on GET /v1/work_items (aihub#224/#280) already
//	         400 — parseListWIBool, the first of them, is now queryBool below —
//	         as does parseRecallTypes on a piped `type` (aihub#289), and
//	         aihub#279 chose reject over default for include_repos.
//
//	RULE 2 — OVER A SERVER LIMIT means the value is perfectly meaningful and the
//	         server will not honour it in full: a page size above the ceiling.
//	         That is not a caller bug, it is a RESOURCE LIMIT, and there is no
//	         other value the caller could have sent that would have got them what
//	         they asked for. So it is clamped to the nearest legal bound and
//	         REPORTED in `request_adjusted` (aihub#314) rather than rejected.
//
//	         Clamp to the CEILING, never back to the default. A default below the
//	         ceiling means asking for a bigger page returns a smaller one, which
//	         is the inversion aihub#309 shipped and measured (top_k=30 -> 10 items
//	         while top_k unset -> 20). normalizeRecallTopK and GetReadyQueue
//	         already clamp to the ceiling; ListWorkItems was the outlier.
//
//	         A NON-POSITIVE page size is neither malformed nor over a limit: it
//	         means "the caller named no page size" and yields the endpoint
//	         default. That is the aihub#249 contract, kept verbatim.
//
// An ABSENT or EMPTY parameter is a third thing again — "the caller did not ask"
// — and takes the default silently, because there is nothing to report.
//
// ─── Which rule applies is decided by WHOSE MISTAKE IT IS ───────────────────
//
// Not by whether the value sits inside some range. The question is whether the
// caller could have sent something else and got what they wanted:
//
//	similarity_threshold=5   Rule 1. Cosine similarity cannot exceed 1, so this
//	                         asks for a filter that can never match anything. The
//	                         caller meant 0.5 and can send 0.5. -> 400.
//
//	limit=5000               Rule 2. 5000 is a coherent request; the server just
//	                         will not serve a page that big. No other value gets
//	                         the caller 5000 items in one response. -> 200 + told.
//
// Getting this backwards in either direction is expensive. Rejecting a Rule-2
// value breaks callers over a limit they cannot do anything about; clamping a
// Rule-1 value invents a request the caller never made, which is how
// `limit=12abc` became a page of 12.
//
// ─── The one thing these rules do NOT cover, stated so nobody reads it in ───
//
// Both rules act on a value that ARRIVED. Neither says anything about a
// parameter that never reaches this layer — a field dropped by an MCP forwarding
// loop (aihub#148) or by a type mismatch. request_adjusted.go's closing section
// makes the same boundary for the same reason: a caller must not read "no
// complaint" as "my parameter was honoured".

// ─── Rule 1: parse-or-reject ────────────────────────────────────────────────

// queryInt reads an integer query param.
//
// Returns present=false for an absent or all-whitespace value, so the caller can
// tell "not sent" from "sent 0" — the zero-value/absent ambiguity that
// appendIntAdjustment flags as its remaining blind spot.
//
// strconv.Atoi, not fmt.Sscanf. Sscanf("12abc", "%d", &n) succeeds with n=12 and
// a caller-supplied page size of "12abc" became a page of 12 that nobody asked
// for; Atoi rejects the whole token, which is the only reading that cannot
// invent a request.
func queryInt(c echo.Context, name string) (value int, present bool, aerr *domain.AihubError) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s must be an integer, got %q", name, raw))
	}
	return n, true, nil
}

// queryFloat reads a floating-point query param, rejecting anything Rule 1 calls
// malformed. NaN and ±Inf parse fine in Go and compare false against every
// bound, so they are refused here rather than allowed to disable a filter from
// the inside.
func queryFloat(c echo.Context, name string) (value float64, present bool, aerr *domain.AihubError) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s must be a finite number, got %q", name, raw))
	}
	return f, true, nil
}

// queryFloatInRange is queryFloat with the parameter's own closed domain
// applied: a value outside [min,max] is Rule 1 malformed, because the range is a
// property of what the parameter MEANS and not a limit the server imposes to
// protect itself. Pass math.Inf for an open end.
func queryFloatInRange(c echo.Context, name string, minValue, maxValue float64) (value float64, present bool, aerr *domain.AihubError) {
	f, present, aerr := queryFloat(c, name)
	if aerr != nil || !present {
		return 0, present, aerr
	}
	if f < minValue || f > maxValue {
		return 0, false, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s must be %s, got %s", name, describeRange(minValue, maxValue), num(f)))
	}
	return f, true, nil
}

// describeRange renders a range for an error message. A half-open range is
// spelled as a single bound ("at least 0") rather than as "between 0 and
// unbounded", because the message is the entire fix from the caller's side and
// naming a bound that does not exist sends them looking for one.
func describeRange(minValue, maxValue float64) string {
	minOpen, maxOpen := math.IsInf(minValue, -1), math.IsInf(maxValue, 1)
	switch {
	case minOpen && maxOpen:
		return "a finite number"
	case minOpen:
		return "at most " + num(maxValue)
	case maxOpen:
		return "at least " + num(minValue)
	default:
		return "between " + num(minValue) + " and " + num(maxValue)
	}
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// queryBool reads a boolean query param using strconv.ParseBool's spellings, so
// the forms a caller is likely to try (True, TRUE, T, 1, 0, f) are each either
// honoured or refused — never silently read as false (aihub#280).
func queryBool(c echo.Context, name string) (value bool, present bool, aerr *domain.AihubError) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s must be true or false, got %q", name, raw))
	}
	return b, true, nil
}

// queryRFC3339 reads a timestamp query param.
//
// Rule 1 applies unchanged, and this one was already obeying it before the rule
// had a name (aihub#280 rejected an unparseable `since` loudly). It lives here so
// the claim "no conversion outside this file" is literally true rather than true
// apart from one that happens to be correct — an exception nobody can see is one
// the next reader copies.
func queryRFC3339(c echo.Context, name string) (value time.Time, present bool, aerr *domain.AihubError) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return time.Time{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s must be an RFC3339 timestamp, got %q", name, raw))
	}
	return ts, true, nil
}

// queryCSV splits a comma-separated query param, trimming each entry and
// dropping empties.
//
// Trimming is load-bearing, not tidiness: `ids=wi_a, wi_b` is the form a human
// or an agent writes, and an untrimmed " wi_b" matches no row while looking like
// a working filter (aihub#280).
func queryCSV(c echo.Context, name string) []string {
	return splitCSVParam(c.QueryParam(name))
}

// queryCSVStrict is queryCSV for a list param whose EMPTINESS CHANGES THE QUERY:
// a value that arrives non-empty but contains no usable entry is Rule 1
// malformed, rather than "the caller sent no filter".
//
// 🔴 The distinction is not pedantic, and getting it wrong is how this very
// change nearly shipped a regression. `?types=,` used to reach SQL as
// `event_type IN (”,”)` and match NOTHING; routing it through queryCSV, which
// drops empties, turned it into no filter at all — the caller asked to narrow
// the stream and got ALL of it. Both old and new are silent, and the new one is
// silent in the dangerous direction: an over-broad answer that looks like data.
//
// handleListWorkItems already guards exactly this for `ids` (`ids=,` must not
// become "list every accessible project"); this gives the other list params the
// same guard instead of a comment asking future readers to remember.
func queryCSVStrict(c echo.Context, name string) ([]string, *domain.AihubError) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return nil, nil
	}
	values := splitCSVParam(raw)
	if len(values) == 0 {
		return nil, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
			"%s was sent but names nothing (got %q); omit it to apply no filter", name, raw))
	}
	return values, nil
}

// splitCSVParam is queryCSV's string half, for callers that already hold the raw
// value.
func splitCSVParam(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ─── The /ui exemption, declared here rather than taken quietly ─────────────
//
// The two rules above govern the /v1 API, whose caller is a program that can
// read a 400 and be fixed. /ui is server-rendered HTML whose caller is a browser
// following links this server generated: every one of these params is emitted by
// our own templates from an already-typed Go value, and no /ui form field
// carries any of them (verified against internal/server/templates/*.tmpl —
// `limit` and `strength_min` are hidden inputs holding the server's own parsed
// values; the only free-text field is `q`).
//
// So the only way a malformed value reaches /ui is a hand-edited or stale
// bookmarked URL, and there an error page is a worse answer than the default
// view: it fails a human who cannot read the response body, over a parameter
// they did not knowingly set.
//
// The exemption is therefore about WHO the caller is, not about which file the
// code lives in — and it is bounded two ways: the lenient readers live in this
// file with the strict ones, and the gate test rejects a call to either of them
// from any file not named ui_*.go.

// queryIntLenientUI reads a page size for a /ui page: anything that does not
// parse or is not positive becomes def, and anything above ceiling becomes
// ceiling. See the exemption note above; do NOT call this from a /v1 handler.
//
// Note that the two halves differ from Rule 1/Rule 2 in exactly one way — the
// unparseable case is silent instead of a 400. Above the ceiling it still lands
// on the CEILING and not back on the default, because that half is not about who
// the caller is: a bigger request returning a smaller page is wrong for a human
// reading a table too.
func queryIntLenientUI(c echo.Context, name string, def, ceiling int) int {
	n, present, err := queryInt(c, name)
	if err != nil || !present || n <= 0 {
		return def
	}
	if n > ceiling {
		return ceiling
	}
	return n
}

// queryFloatLenientUI reads a float query param for a /ui page, falling back to
// def for anything that does not parse or falls outside [min,max]. See the
// exemption note above; do NOT call this from a /v1 handler.
func queryFloatLenientUI(c echo.Context, name string, minValue, maxValue, def float64) float64 {
	f, present, err := queryFloatInRange(c, name, minValue, maxValue)
	if err != nil || !present {
		return def
	}
	return f
}

// queryEnumCSV reads a comma-separated query param whose entries must all come
// from a CLOSED vocabulary, rejecting the whole request if any does not.
//
// Two things follow from the allowlist that a cardinality cap would not give
// (aihub#255):
//
//   - The result is bounded BY CONSTRUCTION. Entries are deduplicated, so the
//     returned slice can never be longer than `allowed`, whatever the caller
//     sends. `?status=` carrying 6,000 elements used to reach Postgres as a
//     6,000-element `= ANY($1)`; it now cannot exceed 7. That is a structural
//     invariant rather than a number somebody has to keep in step with the load.
//
//   - An unknown value stops being an empty result set. `?status=notastatus`
//     returned 200 with zero rows, indistinguishable from "no work item is in
//     that state" — the same shape as aihub#289's piped `type`, where an empty
//     page was read as "no prior experience exists".
//
// Order is the caller's, so a caller that cares about it keeps it.
func queryEnumCSV(c echo.Context, name string, allowed []string) ([]string, *domain.AihubError) {
	values := queryCSV(c, name)
	if len(values) == 0 {
		return nil, nil
	}
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(allowed))
	for _, v := range values {
		if !ok[v] {
			return nil, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
				"invalid %s %q: must be one of %s", name, v, strings.Join(allowed, ", ")))
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}
