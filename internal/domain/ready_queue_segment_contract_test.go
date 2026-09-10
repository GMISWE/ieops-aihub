package domain

// aihub#543 probe wave 1 — the two per-segment claims the pf_get_ready_queue
// card makes about GetReadyQueue's seven queries.
//
// Both are read off the ONE function that issues them, in source order, so a
// segment added or reordered tomorrow moves the census rather than slipping past
// it. That is the shape ready_queue_query_errors_test.go argues for on the same
// function and for the same reason: "a gate enumerating today's seven would pass
// [an eighth] by default".
//
// What each arm holds, stated so a reader does not have to infer it:
//
//	TestReadyQueueMaxPagesThreeOfTheSevenSegments   `max` reaches items[],
//	  needs_human_session[] and unclassified[] as their own LIMIT and reaches no
//	  other segment, and items[] is the FIRST query the function sends.
//	TestEveryReadyQueueSegmentQueryErrorIsA500NamingThatSegment  every one of the
//	  seven answers a send-time failure with 500 INTERNAL_ERROR and a message that
//	  names its own segment, distinctly.
//
// Neither needs a database: what is measured is which arguments the calls carry
// and which error each one returns, and both are properties of this source file.
// aihub#432's own disclosure tests state the reason a domain claim should avoid
// AIHUB_TEST_DB where it can — ci.yml deliberately leaves it unset on the step
// that decides whether a change lands.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestReadyQueue(Max|.*Segment)' -count=1 -v

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// readyQueueSegmentCount is how many segments GetReadyQueue drains. It is the
// same number ready_queue_query_errors_test.go pins for the same function, and
// internal/mcp/ready_queue_section_count_test.go owns it against the struct and
// the published description; here it is the census floor, not a third authority.
const readyQueueSegmentCount = 7

// querySite is one pool.Query inside GetReadyQueue, with what the statement
// right after it returns.
type querySite struct {
	// Index is the position in source order, 0-based.
	Index int
	// Line is where the call sits, for failure text.
	Line int
	// BindIdents are the identifiers passed as bind arguments — everything after
	// the context and the statement itself.
	BindIdents []string
	// ErrCodeIdent is the domain error code the following `if err != nil` branch
	// returns ("ErrInternalError"), empty when it returns something else.
	ErrCodeIdent string
	// ErrMessage is that error's message literal.
	ErrMessage string
}

// TakesPageSize reports whether this call pages with the clamped `max`.
func (s querySite) TakesPageSize() bool {
	for _, id := range s.BindIdents {
		if id == "max" {
			return true
		}
	}
	return false
}

// readyQueueQuerySites is the census: every pool.Query in GetReadyQueue, in
// source order, paired with the error its guard returns.
//
// 🔴 The pairing is positional — the guard is the statement DIRECTLY after the
// call — which is the same rule scanQueryErrorHandling enforces one file over,
// and it is deliberate rather than convenient: a checker that hunted for the
// nearest NewErr anywhere below would happily pair a segment with the error of
// the segment after it, and a message naming the wrong segment is the failure
// this arm is about.
func readyQueueQuerySites(t *testing.T) []querySite {
	t.Helper()
	fset, file := parseDomainSource(t, "work_items.go")
	fn := funcDeclNamed(t, fset, file, "GetReadyQueue")

	var sites []querySite
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			switch n := s.(type) {
			case *ast.IfStmt:
				walk(n.Body.List)
			case *ast.ForStmt:
				walk(n.Body.List)
			case *ast.RangeStmt:
				walk(n.Body.List)
			case *ast.BlockStmt:
				walk(n.List)
			}

			as, ok := s.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				continue
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Query" {
				continue
			}

			site := querySite{Index: len(sites), Line: fset.Position(as.Pos()).Line}
			// args[0] is the context and args[1] is the statement; everything
			// after that is a bind.
			for _, arg := range call.Args[min(2, len(call.Args)):] {
				if id, ok := arg.(*ast.Ident); ok {
					site.BindIdents = append(site.BindIdents, id.Name)
				}
			}
			if i+1 < len(stmts) {
				site.ErrCodeIdent, site.ErrMessage = returnedNewErr(stmts[i+1])
			}
			sites = append(sites, site)
		}
	}
	walk(fn.Body.List)

	if len(sites) != readyQueueSegmentCount {
		// A Fatal, not an Error: every assertion below is about WHICH site is
		// which, and a census that lost one would report the survivors as a
		// clean tree. Same reasoning as the count arm in
		// ready_queue_query_errors_test.go, which owns the shape of the guards.
		t.Fatalf("the census found %d pool.Query site(s) in GetReadyQueue, want %d. More: a "+
			"segment was added — give it the same guard and raise this number in the same "+
			"change, and check whether it should page with `max`. Fewer: either a segment "+
			"went, or this walk has stopped recognising the calls it claims to census, in "+
			"which case every arm below is vacuous.", len(sites), readyQueueSegmentCount)
	}
	return sites
}

// returnedNewErr pulls the error code identifier and message literal out of a
// statement of the shape `if err != nil { return nil, NewErr(Code, "msg") }`.
func returnedNewErr(stmt ast.Stmt) (code, message string) {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok {
		return "", ""
	}
	for _, s := range ifs.Body.List {
		ret, ok := s.(*ast.ReturnStmt)
		if !ok {
			continue
		}
		for _, res := range ret.Results {
			call, ok := res.(*ast.CallExpr)
			if !ok {
				continue
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "NewErr" || len(call.Args) < 2 {
				continue
			}
			codeIdent, ok := call.Args[0].(*ast.Ident)
			if !ok {
				continue
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return codeIdent.Name, ""
			}
			msg, err := strconv.Unquote(lit.Value)
			if err != nil {
				return codeIdent.Name, ""
			}
			return codeIdent.Name, msg
		}
	}
	return "", ""
}

// readyQueuePagedSegments are the segments whose query carries the caller's
// `max` as its own LIMIT, named by the message their guard returns.
//
// Written down rather than derived, because the claim on the card is exactly
// that it is THESE three and not the others — "so it is not a budget over the
// response, and one section arriving full says nothing about the others". A
// derived expectation would agree with any answer the code gave.
var readyQueuePagedSegments = map[string]bool{
	"ready":               true, // items[], whose message says "ready" — see the mapping below
	"needs_human_session": true,
	"unclassified":        true,
}

// readyQueueSegmentWord maps each of ReadyQueue's json keys to the word its own
// query's error message uses, and is checked in BOTH directions below.
//
// 🔴 Six of the seven use the key itself. items[] is the exception: its message
// is "failed to query ready items", the endpoint's own name for that segment
// (GET /v1/work_items/ready), and it has read that way since before the other
// six had guards at all. Recorded as a mapping rather than smoothed over with a
// looser pattern, because "the message names its segment" is only worth
// asserting if a message naming the WRONG segment is red — and a pattern loose
// enough to accept "ready" for items[] accepts "paused" for it too.
var readyQueueSegmentWord = map[string]string{
	"items":               "ready",
	"running":             "running",
	"stalled":             "stalled",
	"paused":              "paused",
	"needs_human_session": "needs_human_session",
	"unclassified":        "unclassified",
	"stale_running":       "stale_running",
}

// readyQueueErrMessage is the shape the card publishes: `failed to query
// <segment> items`.
var readyQueueErrMessage = regexp.MustCompile(`^failed to query (\S+) items$`)

// TestReadyQueueMaxPagesThreeOfTheSevenSegments holds the card's `max` claim:
// a PER-SECTION page size that reaches three of the seven sections, and not a
// budget over the response.
//
// It also pins that items[] is the first query the function sends, which is what
// makes the reachable case of a mid-call pool failure narrow: a pool that is
// already down fails there, before any segment has been rendered.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the code, the card untouched) ──
//	M1  drop `max` from the unclassified[] query's binds
//	                                            RED  that segment stops paging
//	M2  add `max` to the paused[] query's binds  RED  an unbounded segment pages
//	M3  swap the items[] and running[] blocks    RED  "the first query GetReadyQueue
//	                                                  sends belongs to the running
//	                                                  segment, not items[]"
//
//	── publication side (the card, the code untouched) ──
//	M4  delete the sentence from the card       RED  K12 POPULATION_MOVED — the
//	                                                 candidate and its citation
//	                                                 leave together
//	G1  control: reword that sentence, citation untouched
//	                                          GREEN  this arm reads the queries and
//	                                                 not the card, so K12 owns its
//	                                                 publication side
func TestReadyQueueMaxPagesThreeOfTheSevenSegments(t *testing.T) {
	sites := readyQueueQuerySites(t)

	paged := 0
	for _, s := range sites {
		word, ok := segmentWordOf(s)
		if !ok {
			t.Errorf("work_items.go:%d: the query at census position %d returns %q, which is "+
				"not the `failed to query <segment> items` shape — this arm cannot tell which "+
				"segment it belongs to, so it cannot tell whether that segment should page",
				s.Line, s.Index, s.ErrMessage)
			continue
		}
		want := readyQueuePagedSegments[word]
		got := s.TakesPageSize()
		if got != want {
			paging := "pages with `max`"
			if !got {
				paging = "does not page with `max`"
			}
			t.Errorf("work_items.go:%d: the %s segment %s, and the published `max` description "+
				"says the opposite.\n    `max` is a PER-SECTION page size and reaches exactly "+
				"three sections: items[], needs_human_session[] and unclassified[]. The other "+
				"four are unbounded, which is why one section arriving full says nothing about "+
				"the others. Changing which segments page changes what a caller's `max` means, "+
				"so the schema string in internal/mcp/tools_lifecycle.go and the "+
				"pf_get_ready_queue card have to move in the same diff.", s.Line, word, paging)
		}
		if got {
			paged++
		}
	}

	if paged != len(readyQueuePagedSegments) {
		t.Errorf("%d of the seven segments page with `max`, want %d — a count that moves "+
			"without a named segment moving is this arm having stopped recognising the binds",
			paged, len(readyQueuePagedSegments))
	}

	// items[] first. Without this the sentence "a pool that is down already
	// fails at items[], the first query" is unheld — and it is the sentence that
	// makes the aihub#500 error contract's reachable case narrow.
	if first, ok := segmentWordOf(sites[0]); !ok || first != readyQueueSegmentWord["items"] {
		t.Errorf("the first query GetReadyQueue sends belongs to the %q segment, not items[]. "+
			"A pool that is down then fails somewhere in the middle of the response instead of "+
			"on the first statement, which widens the partial-queue window aihub#500 closed "+
			"rather than the narrow one the card describes.", first)
	}

	t.Logf("ready queue: %d query sites, %d paging with max (%s)",
		len(sites), paged, strings.Join(sortedSegmentWords(readyQueuePagedSegments), ", "))
}

// TestEveryReadyQueueSegmentQueryErrorIsA500NamingThatSegment holds the other
// half of aihub#500's contract: not merely that every segment answers its
// send-time failure — ready_queue_query_errors_test.go owns that — but that the
// answer is the 500 the card publishes, and that it names the segment that
// failed.
//
// The two halves are separable, and the second was unheld: a segment whose guard
// returned ErrBadRequest, or returned the message of the segment before it,
// satisfied the existing scanner completely.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the code, the card untouched) ──
//	M5  paused[]'s guard returns ErrBadRequest   RED  a failed segment answered as
//	                                                  the caller's mistake
//	M6  copy needs_human_session[]'s message onto unclassified[]
//	                                             RED  two segments naming one, and a
//	                                                  segment whose failure arrives
//	                                                  under another name
//	M7  point codeToHTTPStatus's default at 503   RED  "answers 503 INTERNAL_ERROR;
//	                                                  the card publishes 500" — the
//	                                                  status is read off the mapping,
//	                                                  never spelled out here
//
//	── publication side (the card, the code untouched) ──
//	M8  delete the sentence from the card        RED  K12 POPULATION_MOVED
//	G2  control: reword it, citation untouched GREEN  same reason as G1
func TestEveryReadyQueueSegmentQueryErrorIsA500NamingThatSegment(t *testing.T) {
	sites := readyQueueQuerySites(t)

	// The status is read off the mapping the server actually uses rather than
	// spelled out here, so "the constant moved but the card did not" is red too
	// — the base-strength rule from aihub#543 spec §3.4.
	published := NewErr(ErrInternalError, "probe")
	if published.HTTPStatus != 500 || string(published.Code) != "INTERNAL_ERROR" {
		t.Errorf("NewErr(ErrInternalError, …) answers %d %s; the pf_get_ready_queue card "+
			"publishes `500 INTERNAL_ERROR` for all seven segments. Either codeToHTTPStatus "+
			"moved or the code was renamed — the card's hop-5 section has to move with it.",
			published.HTTPStatus, published.Code)
	}

	seen := map[string]int{}
	for _, s := range sites {
		if s.ErrCodeIdent != "ErrInternalError" {
			t.Errorf("work_items.go:%d: the query at census position %d answers a send-time "+
				"failure with %s, not ErrInternalError. A failed segment is not the caller's "+
				"mistake, and a 4xx tells them to change a request that was fine.",
				s.Line, s.Index, s.ErrCodeIdent)
			continue
		}
		word, ok := segmentWordOf(s)
		if !ok {
			t.Errorf("work_items.go:%d: message %q does not match `failed to query <segment> "+
				"items`. The caller gets a 500 that does not say which of the seven queries "+
				"failed, which is the one thing the message is for.", s.Line, s.ErrMessage)
			continue
		}
		seen[word]++
	}

	for word, n := range seen {
		if n > 1 {
			t.Errorf("%d segments answer with the same message word %q. Two segments naming one "+
				"segment means one of them is reporting the other's failure, and the copy is "+
				"invisible in review because both messages are individually correct-looking.",
				n, word)
		}
	}

	// Both directions against the struct: every marshalled segment has a query
	// that names it, and every named segment is still a marshalled one.
	keys := readyQueueJSONSegmentKeys(t)
	for _, key := range keys {
		word, mapped := readyQueueSegmentWord[key]
		if !mapped {
			t.Errorf("ReadyQueue marshals the %q segment and readyQueueSegmentWord has no row "+
				"for it, so nothing checks that its query error names it. Add the row in the "+
				"same change that added the segment.", key)
			continue
		}
		if seen[word] == 0 {
			t.Errorf("no query in GetReadyQueue answers with %q, so the %q segment's failure "+
				"either goes unreported or arrives under another segment's name.",
				fmt.Sprintf("failed to query %s items", word), key)
		}
	}
	for key := range readyQueueSegmentWord {
		if !containsString(keys, key) {
			t.Errorf("readyQueueSegmentWord has a row for %q, which ReadyQueue no longer "+
				"marshals — a stale row makes this arm check one segment more than exists, and "+
				"the extra one can never be satisfied", key)
		}
	}

	// The measured status, not the literal 500: a line that printed the number it
	// was asserting would go on saying 500 through the one mutant that moves it.
	t.Logf("ready queue: all %d segments answer %d %s, naming %s",
		len(sites), published.HTTPStatus, published.Code, strings.Join(sortedSeen(seen), ", "))
}

// segmentWordOf extracts the segment word out of a site's error message.
func segmentWordOf(s querySite) (string, bool) {
	m := readyQueueErrMessage.FindStringSubmatch(s.ErrMessage)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// readyQueueJSONSegmentKeys reads the segment keys off the struct that marshals
// them, by reflection rather than by parsing — this test is IN package domain,
// so the type itself is the cheapest authority available.
//
// A segment is a slice of an *Item type, the same discriminator
// internal/mcp/ready_queue_section_count_test.go uses, which is what keeps
// request_adjusted (a slice, but a disclosure envelope) out of the census.
func readyQueueJSONSegmentKeys(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(ReadyQueue{})
	var keys []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Slice || !strings.HasSuffix(f.Type.Elem().Name(), "Item") {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" {
			keys = append(keys, name)
		}
	}
	if len(keys) != readyQueueSegmentCount {
		t.Fatalf("reflection found %d segment field(s) on ReadyQueue, want %d — with the wrong "+
			"set the both-ways comparison below compares nothing", len(keys), readyQueueSegmentCount)
	}
	return keys
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func sortedSegmentWords(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSeen(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
