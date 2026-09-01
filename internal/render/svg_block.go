package render

// A block parser for top-level, MULTI-LINE <svg>...</svg> (aihub#262).
//
// # The bug this closes
//
// goldmark's stock HTML-block parser implements CommonMark's seven HTML block types. A
// line-initial `<svg ...>` opens a type-7 block, and a type-7 block's own Continue()
// closes it at the first blank line (util.IsBlank check in html_block.go). Hand-written
// SVG routinely has blank lines between element groups, so the block ends mid-figure and
// everything after the blank line is handed back to the markdown parser: an indented
// (4+ space) line becomes an indented code block, and an open tag with trailing content
// (e.g. `<text x="10" y="20">Label</text>`) becomes a <p>. A <p> start tag inside SVG
// foreign content triggers the HTML5 parser's breakout rule and force-closes the <svg> —
// the figure collapses to whatever came before the blank line, and the rest of the
// drawing is sprayed below as literal text and code blocks.
//
// A single physical line can never exhibit this bug — there is no blank line to trip
// over inside one line — so this parser deliberately leaves single-line `<svg>...</svg>`
// (however long) untouched: see "Only multi-line <svg> is taken over" below. It exists
// only for the case that actually breaks: an opening `<svg` whose balancing `</svg>` is
// on a later line.
//
// # The fix
//
// svgBlockParser is a second, higher-priority BlockParser that recognizes the same
// trigger line and, instead of deferring to CommonMark's per-line blank-line rule, looks
// ahead through the *rest of the document* for the </svg> that balances the opening tag
// (honouring nesting: <svg> inside <svg> is legal). If one is found on a later line,
// every line in between — blank or indented, it does not matter — is consumed verbatim
// as a single raw HTML block, so goldmark never gets a chance to re-parse the middle of
// the figure as markdown.
//
// # Only multi-line <svg> is taken over
//
// Open() computes where the balancing </svg> is, then checks it against the opening
// line's own Segment.Stop before deciding to take the block over. In goldmark's
// text.Reader, a line Segment's Stop is one past that line's trailing '\n' (or, on a
// final line with no trailing newline, equal to len(source) — see reader.go's
// AdvanceLine, which sets pos.Stop = pos.Start + index-of('\n') + 1). Concretely:
//
//   - "<svg ...></svg>\n"                 -> end lands ON the '\n' itself, i.e.
//     end == segment.Stop-1 < segment.Stop.
//   - "<svg ...></svg> trailing text\n"   -> end lands before the trailing text, still
//     < segment.Stop (the whole line, trailing text included, is one Segment).
//   - "<svg ...></svg>" with no trailing newline at EOF -> segment.Stop == len(source)
//     == end (both land on "one past the last byte").
//
// In every one of those, end <= segment.Stop, so the check below hands the line straight
// back to the next-priority parser (goldmark's stock HTMLBlockParser / paragraph inline
// HTML), unchanged from today. Only when the balancing </svg> is on a later line —
// meaning end is strictly beyond this line's Stop — does this parser take over. This
// matters in practice: SVG is `display:inline` by default, so a single-line <svg> is
// routinely followed on the very same paragraph by ordinary prose (`<svg .../> **bold**
// text`), or sits adjacent to another single-line <svg> with only a blank line between
// (each its own <p>), or is itself the last line of a paragraph that soft-wraps onto the
// next. Taking over any of those regresses working output for a case that was never
// broken in the first place.
//
// # Fail-closed, on purpose — a deliberately conservative, line-level heuristic, not a
// # markdown parser, and not "detected precisely"
//
// findSVGBlockEnd's lookahead operates at the raw HTML-tokenizer level: it has no idea
// what a fenced code block, an indented code block, or an inline code span is — those
// are markdown constructs, and this is not a markdown parser. Left alone, that would let
// a `</svg>` that only *looks* like a real closing tag — because it happens to sit inside
// a ```` ``` ```` fence or a `` ` `` code span later in the document — terminate the
// lookahead and swallow everything between the unclosed <svg> and that false closer
// (prose turned into raw HTML, fence markers leaking into the output, a `<pre><code>`
// swallowing the tail). Two carve-outs reduce that surface without writing a second
// hand-rolled markdown parser, and both are deliberately coarse, line-level heuristics —
// neither is CommonMark's actual fence or code-span rule, and both have known, accepted
// misses and over-triggers documented below and pinned by tests:
//
//   - fencedCodeOpenRegexp (0-3 leading spaces, then a run of 3+ backticks or tildes,
//     matched against each RAW SOURCE LINE) is treated as a hard wall: the lookahead
//     never scans past the first such line. If no balancing </svg> was found before that
//     wall, findSVGBlockEnd fails closed exactly as it would at real EOF — it does not
//     try to "skip over" the fence and resume on the far side, because correctly matching
//     that fence's own closing delimiter (same character, same or greater run length) to
//     know where it's safe to resume is exactly the kind of markdown-parsing-in-miniature
//     this file's package doc has twice now (see the CDATA and mem_Z4Ap0DdL notes below)
//     warned against re-deriving by hand.
//
//     Because this is a raw-line regexp and not CommonMark's fence rule, it MISSES some
//     real fences (the wall never goes up, so the lookahead can swallow prose past them):
//     a fence inside a blockquote (`> ```` ``` ````) — the `>` prefix means the line
//     doesn't start with backticks at column 0-3 — and a fence indented 4+ raw spaces
//     inside a list item (legal CommonMark once the list marker's own indent is
//     subtracted, but this regexp does no such subtraction). See
//     TestSVGBlock_FenceWallMiss_InsideBlockquote and
//     TestSVGBlock_FenceWallMiss_IndentedInListItem. It also OVER-TRIGGERS: a line that
//     merely begins with ``` or ~~~ inside a real, legitimate multi-line <svg> stops the
//     lookahead there, same as a real fence would. See
//     TestSVGBlock_FenceOverTrigger_InsideRealSVG.
//
//     Both directions were judged acceptable and are kept on purpose: a miss (fail OPEN)
//     is only reachable when an unclosed line-initial `<svg>` already exists earlier in
//     the document — i.e. the input was already going to render badly — and an
//     over-trigger (fail CLOSED) only degrades a legitimate figure to exactly the
//     pre-fix rendering, never worse. The wall still strictly shrinks the pre-existing
//     fail-open surface relative to having no wall at all, which is the bar this file
//     needs to clear.
//
//   - A same-line inline code span is handled by an even coarser heuristic
//     (isInsideLineCodeSpan: is there a backtick before this `</svg>` and a backtick
//     after it, on the same source line?), not a CommonMark-accurate code-span parser
//     (which would need to match backtick run lengths and handle escaping). Getting this
//     wrong can only make the parser MORE conservative — a false "yes, code span" just
//     means a real closing tag gets ignored and the lookahead fails closed, falling back
//     to pre-fix rendering — never the reverse, so approximating is safe here in a way it
//     would not be for the fence wall above.
//
// What is NOT handled by isInsideLineCodeSpan, and is a deliberate, narrow, documented
// fail-OPEN gap rather than a pretended fail-closed guarantee: an inline code span that
// itself spans a soft line break (so the "backtick before and after on the same line"
// heuristic sees no code span at all), or one built from an unusual backtick-escaping
// shape. In that narrow sliver, a `</svg>` mentioned in running prose could still be
// mistaken for a real closing tag. This is judged acceptable because (a) it requires a
// fairly contrived document to hit, and (b) the output in that sliver is still passed
// through SanitizeArtifactHTML on every /ui path exactly as before — this file has no
// opinion on sanitization either way.
//
// # Performance: a document full of unclosed <svg> lines must not be quadratic
//
// Naively, every line-initial `<svg` that goldmark's block-parser trial loop offers to
// Open() triggers its own independent findSVGBlockEnd scan of "the rest of the document."
// A document that is mostly repeated, never-closed `<svg ...>` lines (each separated by a
// blank line, so each is its own new block-opening attempt) makes every one of those
// scans run all the way to EOF: O(n) work, O(n) times, i.e. O(n²) — measured at 21ms at
// 500 lines, 301ms at 2000, 1.14s at 4000, ~67s at ~1MB before the fix below. The same
// blowup happens, worse, on a document of repeated NESTED-but-unbalanced `<svg>\n<svg>\n
// </svg>\n\n` groups: 300ms at 25KB, 4.67s at 100KB, 8m20s at 1MB.
//
// An earlier version of this fix tried to bound that with memoization: the claim was
// that once a scan runs all the way to the true end of the document without finding a
// balancing </svg>, that fact — "no closer exists at or after offset L" — holds for
// every later start offset too, because source[Y:] is a byte-suffix of source[L:]. That
// claim is FALSE for this tokenizer, and is why it was deleted rather than patched: see
// "A memoization scheme that does NOT work" below for why, and TestSVGBlock_
// RawtextDoesNotHideRealCloser for the regression it would otherwise reintroduce. It also
// never fired at all on the nested-unbalanced shape above, because the memoized offset is
// derived from the LAST </svg> token seen (near EOF), so no earlier <svg> opener's start
// offset is ever >= it — the O(n²) blowup went completely unbounded on that shape.
//
// What replaces it makes no claim about tokenizer state at all: a single, per-parse
// (parser.Context-scoped — see "Concurrency safety" below) tokenizer-step BUDGET,
// svgBlockWorkBudgetKey, initialized on first use to svgBlockWorkBudgetMultiplier (K) *
// len(source) and decremented by the step count of every findSVGBlockEnd call, whether
// that call succeeds, hits the fence wall, hits the byte cap, or reaches real EOF. Once
// the budget reaches zero, every subsequent Open() fails closed immediately — returns
// nil, parser.NoChildren without scanning at all — degrading the rest of the document
// straight to pre-fix rendering. Because the budget is shared across every Open() call in
// the parse and only ever decreases, tokenizer-step work done BY findSVGBlockEnd across
// the WHOLE parse is bounded by K * len(source) by construction. That bound does not
// depend on any invariant about tokenizer state, document shape, or how many times
// Open() is called — unlike the deleted memo, there is nothing here that a rawtext tag or
// an unterminated comment can invalidate. To be precise about what this bound covers: it
// is a bound on xhtml tokenizer .Next() calls specifically, not on every byte of work
// this file does per Open() call — see the next paragraph for the other per-call cost
// this does NOT cover, and how it is bounded separately.
//
// K = 8 (svgBlockWorkBudgetMultiplier below, chosen from the 4-16 range as a generous
// middle value): a well-formed document's total top-level lookahead work is naturally
// close to O(len(source)) — once a top-level <svg> block is open, every line inside it
// (including a nested <svg>) is consumed by Continue(), which does not scan at all, so
// each byte is subject to at most one real findSVGBlockEnd scan (the single Open() call
// for the top-level line it lives under). K=8 leaves roughly an 8x safety margin over
// that legitimate cost — comfortably generous for every real document — while still
// bounding the pathological, adversarial case to a small constant multiple of document
// size instead of letting it run unbounded. This also pins a threshold: because a single
// scan's step count can never exceed the number of bytes it scans, K=8 guarantees at
// least 8 full-document-worth of scanning before the budget can be exhausted, so
// exhaustion is only reachable once a document already has >= 8 unclosed top-level <svg>
// openers — a document that was already broken pre-fix. Below that count, every
// well-formed figure in the document is still fixed; see
// TestSVGBlock_BudgetExhaustionThreshold.
//
// A per-scan byte cap (maxSVGLookaheadBytes) is kept as an independent, secondary
// backstop bounding any ONE call's tokenizer work in isolation, but it is not what makes
// total tokenizer work linear — the shared budget above is the load-bearing bound and
// holds regardless of whether the byte cap ever binds.
//
// The budget above bounds tokenizer .Next() calls, but every findSVGBlockEnd call also
// asks firstFenceBoundary "where is the next fence wall at or after this start offset?" —
// and a naive answer to that (re-walking source line by line from start to find one) is
// itself O(remaining document) per call, charged against nothing, so a document made
// entirely of well-formed, correctly-declined single-line <svg> lines (each costs only a
// few tokenizer steps, so the budget above barely moves) could still be made quadratic by
// the fence lookup alone. The fix: the set of fence-opening line offsets is a property of
// the whole document, computed once, not per findSVGBlockEnd call — a single O(n) pass
// (computeFenceOffsets), cached on parser.Context as svgFenceOffsetsKey — and
// firstFenceBoundary answers each call in O(log n) via sort.SearchInts over that cached,
// sorted list instead of rescanning. This is a second, independent bound (an O(n) index
// build plus O(log n) per lookup) alongside the tokenizer-step budget above; together they
// are what makes total lookahead work for the whole parse linear in every dimension this
// file's Open() touches, not just tokenizer steps.
//
// # Why golang.org/x/net/html and not a regexp
//
// Team memory (mem_Z4Ap0DdL) already recorded — twice, in sanitize.go's <style> history —
// that a hand-rolled scanner whose correctness depends on "our understanding of a parse
// boundary == the browser's" gets bypassed by exactly the input it didn't model (an
// attribute value or comment containing literal "</svg>", CDATA, etc.) and was deleted
// rather than patched a third time. findSVGBlockEnd instead runs the same tokenizer the
// repo already trusts for this class of problem (see postsanitize_boundary_test.go) over
// the remaining source and tracks nesting depth at the token level, so those cases are
// handled by a real HTML tokenizer, not by us. That said, "a real tokenizer" is not the
// same claim as "matches a browser's parse boundary in every case" — see the next section
// for one specific, accepted place where it does not.
//
// # A memoization scheme that does NOT work — kept as a comment so it isn't tried again
//
// It is tempting to bound findSVGBlockEnd's total cost by remembering, per parse, the
// smallest offset L for which a scan has already proven "no </svg> end-tag token exists
// anywhere at or after L," on the theory that source[Y:] for any Y >= L is a byte-suffix
// of source[L:], and a suffix of "no closer exists" still has no closer. This repo tried
// exactly that and it is unsound: golang.org/x/net/html's tokenizer state is NOT
// suffix-invariant. Its rawtext tag set — iframe, noembed, noframes, noscript,
// plaintext, script, style, textarea, title, xmp — puts the tokenizer into a mode where
// everything up to that specific tag's own closing tag (or EOF) is swallowed as opaque
// text, hiding any `</svg>` token inside it; an unterminated HTML comment does the same
// for the rest of the document. A scan that starts before such an unterminated tag can
// walk straight through a real, later `</svg>` without ever tokenizing it as an end tag,
// and "reached true EOF, saw zero (or only early) </svg> tokens" is exactly the state
// that scheme used to memoize "no closer past here" — a false negative that would
// short-circuit a LATER scan (starting after the unterminated tag) that never enters it
// and sees the real closer just fine. Concretely: an unclosed `<svg><title>` (a routine
// SVG accessibility child) or `<svg><script>` near the top of a document would make a
// later, perfectly well-formed, multi-line <svg> fail to be fixed — rect=1, text=0, the
// exact bad signature this file exists to remove. See
// TestSVGBlock_RawtextDoesNotHideRealCloser. The fix in the "Performance" section above
// does not rely on this or any other claim about tokenizer state.
//
// # A known, deliberate divergence from a browser: the full rawtext tag set
//
// golang.org/x/net/html's tokenizer puts a fixed set of tag names — iframe, noembed,
// noframes, noscript, plaintext, script, style, textarea, title, xmp — into "rawtext"
// mode unconditionally once it sees the start tag; this is a rule of the tokenizer's
// state machine, applied the same way regardless of namespace. So a literal "</svg>"
// written inside, say, `<svg><script>var a="</svg>";</script>` does not terminate this
// scan's rawtext run; the scan correctly keeps looking past it for a real closing tag.
// `<title>` is a routine, accessible SVG child; `<textarea>` and `<iframe>` are legal
// inside an SVG `<foreignObject>`; `plaintext` is a one-way trapdoor in the tokenizer
// (nothing after it is ever treated as a tag again). A browser is stricter here: per the
// HTML5 foreign-content rules, none of these get ordinary HTML rawtext treatment inside
// SVG, so a browser can break out of the <svg> earlier than this scan does on such input.
//
// The direction of this mismatch is what matters: this scan is MORE permissive than a
// browser (it treats more of the document as "still inside the svg" than a browser
// would), never less. It cannot cause this parser to swallow prose that a browser would
// have treated as markdown — the one failure mode this file exists to prevent — and the
// raw bytes emitted still pass through SanitizeArtifactHTML on every /ui path, which
// strips script/style/etc. regardless of where this scan drew the boundary. This is a
// narrow, accepted gap, not "our boundary matches the browser's."
//
// # Concurrency safety
//
// svgBlockWorkBudgetKey and svgBlockEndOffsetKey are both stored on parser.Context, which
// goldmark constructs fresh per call to Parser().Parse() / md.Convert() — never shared
// across concurrent parses. So the work budget is per-document, not per-process: two
// goroutines each rendering their own document each get their own K*len(source) budget,
// and neither can exhaust or corrupt the other's. (svgTokenizerStepsForTest below is the
// one process-wide, cross-goroutine piece of state in this file, and it exists only for
// tests — see its own doc comment for why it's an atomic.)
//
// # This does not change the trust boundary
//
// This parser only moves where a BLOCK ends; it does not change what is trusted. The raw
// SVG bytes it emits are exactly the bytes goldmark would have emitted for a single-line
// (no-blank-line) <svg>...</svg> today, and they still flow through SanitizeArtifactHTML
// on every /ui path exactly as before — this file has no opinion on sanitization and does
// not touch SanitizeArtifactHTML, SafeEmbedDocument, RenderDiagramsGated, or the d2
// pipeline.
import (
	"bytes"
	"regexp"
	"sort"
	"sync/atomic"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	xhtml "golang.org/x/net/html"
)

// svgBlockOpenRegexp triggers on a line with 0-3 leading spaces (CommonMark's html-block
// indent budget — 4+ spaces is an indented code block and must stay one) whose content
// starts with "<svg" followed by whitespace, '>', or '/' (so "<svg>", "<svg/>", and
// `<svg width="1">` all trigger, but "<svgfoo>" does not). Case-insensitive to also catch
// "<SVG ...>".
var svgBlockOpenRegexp = regexp.MustCompile(`(?i)^[ ]{0,3}<svg(?:[\s>/]|$)`)

// fencedCodeOpenRegexp recognizes a line that opens a CommonMark fenced code block: 0-3
// leading spaces, then a run of 3 or more identical backticks or tildes (an info string
// may follow; we don't need to look at it). Used only to build a hard wall that
// findSVGBlockEnd's lookahead refuses to scan past — see the package comment.
var fencedCodeOpenRegexp = regexp.MustCompile("^[ ]{0,3}(`{3,}|~{3,})")

// maxSVGLookaheadBytes bounds how much of the document a single findSVGBlockEnd call
// will tokenize, as a secondary, per-call backstop. It does not, by itself, bound total
// work across a whole parse — svgBlockWorkBudgetKey below is what does that. See the
// package comment's "Performance" section.
const maxSVGLookaheadBytes = 8 << 20 // 8 MiB

// svgBlockWorkBudgetMultiplier (K) bounds total findSVGBlockEnd tokenizer-step work
// across an ENTIRE parse to K * len(source). See the package comment's "Performance"
// section for the rationale behind this value and this mechanism.
const svgBlockWorkBudgetMultiplier = 8

// svgBlockEndOffsetKey stores, for the currently-open svg block, the absolute byte offset
// in the source (one past the balancing </svg>) that Continue() must consume up to.
var svgBlockEndOffsetKey = parser.NewContextKey()

// svgBlockWorkBudgetKey stores, per document parse (parser.Context is created fresh per
// Parse()/Convert() call — see the package comment's "Concurrency safety" section), the
// remaining tokenizer-step budget for findSVGBlockEnd lookaheads. Initialized on first
// use in Open() to svgBlockWorkBudgetMultiplier * len(source), and decremented by every
// findSVGBlockEnd call's actual step count regardless of that call's outcome. Once it
// reaches zero, Open() fails closed without scanning. This bounds the tokenizer-step
// work findSVGBlockEnd itself does across the whole parse — see the package comment's
// "Performance" section. It does NOT bound firstFenceBoundary's fence lookup; that is
// bounded separately by svgFenceOffsetsKey below.
var svgBlockWorkBudgetKey = parser.NewContextKey()

// svgFenceOffsetsKey stores, per document parse, the sorted slice of absolute byte
// offsets of every line in source that opens a fenced code block (see
// fencedCodeOpenRegexp), computed once by computeFenceOffsets on first use in Open().
// The set of such offsets is a property of the whole document and never changes during
// a parse, so scanning for it once and thereafter binary-searching (see
// firstFenceBoundary) turns what used to be an O(remaining-document) rescan on every
// single findSVGBlockEnd call into one O(n) pass per parse plus an O(log n) lookup per
// call. See the package comment's "Performance" section for the pathological shape this
// closes (many well-formed single-line <svg> lines, each triggering its own Open() and,
// pre-fix, its own full-document fence rescan).
var svgFenceOffsetsKey = parser.NewContextKey()

// computeFenceOffsets returns, in ascending order, the absolute byte offset of the start
// of every line in source that opens a fenced code block per fencedCodeOpenRegexp. This
// is the same line-by-line walk firstFenceBoundary used to do per call; it now runs
// exactly once per parse (cached on parser.Context as svgFenceOffsetsKey), and
// firstFenceBoundary binary-searches the result instead of re-walking the document.
//
// Postcondition: never returns nil — a fence-free document yields an empty, non-nil
// []int — so the cache-hit check in Open() (pc.Get(svgFenceOffsetsKey).([]int)) is
// unconditional. This matters because goldmark's parser.Context keeps values in a
// []any: an unset key comes back as untyped nil (assertion fails, cache miss) while a
// stored typed-nil []int comes back as a typed nil (assertion succeeds, cache hit) —
// but that is an implementation detail of parser.Context, not a documented guarantee.
// If it ever stopped holding, a stored nil would read back as a cache miss and
// computeFenceOffsets would re-run its full O(n) scan on every Open() call,
// reintroducing the O(n^2) blowup round 3 removed. Returning a non-nil empty slice here
// makes the cache hit unconditional regardless of that detail.
//
// Also increments svgFenceScanLinesForTest, once per call, by the number of lines this
// walk examined — see that variable's doc comment for why, and for the invariant this
// lets tests assert directly: since this function runs at most once per parse (cached),
// the total lines it examines across a whole parse is exactly the document's line
// count, never multiplied by the number of svg-opening lines in the document.
func computeFenceOffsets(source []byte) []int {
	offsets := []int{}
	pos := 0
	lines := 0
	for pos < len(source) {
		var line []byte
		var next int
		if rel := bytes.IndexByte(source[pos:], '\n'); rel < 0 {
			line = source[pos:]
			next = len(source)
		} else {
			line = source[pos : pos+rel+1]
			next = pos + rel + 1
		}
		lines++
		if fencedCodeOpenRegexp.Match(line) {
			offsets = append(offsets, pos)
		}
		pos = next
	}
	svgFenceScanLinesForTest.Add(int64(lines))
	return offsets
}

// newSVGBlockParser returns the BlockParser described above.
func newSVGBlockParser() parser.BlockParser {
	return &svgBlockParser{}
}

type svgBlockParser struct{}

func (b *svgBlockParser) Trigger() []byte {
	return []byte{'<'}
}

// CanInterruptParagraph mirrors CommonMark's type-7 HTML block rule: a tag that is not on
// the allowed-block-tags list (svg is not) must not interrupt an open paragraph. Without
// this, `<svg>` appearing as the continuation of running prose would newly become a block
// where today it is inline raw HTML — a behavior change outside the bug this file fixes.
func (b *svgBlockParser) CanInterruptParagraph() bool {
	return false
}

// CanAcceptIndentedLine is false so goldmark's own indent-width gate (parser.go's
// openBlocks, `w > 3 && !bp.CanAcceptIndentedLine()`) filters out 4+-space-indented lines
// before Open is even called, on top of svgBlockOpenRegexp's own 0-3 leading-space budget.
func (b *svgBlockParser) CanAcceptIndentedLine() bool {
	return false
}

func (b *svgBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	if !svgBlockOpenRegexp.Match(line) {
		return nil, parser.NoChildren
	}

	// Per-parse tokenizer-step budget: initialize on first use to K * len(source), and
	// fail closed immediately — without scanning at all — once it's exhausted. See the
	// package comment's "Performance" section for why this (and not the deleted
	// suffix-invariant memoization) is what bounds total lookahead work for the whole
	// parse.
	source := reader.Source()
	budget, ok := pc.Get(svgBlockWorkBudgetKey).(int)
	if !ok {
		budget = svgBlockWorkBudgetMultiplier * len(source)
	}
	if budget <= 0 {
		return nil, parser.NoChildren
	}

	// Per-parse fence-offset index: computed once (an O(n) pass) and cached, then
	// binary-searched by every findSVGBlockEnd call instead of each one rescanning the
	// rest of the document for the next fence wall. See svgFenceOffsetsKey's doc
	// comment for why this is safe to cache (the set of fence-opening lines is
	// document-global and immutable) and the package comment's "Performance" section
	// for the pathological shape (many well-formed single-line <svg> lines) this closes.
	fenceOffsets, ok3 := pc.Get(svgFenceOffsetsKey).([]int)
	if !ok3 {
		// computeFenceOffsets never returns nil (see its doc comment), so the cache
		// hit above is unconditional on every subsequent Open() call in this parse.
		fenceOffsets = computeFenceOffsets(source)
		pc.Set(svgFenceOffsetsKey, fenceOffsets)
	}

	// Look ahead over the rest of the document WITHOUT consuming anything. If no
	// balancing </svg> exists (or the lookahead refuses to look further — see the
	// fenced-code-block wall, the byte cap, or budget exhaustion in findSVGBlockEnd),
	// fail closed: return nil so the next-priority parser (goldmark's stock
	// HTMLBlockParser) gets to try this exact, untouched line.
	end, ok2, steps := findSVGBlockEnd(source, segment.Start, budget, fenceOffsets)
	budget -= steps
	if budget < 0 {
		budget = 0
	}
	pc.Set(svgBlockWorkBudgetKey, budget)
	if !ok2 {
		return nil, parser.NoChildren
	}

	// Only take over when the balancing </svg> is NOT on the opening line — see "Only
	// multi-line <svg> is taken over" in the package comment for segment.Stop's exact
	// semantics and the three boundary cases this guards.
	if end <= segment.Stop {
		return nil, parser.NoChildren
	}

	node := ast.NewHTMLBlock(ast.HTMLBlockType6)
	node.Lines().Append(segment)
	reader.AdvanceToEOL()
	pc.Set(svgBlockEndOffsetKey, end)
	return node, parser.NoChildren
}

func (b *svgBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	htmlBlock := node.(*ast.HTMLBlock)
	lines := htmlBlock.Lines()

	// If the line already appended (by Open, or by a previous call to Continue) already
	// reached the balancing </svg>, the block is done and this call must not consume the
	// line that follows it — that line belongs to whatever comes next (more raw HTML,
	// markdown, or EOF).
	if lines.Len() > 0 {
		end, _ := pc.Get(svgBlockEndOffsetKey).(int)
		if lines.At(lines.Len()-1).Stop >= end {
			return parser.Close
		}
	}

	line, segment := reader.PeekLine()
	if line == nil {
		// findSVGBlockEnd guarantees a balancing </svg> exists before EOF, so this is
		// defensive only.
		return parser.Close
	}
	// Deliberately NOT checking util.IsBlank here (unlike the stock type-6/7 Continue) —
	// a blank line inside the figure is exactly what must NOT terminate this block.
	lines.Append(segment)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (b *svgBlockParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {
	// nothing to do; renderHTMLBlock (goldmark's stock html renderer) writes node.Lines()
	// verbatim for any *ast.HTMLBlock when html.WithUnsafe() is set, regardless of which
	// BlockParser produced it.
}

// isInsideLineCodeSpan reports whether the byte at absolute offset pos in source sits on
// a source line that also contains a backtick before pos and a backtick after pos — the
// shape of prose like "The closing tag is `</svg>` here." This is a deliberately coarse,
// single-line heuristic, not a CommonMark-accurate inline-code-span parser (which would
// need to match backtick run lengths and handle escaping) — see the package comment for
// why approximating here is safe (it can only make the lookahead more conservative).
func isInsideLineCodeSpan(source []byte, pos int) bool {
	lineStart := bytes.LastIndexByte(source[:pos], '\n') + 1 // 0 if no preceding '\n'
	lineEnd := len(source)
	if rel := bytes.IndexByte(source[pos:], '\n'); rel >= 0 {
		lineEnd = pos + rel
	}
	return bytes.IndexByte(source[lineStart:pos], '`') >= 0 &&
		bytes.IndexByte(source[pos:lineEnd], '`') >= 0
}

// firstFenceBoundary returns the absolute byte offset of the first line at or after from
// that opens a fenced code block (see fencedCodeOpenRegexp), or len(source) if there is
// none. findSVGBlockEnd treats this as a hard wall it will not scan past — see the
// package comment.
//
// fenceOffsets is the full, ascending list of every such line's offset in source,
// computed once per parse by computeFenceOffsets (see svgFenceOffsetsKey) — this is a
// binary search (sort.SearchInts finds the smallest offset >= from) over that
// document-global, immutable list, not a rescan of source. Behaviour is bit-for-bit
// identical to the line-by-line walk this replaced: same regexp, same lines classified
// as fence-openers, same "first one at or after from" answer.
//
// Increments svgFenceBoundaryCallsForTest once per call (a plain call counter, not a
// work counter — this function's own work is an O(log n) binary search, independent of
// document size). See that variable's doc comment for the invariant this lets tests
// assert: this function is called at most once per findSVGBlockEnd call, i.e. once per
// Open() attempt, never more.
func firstFenceBoundary(source []byte, from int, fenceOffsets []int) int {
	svgFenceBoundaryCallsForTest.Add(1)
	i := sort.SearchInts(fenceOffsets, from)
	if i < len(fenceOffsets) {
		return fenceOffsets[i]
	}
	return len(source)
}

// findSVGBlockEnd scans source[start:] with a real HTML tokenizer (not a regexp — see the
// package doc above) and returns the absolute offset one past the </svg> that balances the
// <svg (or <SVG, ...) tag beginning at start, honouring nesting. ok is false if no
// balancing close tag is found within the scanned region (real EOF, a fenced-code-block
// wall, or the byte cap — see the package comment).
//
// maxSteps bounds how many tokenizer.Next() calls this one call will make: once steps
// reaches maxSteps, the scan stops and reports failure exactly as it would at a real wall,
// without making any claim about what lies beyond (there is no noCloserFrom-style
// memoization result here — see the package comment's "A memoization scheme that does NOT
// work" section for why that was deleted rather than kept). steps is always returned (on
// every return path) so the caller (Open) can decrement its shared, per-parse work budget
// by the actual amount of work this call did, whether it succeeded, failed, or was cut off
// by maxSteps, the fence wall, or the byte cap.
//
// fenceOffsets is passed straight through to firstFenceBoundary — see its doc comment and
// svgFenceOffsetsKey for why this is a per-parse cached list rather than a per-call scan.
func findSVGBlockEnd(source []byte, start int, maxSteps int, fenceOffsets []int) (end int, ok bool, steps int) {
	hardLimit := start + maxSVGLookaheadBytes
	if hardLimit > len(source) {
		hardLimit = len(source)
	}
	scanLimit := firstFenceBoundary(source, start, fenceOffsets)
	if scanLimit > hardLimit {
		scanLimit = hardLimit
	}

	z := xhtml.NewTokenizer(bytes.NewReader(source[start:scanLimit]))
	// SVG is foreign content where CDATA sections are legal; without this the tokenizer
	// treats "<![CDATA[" as a bogus comment instead, which is still harmless here but
	// would let a "</svg>" inside a CDATA section terminate the scan early.
	z.AllowCDATA(true)

	// Charge the process-wide test counter once, on the way out, with the final step
	// count this call did — not once per token. steps is a named return value, so this
	// deferred closure sees its value as of whichever return statement fires. An
	// unconditional atomic.Add per tokenizer step would put a contended cache line on
	// the production hot path (this function runs on every markdown render); a single
	// Add per call does not.
	defer func() { svgTokenizerStepsForTest.Add(int64(steps)) }()

	depth := 0
	consumed := 0
	for {
		if steps >= maxSteps {
			// Shared per-parse budget exhausted mid-scan: fail closed exactly as at a
			// real wall. No fact about the unscanned remainder is claimed or recorded.
			return 0, false, steps
		}
		tt := z.Next()
		steps++
		tokenStart := start + consumed
		consumed += len(z.Raw())
		switch tt {
		case xhtml.ErrorToken:
			// EOF (io.EOF) or a tokenizer read error, OR we hit the fence wall / byte
			// cap: either way, no balancing </svg> was found within the scanned region.
			return 0, false, steps
		case xhtml.StartTagToken:
			name, _ := z.TagName()
			if bytes.Equal(name, []byte("svg")) {
				depth++
			}
		case xhtml.SelfClosingTagToken:
			// "<svg .../>" opens and closes in the same token. If it is the outermost
			// tag (depth == 0, i.e. no enclosing <svg> has been opened yet) it is a
			// complete block all by itself. If it is nested inside an already-open
			// <svg>, it has no net effect on the enclosing depth.
			name, _ := z.TagName()
			if bytes.Equal(name, []byte("svg")) && depth == 0 {
				return start + consumed, true, steps
			}
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			if bytes.Equal(name, []byte("svg")) {
				if isInsideLineCodeSpan(source, tokenStart) {
					// A `</svg>` inside a same-line inline code span (e.g. "the
					// closing tag is `</svg>` here") is prose describing the tag,
					// not a real tag boundary. Ignore it for depth-tracking
					// purposes and keep scanning for a real closer.
					continue
				}
				depth--
				if depth == 0 {
					return start + consumed, true, steps
				}
			}
		}
	}
}

// svgTokenizerStepsForTest counts findSVGBlockEnd's tokenizer.Next() calls across the
// process. It exists so tests can assert on work actually done (a robust signal) instead
// of wall-clock time (a flaky one) when pinning down the O(n) vs O(n²) behavior described
// in the package comment's "Performance" section. Not used by, and has no effect on,
// production rendering. It's an atomic.Int64 (rather than a plain int) purely so
// concurrent Markdown() calls — which the package doc guarantees are supported — never
// race on it, including under `go test -race`. Updated once per findSVGBlockEnd call (via
// a deferred Add of that call's final step count), not once per tokenizer step, so a
// concurrent-rendering hot path only takes one atomic add per lookahead instead of one per
// token.
var svgTokenizerStepsForTest atomic.Int64

// svgFenceScanLinesForTest counts the number of source lines actually walked by
// computeFenceOffsets, across the process. It exists so tests can assert directly on
// the fence-lookup work described in the package comment's "Performance" section,
// which svgTokenizerStepsForTest above does NOT cover (that one counts findSVGBlockEnd's
// tokenizer steps only). Because computeFenceOffsets runs at most once per parse
// (cached on parser.Context as svgFenceOffsetsKey), this counter's delta across a
// single render is exactly that document's line count, regardless of how many
// svg-opening lines the document has — a value larger than that means fence lookup is
// re-scanning per Open() call instead of using the cached index. Not used by, and has
// no effect on, production rendering; an atomic.Int64 for the same concurrent-Markdown()
// / -race reason as svgTokenizerStepsForTest, updated once per computeFenceOffsets call
// (not once per line) for the same reason that one is updated once per findSVGBlockEnd
// call.
var svgFenceScanLinesForTest atomic.Int64

// svgFenceBoundaryCallsForTest counts calls to firstFenceBoundary, across the process.
// Paired with svgFenceScanLinesForTest above so a test can assert "work per call is
// constant, not proportional to the remaining document": firstFenceBoundary's own work
// is an O(log n) binary search independent of document size, so its call count is
// expected to track the number of Open() attempts (one call per findSVGBlockEnd call),
// never the remaining-document-size-weighted cost a per-call rescan would add. Not used
// by, and has no effect on, production rendering; an atomic.Int64 for the same reason as
// the two counters above.
var svgFenceBoundaryCallsForTest atomic.Int64
