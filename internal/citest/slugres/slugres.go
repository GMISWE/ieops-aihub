// Package slugres finds caller-controlled work-item references that reach a
// column holding canonical work_items(id) values without going through the
// one id-or-slug resolution rule this repo has.
//
// # The defect class this exists to prevent
//
// A work item is addressable by canonical id (`wi_...`) or by slug
// (`project#seq`), and the slug is what every human and every polyforge skill
// types. Columns that FK-reference work_items(id) — work_item_id,
// blocked_wi_id, blocking_wi_id, parent_work_item_id — only ever hold the
// canonical id. So code that takes the CALLER'S raw parameter and binds it to
// a comparison, an INSERT, or an UPDATE against one of those columns works
// for ids and silently misbehaves for slugs.
//
// The class recurred four times before this gate, in three distinct shapes,
// and the gate recognises all three:
//
//   - WRITE side (aihub#127): the raw value lands in an INSERT/UPDATE against
//     an FK column. This shape self-reports as a 500 — the foreign key trips —
//     which is why it was found first.
//   - READ side (aihub#343, then aihub#357's handleListDependencies): the raw
//     value lands in a WHERE comparison. A read has NO constraint to trip:
//     `WHERE work_item_id = 'aihub#343'` matches nothing and the endpoint
//     answers 200 with an empty list, indistinguishable from "there is
//     genuinely nothing". This is why read-side instances outlive write-side
//     ones by months.
//   - DEPENDENCY-STYLE call sites (aihub#357): the handler RESOLVES the
//     reference for its access check and then hands the caller's ORIGINAL
//     parameter to a function whose parameter is canonical-by-contract
//     (handleListDependencies / handleCreateDependency / handleDeleteDependency
//     / PredictConflicts.will_unlock). The resolution happened; its result was
//     dropped.
//
// aihub#357's independent review swept the repo by hand and found a fifth
// instance the same day (POST /v1/work_items/:id/unblock, fixed by aihub#362
// alongside this gate). Fixing instances one at a time does not converge;
// this package polices the MECHANISM: every value bound to a work-item-id
// position in SQL must be PROVABLY canonical, and everything that is not
// provable must be explicitly classified by a human, per call site, with a
// reason.
//
// # How a value proves it is canonical
//
// The scanner walks every non-test .go file, finds every static SQL string
// handed to a Query/QueryRow/Exec call (or a registered wrapper), extracts the
// placeholder positions that compare against — or insert into — a
// work-item-id column, maps each position to the Go expression bound to it,
// and requires one of:
//
//   - SELF-RESOLVING SQL: the same statement applies the one id-or-slug rule,
//     `id = $N OR slug = $N` (internal/domain/ids.go), to that placeholder.
//     This is the resolver shape itself.
//   - RESOLVER OUTPUT: the expression is X.ID where X is a parameter typed
//     WorkItem, or a local assigned from a registered resolver
//     (GetWorkItem, ResolveVisibleWorkItemRef, ...), or the direct result of a
//     registered id-returning resolver (resolveRecallWorkItemRef, ...).
//   - A MINTED ID: the expression traces to NewID/newWorkItemID — a fresh
//     canonical id that has never been a slug.
//   - ROW PROVENANCE: the expression is filled by a .Scan(...) whose source
//     query reads the value OUT of a work-item-id column, and every
//     work-item-id placeholder of that source query is itself green. What
//     comes out of work_items(id) — or a column FK-referencing it — is
//     canonical by construction.
//   - A DECLARED CONTRACT: the expression is (or is a field of) a parameter of
//     the enclosing function, and that parameter is registered in
//     canonicalParams as canonical-by-contract. The obligation then moves to
//     every caller of that function, which the gate checks too (see below).
//   - A PER-CALL-SITE EXEMPTION with a written reason (siteExemptions).
//
// Anything else is a violation. Default-deny is the point: the four historical
// instances were all "a shape nobody thought about", and a gate that only
// rejects known-bad shapes goes green on the fifth one.
//
// # The contract layer
//
// canonicalParams moves an obligation, it does not discharge it. For every
// registered (function, parameter), the gate scans every CALL to that function
// in the repo and requires the argument at that position to prove itself
// canonical by the same rules — recursively: a caller may satisfy the check
// with its own registered parameter, so the obligation propagates outward
// until it terminates at a resolver, a minted id, row provenance, or an
// explicit exemption. A registered function referenced as a VALUE (assigned to
// a variable, passed as a callback) must be declared in aliasContracts, or the
// calls through the alias would be invisible.
//
// # Dynamic SQL
//
// SQL assembled at runtime (fmt.Sprintf where-builders and the like) cannot be
// position-mapped statically. The scanner therefore also scans EVERY string
// literal in every non-test file for work-item-id comparison fragments; a
// fragment that is not part of a directly analysed call must be covered by a
// dynamicSQLExemptions entry whose reason records where the value is resolved.
// A dynamic builder is exactly where a read-side instance of this class lived
// (pf_recall's work_item_id filter, aihub#363), so "the scanner cannot see it"
// must never mean "the scanner does not report it".
//
// # What this scanner deliberately does not do
//
// It does not type-check. Loading go/types would make the gate depend on the
// module building, in the DB-free unit step where it needs to run even when
// something else is broken (same stance as internal/citest/rowserr). The cost
// is that recognition is syntactic: a resolver is a CALL TO A NAME, a WorkItem
// is a TYPE THAT SPELLS WorkItem, and value tracing is "the last assignment
// before the use, in source order" — right for the straight-line code this
// repo writes, and pinned both ways by the calibration fixtures in the test
// file. The census floor pins that the recogniser still sees the population it
// polices.
package slugres

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ─── SQL analysis ────────────────────────────────────────────────────────────

// Position is one placeholder in one SQL string that lands in a work-item-id
// column.
type Position struct {
	// Col names the column, table-qualified for work_items.id.
	Col string
	// N is the 1-based placeholder number ($N).
	N int
	// SelfResolving is true when the same statement applies `id = $N OR
	// slug = $N` to this placeholder — the resolver shape itself.
	SelfResolving bool
}

// fkColAlt is the alternation of column names that FK-reference
// work_items(id). These names are unambiguous across the schema: no table
// declares them for any other purpose, so no table attribution is needed.
// work_items.id itself IS ambiguous (`id` names half the tables) and is
// handled by table attribution below.
const fkColAlt = `work_item_id|blocked_wi_id|blocking_wi_id|parent_work_item_id`

var (
	reFKCompare   = regexp.MustCompile(`(?i)(?:\b[a-z_][a-z0-9_]*\.)?\b(` + fkColAlt + `)\s*(?:=|!=|<>)\s*\$([0-9]+)`)
	reFKAny       = regexp.MustCompile(`(?i)(?:\b[a-z_][a-z0-9_]*\.)?\b(` + fkColAlt + `)\s*=\s*ANY\s*\(\s*\$([0-9]+)`)
	reFKIn        = regexp.MustCompile(`(?i)(?:\b[a-z_][a-z0-9_]*\.)?\b(` + fkColAlt + `)\s+IN\s*\(([^)]*)\)`)
	reFKReversed  = regexp.MustCompile(`(?i)\$([0-9]+)\s*(?:=|!=|<>)\s*(?:\b[a-z_][a-z0-9_]*\.)?\b(` + fkColAlt + `)\b`)
	reTableIntro  = regexp.MustCompile(`(?i)\b(?:from|join|update|into)\s+([a-z_][a-z0-9_]*)`)
	reWIAlias     = regexp.MustCompile(`(?i)\bwork_items\s+(?:as\s+)?([a-z_][a-z0-9_]*)`)
	reBareID      = regexp.MustCompile(`(?i)(?:^|[^.\w])id\s*(?:=|!=|<>)\s*\$([0-9]+)`)
	rePlaceholder = regexp.MustCompile(`\$([0-9]+)`)
	reInsert      = regexp.MustCompile(`(?i)insert\s+into\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)\s*values\s*\(`)

	// reDynamicFragment recognises a work-item-id comparison inside ANY string
	// literal, including Sprintf fragments where the placeholder number is
	// `$%d`. This is the net under the position-mapper.
	reDynamicFragment = regexp.MustCompile(`(?i)(?:\b[a-z_][a-z0-9_]*\.)?\b(` + fkColAlt + `)\s*(?:=|!=|<>)\s*(?:ANY\s*\(\s*)?\$`)
)

// sqlKeywords keeps reWIAlias from reading a clause keyword as a table alias
// in `FROM work_items WHERE ...`.
var sqlKeywords = map[string]bool{
	"where": true, "set": true, "on": true, "using": true, "left": true,
	"right": true, "inner": true, "outer": true, "cross": true, "join": true,
	"group": true, "order": true, "limit": true, "offset": true, "for": true,
	"values": true, "returning": true, "and": true, "or": true, "not": true,
	"union": true, "having": true, "window": true, "natural": true,
	"tablesample": true, "as": true,
}

// wiIDColumn reports whether a (lowercased, trimmed) column name is a
// work-item-id column, given the table it belongs to.
func wiIDColumn(col, table string) bool {
	switch col {
	case "work_item_id", "blocked_wi_id", "blocking_wi_id", "parent_work_item_id":
		return true
	case "id":
		return table == "work_items"
	}
	return false
}

// WiPositions extracts every placeholder position in sql that lands in a
// work-item-id column.
func WiPositions(sql string) []Position {
	var out []Position
	add := func(col string, n int) {
		col = strings.ToLower(col)
		for _, p := range out {
			if p.Col == col && p.N == n {
				return
			}
		}
		out = append(out, Position{Col: col, N: n})
	}

	for _, m := range reFKCompare.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[2])
		add(m[1], n)
	}
	for _, m := range reFKAny.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[2])
		add(m[1], n)
	}
	for _, m := range reFKIn.FindAllStringSubmatch(sql, -1) {
		for _, pm := range rePlaceholder.FindAllStringSubmatch(m[2], -1) {
			n, _ := strconv.Atoi(pm[1])
			add(m[1], n)
		}
	}
	for _, m := range reFKReversed.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[1])
		add(m[2], n)
	}

	// work_items.id: qualified by the table name or a declared alias, or bare
	// and attributed to the nearest preceding table-introducing keyword.
	aliases := map[string]bool{"work_items": true}
	for _, m := range reWIAlias.FindAllStringSubmatch(sql, -1) {
		a := strings.ToLower(m[1])
		if !sqlKeywords[a] {
			aliases[a] = true
		}
	}
	for a := range aliases {
		reQual := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a) + `\.id\s*(?:=|!=|<>)\s*\$([0-9]+)`)
		for _, m := range reQual.FindAllStringSubmatch(sql, -1) {
			n, _ := strconv.Atoi(m[1])
			add("work_items.id", n)
		}
	}

	intros := reTableIntro.FindAllStringSubmatchIndex(sql, -1)
	for _, m := range reBareID.FindAllStringSubmatchIndex(sql, -1) {
		table := ""
		for _, in := range intros {
			if in[0] <= m[0] {
				table = strings.ToLower(sql[in[2]:in[3]])
			}
		}
		n, _ := strconv.Atoi(sql[m[2]:m[3]])
		if table == "work_items" {
			add("work_items.id", n)
		} else if table == "" && strings.Contains(strings.ToLower(sql), "work_items") {
			// A bare `id = $N` with no attributable table, in a statement that
			// mentions work_items: refuse to guess. Reported as a position so
			// a human classifies it rather than the scanner waving it through.
			add("work_items.id(unattributed)", n)
		}
	}

	// INSERT column lists: map work-item-id columns to the placeholder in the
	// same tuple position.
	for _, m := range reInsert.FindAllStringSubmatchIndex(sql, -1) {
		table := strings.ToLower(sql[m[2]:m[3]])
		cols := strings.Split(sql[m[4]:m[5]], ",")
		items, multiRow := splitTuple(sql[m[1]:])
		for i, col := range cols {
			col = strings.ToLower(strings.TrimSpace(col))
			if !wiIDColumn(col, table) || i >= len(items) {
				continue
			}
			if pm := rePlaceholder.FindStringSubmatch(items[i]); pm != nil {
				n, _ := strconv.Atoi(pm[1])
				colName := col
				if col == "id" {
					colName = "work_items.id"
				}
				add(colName, n)
			}
		}
		// A multi-row VALUES list over a work-item-id column is a shape the
		// mapper does not model; surface it instead of guessing.
		if multiRow {
			for _, col := range cols {
				col = strings.ToLower(strings.TrimSpace(col))
				if wiIDColumn(col, table) {
					add(col+"(multi-row-values)", 0)
				}
			}
		}
	}

	// Self-resolution: `id = $N OR slug = $N` in either order marks N as the
	// resolver shape.
	for i := range out {
		n := out[i].N
		pat := fmt.Sprintf(`(?i)\bid\s*=\s*\$%d\s+or\s+slug\s*=\s*\$%d(?:[^0-9]|$)`, n, n)
		rev := fmt.Sprintf(`(?i)\bslug\s*=\s*\$%d\s+or\s+id\s*=\s*\$%d(?:[^0-9]|$)`, n, n)
		if regexp.MustCompile(pat).MatchString(sql) || regexp.MustCompile(rev).MatchString(sql) {
			out[i].SelfResolving = true
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N < out[j].N
		}
		return out[i].Col < out[j].Col
	})
	return out
}

// splitTuple splits a parenthesised tuple — the caller passes the string
// starting just AFTER the opening paren — into top-level comma-separated
// items, respecting nested parens and SQL string quoting. The second result
// reports whether another tuple follows (multi-row VALUES).
func splitTuple(s string) (items []string, multiRow bool) {
	depth := 1
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				items = append(items, s[start:i])
				rest := strings.TrimSpace(s[i+1:])
				return items, strings.HasPrefix(rest, ",")
			}
		case ',':
			if depth == 1 {
				items = append(items, s[start:i])
				start = i + 1
			}
		}
	}
	return items, false
}

// selectColumns returns the top-level items of the first SELECT list in sql,
// each lowercased, alias-stripped ("wi2.slug" -> "slug", "id AS x" -> "id").
// Returns nil when sql is not a SELECT the splitter can read.
func selectColumns(sql string) []string {
	low := strings.ToLower(sql)
	sel := strings.Index(low, "select")
	if sel < 0 {
		return nil
	}
	rest := sql[sel+len("select"):]
	restLow := strings.ToLower(rest)
	// Find the matching top-level FROM.
	depth := 0
	from := -1
	for i := 0; i+4 <= len(restLow); i++ {
		switch restLow[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(restLow[i:], "from") &&
			(i == 0 || isSQLSpace(restLow[i-1])) &&
			(i+4 == len(restLow) || isSQLSpace(restLow[i+4])) {
			from = i
			break
		}
	}
	if from < 0 {
		return nil
	}
	var out []string
	for _, item := range splitTopLevel(rest[:from]) {
		item = strings.ToLower(strings.TrimSpace(item))
		if i := strings.Index(item, " as "); i >= 0 {
			item = strings.TrimSpace(item[:i])
		}
		if i := strings.LastIndex(item, "."); i >= 0 && !strings.ContainsAny(item[:i], "() '") {
			item = item[i+1:]
		}
		out = append(out, item)
	}
	return out
}

func isSQLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inStr:
			if c == '\'' {
				inStr = false
			}
		case c == '\'':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// primaryTable returns the first table a statement introduces (FROM x,
// UPDATE x, INSERT INTO x, DELETE FROM x), lowercased.
func primaryTable(sql string) string {
	if m := reTableIntro.FindStringSubmatch(sql); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// ─── Registries (see registry.go for the entries) ────────────────────────────

// Registry tracks which classification entries were used during one census
// run, so stale entries can be rejected.
type Registry struct {
	usedContracts map[string]bool
	usedExempts   map[string]bool
	usedDynamic   map[string]bool
	usedAliases   map[string]bool
}

func NewRegistry() *Registry {
	return &Registry{
		usedContracts: map[string]bool{},
		usedExempts:   map[string]bool{},
		usedDynamic:   map[string]bool{},
		usedAliases:   map[string]bool{},
	}
}

func contractKey(fn, param string) string { return fn + ":" + param }

func (r *Registry) contractFor(fn, param string) *ParamContract {
	for i := range canonicalParams {
		c := &canonicalParams[i]
		if c.Func == fn && c.Param == param {
			r.usedContracts[contractKey(fn, param)] = true
			return c
		}
	}
	return nil
}

// exemptionFor matches a per-call-site exemption. The key is
// file + function + the exact source text of the argument expression + the
// column it lands in — never the file alone. A file-granular exemption is a
// pre-approved hole: aihub#361's first cut was broken exactly that way, and
// this gate inherits the lesson.
func (r *Registry) exemptionFor(file, fn, arg, col string) *SiteExemption {
	for i := range siteExemptions {
		e := &siteExemptions[i]
		if e.File == file && e.Func == fn && e.Arg == arg && e.Col == col {
			r.usedExempts[e.key()] = true
			return e
		}
	}
	return nil
}

func (r *Registry) dynamicFor(file, fn, lit string) *DynamicExemption {
	for i := range dynamicSQLExemptions {
		d := &dynamicSQLExemptions[i]
		if d.File == file && d.Func == fn && strings.Contains(lit, d.Fragment) {
			r.usedDynamic[d.key()] = true
			return d
		}
	}
	return nil
}

func (r *Registry) aliasFor(file, varName string) *AliasContract {
	for i := range aliasContracts {
		a := &aliasContracts[i]
		if a.File == file && a.Alias == varName {
			r.usedAliases[a.key()] = true
			return a
		}
	}
	return nil
}

// ─── The census ──────────────────────────────────────────────────────────────

type fileEntry struct {
	rel  string
	fset *token.FileSet
	file *ast.File
	src  []byte
}

// Census is the result of scanning the repo.
type Census struct {
	Violations []string
	// Positions is the number of work-item-id placeholder positions the
	// scanner mapped in static SQL — the liveness measure.
	Positions int
	// GreenByKind counts how each mapped position proved itself.
	GreenByKind map[string]int
	// DynamicFragments counts string literals carrying a work-item-id
	// comparison that were NOT part of a mapped call.
	DynamicFragments int
	// ContractCalls counts call sites checked under the contract layer.
	ContractCalls int
	FilesWalked   int
}

type constEntry struct {
	fe   *fileEntry
	expr ast.Expr
}

type scanner struct {
	files []*fileEntry
	// pkgConsts indexes package-level const/var string declarations by
	// package directory then name, so a SQL text assembled from consts across
	// files of one package still resolves (foreignLockHolderSQL in
	// internal/domain is exactly that shape).
	pkgConsts map[string]map[string]constEntry
	// consumed marks string literals that were position-mapped as part of a
	// directly analysed call, per file.
	consumed map[*fileEntry]map[token.Pos]bool
	reg      *Registry
	census   *Census
}

// ScanRepo runs the whole census. root is the repo root (the directory with
// go.mod).
func ScanRepo(root string) (*Census, error) {
	s := &scanner{
		pkgConsts: map[string]map[string]constEntry{},
		consumed:  map[*fileEntry]map[token.Pos]bool{},
		reg:       NewRegistry(),
		census:    &Census{GreenByKind: map[string]int{}},
	}
	c := s.census

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", ".git", "node_modules", ".codegraph":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if perr != nil {
			// A parse failure is a violation, never a silent pass.
			c.Violations = append(c.Violations,
				rel+": does not parse: "+perr.Error()+" — a file that cannot be parsed cannot be cleared")
			return nil
		}
		fe := &fileEntry{rel: rel, fset: fset, file: f, src: src}
		s.files = append(s.files, fe)
		s.consumed[fe] = map[token.Pos]bool{}
		c.FilesWalked++

		dir := filepath.ToSlash(filepath.Dir(rel))
		if s.pkgConsts[dir] == nil {
			s.pkgConsts[dir] = map[string]constEntry{}
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, nm := range vs.Names {
					if i < len(vs.Values) {
						s.pkgConsts[dir][nm.Name] = constEntry{fe: fe, expr: vs.Values[i]}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	funcIndex := map[string][]*declSite{}
	for _, fe := range s.files {
		for _, d := range fe.file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				funcIndex[fn.Name.Name] = append(funcIndex[fn.Name.Name], &declSite{fe: fe, fn: fn})
			}
		}
	}

	for _, fe := range s.files {
		s.scanFileSinks(fe)
	}
	for _, fe := range s.files {
		s.scanDynamicFragments(fe)
	}
	s.scanContractCallers(funcIndex)
	s.scanAliasRefs()

	// Stale registry entries: an exemption or contract matching nothing is
	// deleted, not left lying around — it silently covers whatever lands
	// there next (same rule as rowserr's allowlist).
	for i := range canonicalParams {
		e := &canonicalParams[i]
		if !s.reg.usedContracts[contractKey(e.Func, e.Param)] {
			c.Violations = append(c.Violations, "canonicalParams entry "+contractKey(e.Func, e.Param)+
				" matched no argument in the census — the parameter no longer reaches a work-item-id "+
				"position, or the function was renamed. Delete or update the entry; a stale contract "+
				"is an exemption nobody decided to grant.")
		}
	}
	for i := range siteExemptions {
		e := &siteExemptions[i]
		if !s.reg.usedExempts[e.key()] {
			c.Violations = append(c.Violations, "siteExemptions entry "+e.key()+
				" matched no call site — delete it. A stale exemption exempts whatever lands there next.")
		}
	}
	for i := range dynamicSQLExemptions {
		e := &dynamicSQLExemptions[i]
		if !s.reg.usedDynamic[e.key()] {
			c.Violations = append(c.Violations, "dynamicSQLExemptions entry "+e.key()+
				" matched no string literal — delete it, or fix the Fragment so it names the literal it covers.")
		}
	}
	for i := range aliasContracts {
		a := &aliasContracts[i]
		if !s.reg.usedAliases[a.key()] {
			c.Violations = append(c.Violations, "aliasContracts entry "+a.key()+
				" matched no value reference — delete it.")
		}
	}

	sort.Strings(c.Violations)
	return c, nil
}

type declSite struct {
	fe *fileEntry
	fn *ast.FuncDecl
}

// ─── Layer 1: SQL sinks in direct calls ──────────────────────────────────────

// sqlCallMethods are the pgx call shapes whose SQL argument is followed by the
// positional arguments $1 maps onto. sqlWrapperFuncs (registry.go) extends the
// set with this repo's own helpers of the same (…, sql, args...) shape.
var sqlCallMethods = map[string]bool{"Query": true, "QueryRow": true, "Exec": true}

func isSQLCall(call *ast.CallExpr) bool {
	name := calleeName(call)
	return sqlCallMethods[name] || sqlWrapperFuncs[name]
}

// sqlArgOf finds the one argument of a SQL-shaped call that resolves to a
// static string containing work-item-id positions. Wrappers may carry other
// string arguments (bestEffortExec's message); the SQL is the one with
// positions, and TWO such arguments is an ambiguity reported as unmappable
// rather than guessed at.
func (s *scanner) sqlArgOf(fe *fileEntry, fn *ast.FuncDecl, call *ast.CallExpr) (idx int, sql string, lits []token.Pos, ambiguous bool) {
	idx = -1
	for i, a := range call.Args {
		txt, lp, ok := s.resolveStringExpr(fe, fn, a, 0)
		if !ok || len(WiPositions(txt)) == 0 {
			continue
		}
		if idx >= 0 {
			return -1, "", nil, true
		}
		idx, sql, lits = i, txt, lp
	}
	return idx, sql, lits, false
}

func (s *scanner) scanFileSinks(fe *fileEntry) {
	c := s.census
	for _, d := range fe.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnName := funcDisplayName(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSQLCall(call) {
				return true
			}
			sqlIdx, sqlText, lits, ambiguous := s.sqlArgOf(fe, fn, call)
			if ambiguous {
				c.Violations = append(c.Violations, fe.rel+": "+fnName+": a "+calleeName(call)+
					" call carries TWO static strings with work-item-id positions; the scanner "+
					"cannot tell which is the SQL. Restructure the call.")
				return true
			}
			if sqlIdx < 0 {
				return true
			}
			for _, lp := range lits {
				s.consumed[fe][lp] = true
			}
			for _, p := range WiPositions(sqlText) {
				c.Positions++
				if p.SelfResolving {
					c.GreenByKind["self-resolving"]++
					continue
				}
				argIdx := sqlIdx + p.N
				if call.Ellipsis.IsValid() || argIdx >= len(call.Args) {
					s.handleSinkVerdict(fe, fn, fnName, p, "<unmappable: variadic or missing argument>",
						verdict{kind: vUnmappable})
					continue
				}
				arg := call.Args[argIdx]
				v := s.classifyExpr(fe, fn, arg, 0)
				s.handleSinkVerdict(fe, fn, fnName, p, exprText(arg), v)
			}
			return true
		})
	}
}

// scanDynamicFragments is the net under the position-mapper: any string
// literal with a work-item-id comparison that no analysed call consumed needs
// a dynamicSQLExemptions entry.
func (s *scanner) scanDynamicFragments(fe *fileEntry) {
	c := s.census
	// The gate's own package is excluded from the DYNAMIC net only: the
	// exemption tables in registry.go must QUOTE the fragments they cover, so
	// scanning them re-reports every exemption as a fresh violation. The sink
	// analysis above still applies to this package (real SQL here would be
	// caught), the package holds no database code by construction, and the
	// exclusion is this one directory, stated here rather than discovered.
	if strings.HasPrefix(fe.rel, "internal/citest/slugres/") {
		return
	}
	ast.Inspect(fe.file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || s.consumed[fe][lit.Pos()] {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil || !reDynamicFragment.MatchString(val) {
			return true
		}
		c.DynamicFragments++
		fnName := enclosingFuncName(fe.file, lit.Pos())
		if s.reg.dynamicFor(fe.rel, fnName, val) != nil {
			c.GreenByKind["dynamic-exempt"]++
			return true
		}
		c.Violations = append(c.Violations, fe.rel+": "+fnName+": a string literal carries a "+
			"work-item-id comparison the scanner could not position-map (dynamic SQL): "+
			compactLit(val)+". Verify where the bound value is resolved and add a "+
			"dynamicSQLExemptions entry naming this literal and that place, or restructure the "+
			"query so the scanner can map it. Dynamic where-builders are exactly where the "+
			"read-side instance of this class lived (aihub#363).")
		return true
	})
}

type verdictKind int

const (
	vViolation verdictKind = iota
	vResolved
	vMinted
	vRowSourced
	vSentinel   // the empty-string "no work item" sentinel
	vParam      // param or param field; needs a contract
	vUnmappable // could not bind the placeholder to an expression
)

type verdict struct {
	kind      verdictKind
	paramExpr string // for vParam: "wiID" or "req.BlockedWIID"
	why       string
}

func (s *scanner) handleSinkVerdict(fe *fileEntry, fn *ast.FuncDecl, fnName string, p Position, argText string, v verdict) {
	c := s.census
	col := p.Col
	switch v.kind {
	case vResolved:
		c.GreenByKind["resolver"]++
	case vMinted:
		c.GreenByKind["minted"]++
	case vRowSourced:
		c.GreenByKind["row-sourced"]++
	case vSentinel:
		c.GreenByKind["sentinel"]++
	case vParam:
		if s.reg.contractFor(fn.Name.Name, v.paramExpr) != nil {
			c.GreenByKind["contract"]++
			return
		}
		if s.reg.exemptionFor(fe.rel, fnName, argText, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		c.Violations = append(c.Violations, fmt.Sprintf(
			"%s: %s binds %s ($%d -> %s), which is a parameter of this function that carries no "+
				"declared contract. If every caller passes a canonical id, register it in "+
				"canonicalParams (the gate will then hold every caller to that); if callers may pass "+
				"an id-or-slug, resolve it here first (domain.GetWorkItem / the `id = $N OR slug = $N` "+
				"shape). Passing it through unresolved is the aihub#127/#343/#357 class.",
			fe.rel, fnName, argText, p.N, col))
	case vUnmappable:
		if s.reg.exemptionFor(fe.rel, fnName, argText, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		c.Violations = append(c.Violations, fmt.Sprintf(
			"%s: %s: could not map $%d (%s) to an argument expression (%s). Restructure the call so "+
				"the scanner can see the binding, or add a siteExemptions entry with the reason.",
			fe.rel, fnName, p.N, col, argText))
	default:
		if s.reg.exemptionFor(fe.rel, fnName, argText, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		why := v.why
		if why != "" {
			why = " (" + why + ")"
		}
		c.Violations = append(c.Violations, fmt.Sprintf(
			"%s: %s binds %s to $%d, which lands in %s — a position that only ever holds a canonical "+
				"work_items(id) — and the value is not provably canonical%s. A slug here either trips "+
				"the FK (write) or silently matches nothing (read). Resolve the reference first "+
				"(domain.GetWorkItem and pass .ID, or the `id = $N OR slug = $N` SQL shape), or if the "+
				"value is canonical for a reason the scanner cannot see, add a siteExemptions entry "+
				"with that reason (aihub#362; class history aihub#127/#343/#357).",
			fe.rel, fnName, argText, p.N, col, why))
	}
}

// ─── Expression classification ───────────────────────────────────────────────

const maxTraceDepth = 6

// classifyExpr decides whether e provably carries a canonical work-item id,
// within the enclosing function only — crossing function boundaries is what
// canonicalParams is for.
func (s *scanner) classifyExpr(fe *fileEntry, fn *ast.FuncDecl, e ast.Expr, depth int) verdict {
	if depth > maxTraceDepth {
		return verdict{kind: vViolation, why: "trace deeper than the scanner follows"}
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.classifyExpr(fe, fn, x.X, depth+1)
	case *ast.StarExpr:
		return s.classifyExpr(fe, fn, x.X, depth+1)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return s.classifyExpr(fe, fn, x.X, depth+1)
		}
		return verdict{kind: vViolation, why: "expression shape the scanner cannot trace"}
	case *ast.SelectorExpr:
		return s.classifySelector(fe, fn, x, depth)
	case *ast.Ident:
		return s.classifyIdent(fe, fn, x, depth)
	case *ast.CallExpr:
		name := calleeName(x)
		switch {
		case idMinters[name]:
			return verdict{kind: vMinted}
		case idResolvers[name]:
			return verdict{kind: vResolved}
		default:
			return verdict{kind: vViolation, why: "the value is the result of " + describeCallee(name) +
				", which is not a registered resolver — wrapping a raw reference does not resolve it"}
		}
	case *ast.BasicLit:
		// The empty string is the "no work item" sentinel (an exclusion filter
		// left open, a nullable column left unset). It cannot be a slug and it
		// matches no row, so it is green; any NON-empty literal is a hardcoded
		// reference and stays a violation.
		if x.Kind == token.STRING && x.Value == `""` {
			return verdict{kind: vSentinel}
		}
		return verdict{kind: vViolation, why: "a hardcoded literal"}
	default:
		return verdict{kind: vViolation, why: "expression shape the scanner cannot trace"}
	}
}

func (s *scanner) classifySelector(fe *fileEntry, fn *ast.FuncDecl, sel *ast.SelectorExpr, depth int) verdict {
	base, ok := sel.X.(*ast.Ident)
	if !ok {
		return verdict{kind: vViolation, why: "selector over a non-identifier base"}
	}
	selText := base.Name + "." + sel.Sel.Name

	// A field of a parameter may have been overwritten with a resolved value
	// before the use (Remember does exactly this: req.WorkItemID = &resolved).
	// The LAST write before the use, in source order, is what reaches the
	// sink; no write at all falls through to the param-contract path.
	if isParamName(fn, base.Name) {
		if rhs, n := lastFieldWriteBefore(fn, base.Name, sel.Sel.Name, sel.Pos()); n > 0 {
			if rhs == nil {
				return verdict{kind: vViolation, why: selText + " is written by an assignment shape " +
					"the scanner cannot trace"}
			}
			return s.classifyExpr(fe, fn, rhs, depth+1)
		}
	}

	if sel.Sel.Name == "ID" {
		if isWorkItemTypedName(fn, base.Name) || isWorkItemRangeVar(fn, base.Name) {
			return verdict{kind: vResolved}
		}
		if s.tracedToResolver(fe, fn, base) {
			return verdict{kind: vResolved}
		}
		if s.scanProvenance(fe, fn, selText, sel.Pos(), depth) {
			return verdict{kind: vRowSourced}
		}
		if isParamName(fn, base.Name) {
			return verdict{kind: vParam, paramExpr: selText}
		}
		return verdict{kind: vViolation, why: selText + " where " + base.Name +
			" is not a WorkItem parameter, is not traceable to a registered resolver, and is not " +
			"filled from a work-item-id column by a row scan"}
	}
	if s.scanProvenance(fe, fn, selText, sel.Pos(), depth) {
		return verdict{kind: vRowSourced}
	}
	if isParamName(fn, base.Name) {
		return verdict{kind: vParam, paramExpr: selText}
	}
	return verdict{kind: vViolation, why: "field " + selText + " of a value the scanner cannot trace"}
}

func (s *scanner) classifyIdent(fe *fileEntry, fn *ast.FuncDecl, id *ast.Ident, depth int) verdict {
	if depth > maxTraceDepth {
		return verdict{kind: vViolation, why: "assignment chain deeper than the scanner traces"}
	}
	if isParamName(fn, id.Name) {
		return verdict{kind: vParam, paramExpr: id.Name}
	}
	if isWorkItemRangeVar(fn, id.Name) {
		// The range variable over a []*WorkItem parameter: each element is a
		// loaded WorkItem, same standing as a WorkItem parameter. (Used via
		// .ID selectors; reaching here means the ident itself is bound, which
		// no current site does, but the classification is the same.)
		return verdict{kind: vResolved}
	}
	if coll := rangeCollectionOf(fn, id); coll != nil {
		// A range VALUE variable is as canonical as the collection's elements:
		// classify the collection, whose own trace ends at the append that
		// built it (and from there at whatever was appended), at a parameter,
		// or at a row scan.
		return s.classifyExpr(fe, fn, coll, depth+1)
	}
	rhs, count := lastAssignmentBefore(fn, id)
	if count == 0 {
		if s.scanProvenance(fe, fn, id.Name, id.Pos(), depth) {
			return verdict{kind: vRowSourced}
		}
		return verdict{kind: vViolation, why: id.Name + " is never assigned in any enclosing block " +
			"and is not filled from a work-item-id column by a row scan"}
	}
	if rhs == nil {
		return verdict{kind: vViolation, why: id.Name + " is last written by an assignment shape " +
			"the scanner cannot trace (a later value of a multi-value call)"}
	}
	switch r := rhs.(type) {
	case *ast.CallExpr:
		name := calleeName(r)
		switch {
		case idMinters[name]:
			return verdict{kind: vMinted}
		case idResolvers[name]:
			return verdict{kind: vResolved}
		case name == "append":
			// ids = append(ids, X): the collection is as canonical as what is
			// appended to it.
			if len(r.Args) == 2 {
				return s.classifyExpr(fe, fn, r.Args[1], depth+1)
			}
			return verdict{kind: vViolation, why: id.Name + " is built by an append the scanner " +
				"cannot trace element-wise"}
		default:
			return verdict{kind: vViolation, why: id.Name + " is assigned from " + describeCallee(name) +
				", which is not a registered resolver"}
		}
	case *ast.Ident:
		return s.classifyIdent(fe, fn, r, depth+1)
	case *ast.SelectorExpr:
		return s.classifySelector(fe, fn, r, depth+1)
	case *ast.UnaryExpr:
		if r.Op == token.AND {
			return s.classifyExpr(fe, fn, r.X, depth+1)
		}
		return verdict{kind: vViolation, why: id.Name + " is assigned from an expression the scanner cannot trace"}
	case *ast.StarExpr:
		return s.classifyExpr(fe, fn, r.X, depth+1)
	default:
		return verdict{kind: vViolation, why: id.Name + " is assigned from an expression the scanner cannot trace"}
	}
}

// tracedToResolver reports whether base's last assignment before its use is a
// call to a registered work-item resolver (first result).
func (s *scanner) tracedToResolver(fe *fileEntry, fn *ast.FuncDecl, base *ast.Ident) bool {
	rhs, count := lastAssignmentBefore(fn, base)
	if count == 0 || rhs == nil {
		return false
	}
	call, ok := rhs.(*ast.CallExpr)
	if !ok {
		return false
	}
	return wiResolvers[calleeName(call)]
}

// lastAssignmentBefore finds the assignments to id.Name in the innermost
// enclosing block that assigns it at all (nested blocks included), and
// returns the RHS of the LAST one before id's own position, plus the number
// of assignments seen in that scope.
//
// "Last before the use, in source order" is the load-bearing choice. The
// repo's resolve-then-overwrite pattern —
//
//	wiID := c.Param("id")
//	wi, err := domain.GetWorkItem(ctx, pool, wiID)
//	wiID = wi.ID
//
// — is CORRECT code (the raw value's only consumer is the resolver), and a
// gate that rejects it gets its verdicts deleted, not its rule obeyed. The
// mirror direction stays red: overwriting a resolved value with a raw one
// makes the raw assignment the last one, and the fixtures pin both.
//
// The known cost: an assignment AFTER the use that loops back around (a raw
// re-assignment at the bottom of a loop feeding the next iteration's sink) is
// invisible. No current site loops a work-item ref that way; the census
// floor and the default-deny on everything untraceable bound the exposure.
//
// For `a, b := f()` the matching value is the call itself only when the name
// is the FIRST result; a later name of a multi-value call yields a nil RHS,
// which every caller treats as untraceable.
func lastAssignmentBefore(fn *ast.FuncDecl, id *ast.Ident) (ast.Expr, int) {
	chain := enclosingBlockChain(fn, id.Pos())
	for _, blk := range chain {
		type write struct {
			pos token.Pos
			rhs ast.Expr
		}
		var writes []write
		ast.Inspect(blk, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, l := range as.Lhs {
				lid, ok := l.(*ast.Ident)
				if !ok || lid.Name != id.Name {
					continue
				}
				switch {
				case len(as.Rhs) == len(as.Lhs):
					writes = append(writes, write{as.Pos(), as.Rhs[i]})
				case len(as.Rhs) == 1 && i == 0:
					writes = append(writes, write{as.Pos(), as.Rhs[0]})
				default:
					writes = append(writes, write{as.Pos(), nil})
				}
			}
			return true
		})
		if len(writes) == 0 {
			continue
		}
		var last *write
		for i := range writes {
			w := &writes[i]
			if w.pos < id.Pos() && (last == nil || w.pos > last.pos) {
				last = w
			}
		}
		if last == nil {
			// Assigned only after the use: untraceable.
			return nil, len(writes)
		}
		return last.rhs, len(writes)
	}
	return nil, 0
}

// lastFieldWriteBefore is lastAssignmentBefore for `base.Field = ...` writes.
func lastFieldWriteBefore(fn *ast.FuncDecl, baseName, field string, usePos token.Pos) (ast.Expr, int) {
	var last ast.Expr
	var lastPos token.Pos
	count := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			sel, ok := l.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != field {
				continue
			}
			b, ok := sel.X.(*ast.Ident)
			if !ok || b.Name != baseName {
				continue
			}
			count++
			if as.Pos() >= usePos {
				continue
			}
			if as.Pos() > lastPos {
				lastPos = as.Pos()
				if len(as.Rhs) == len(as.Lhs) {
					last = as.Rhs[i]
				} else {
					last = nil
				}
			}
		}
		return true
	})
	return last, count
}

// scanProvenance reports whether the expression named by exprStr is filled,
// before usePos, by exactly one row scan that reads it OUT of a work-item-id
// column — and whose source query's own work-item-id placeholders are all
// green. What comes out of work_items(id), or a column FK-referencing it, is
// canonical by construction; what makes that trustworthy is that the query
// it came from was keyed canonically too (or resolved id-or-slug itself).
func (s *scanner) scanProvenance(fe *fileEntry, fn *ast.FuncDecl, exprStr string, usePos token.Pos, depth int) bool {
	if depth > maxTraceDepth {
		return false
	}
	matches := 0
	allGood := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Scan" || call.Pos() >= usePos {
			return true
		}
		argPos := -1
		for i, a := range call.Args {
			ue, ok := a.(*ast.UnaryExpr)
			if !ok || ue.Op != token.AND {
				continue
			}
			if exprText(ue.X) == exprStr {
				argPos = i
				break
			}
		}
		if argPos < 0 {
			return true
		}
		matches++

		// Where do the scanned rows come from?
		var srcCall *ast.CallExpr
		switch recv := sel.X.(type) {
		case *ast.CallExpr:
			// pool.QueryRow(...).Scan(...)
			srcCall = recv
		case *ast.Ident:
			// rows.Scan(...) — trace rows to its Query call.
			if rhs, cnt := lastAssignmentBefore(fn, recv); cnt > 0 && rhs != nil {
				if rc, ok := rhs.(*ast.CallExpr); ok {
					srcCall = rc
				}
			}
		}
		if srcCall == nil || !sqlCallMethods[calleeName(srcCall)] {
			allGood = false
			return true
		}
		sqlIdx, sqlText, _, ambiguous := s.sqlArgOf(fe, fn, srcCall)
		if ambiguous || sqlIdx < 0 {
			// The source SQL may simply carry no work-item-id positions
			// (sqlArgOf only finds strings WITH positions) — re-scan for any
			// static string at all, since the SELECT list still matters.
			sqlIdx, sqlText = -1, ""
			for i, a := range srcCall.Args {
				if txt, _, ok := s.resolveStringExpr(fe, fn, a, 0); ok {
					sqlIdx, sqlText = i, txt
					break
				}
			}
			if sqlIdx < 0 {
				allGood = false
				return true
			}
		}

		// The scanned column at this position must be a work-item-id column.
		cols := selectColumns(sqlText)
		table := primaryTable(sqlText)
		if argPos >= len(cols) || !wiIDColumn(strings.TrimSpace(cols[argPos]), table) {
			allGood = false
			return true
		}

		// And the source query's own work-item-id placeholders must be green.
		for _, p := range WiPositions(sqlText) {
			if p.SelfResolving {
				continue
			}
			argIdx := sqlIdx + p.N
			if srcCall.Ellipsis.IsValid() || argIdx >= len(srcCall.Args) {
				allGood = false
				return true
			}
			v := s.classifyExpr(fe, fn, srcCall.Args[argIdx], depth+1)
			switch v.kind {
			case vResolved, vMinted, vRowSourced, vSentinel:
			case vParam:
				if s.reg.contractFor(fn.Name.Name, v.paramExpr) == nil {
					allGood = false
					return true
				}
			default:
				allGood = false
				return true
			}
		}
		return true
	})
	return matches == 1 && allGood
}

// ─── Layer 2: callers of contract functions ──────────────────────────────────

func (s *scanner) scanContractCallers(funcIndex map[string][]*declSite) {
	c := s.census
	type target struct {
		contract *ParamContract
		paramIdx int
		field    string // non-empty for "req.Field" contracts
	}

	// scannedRels lets a fixture run (a scanner over a few synthetic files)
	// skip registry entries whose anchor file is simply not in the run. In the
	// whole-repo run every file is walked, so a missing anchor there is real
	// rot and is still reported.
	scannedRels := map[string]bool{}
	for _, fe := range s.files {
		scannedRels[fe.rel] = true
	}

	byFunc := map[string][]target{}
	for i := range canonicalParams {
		ct := &canonicalParams[i]
		base, field := ct.Param, ""
		if dot := strings.IndexByte(ct.Param, '.'); dot >= 0 {
			base, field = ct.Param[:dot], ct.Param[dot+1:]
		}
		if !scannedRels[ct.File] {
			continue
		}
		var decl *declSite
		for _, d := range funcIndex[ct.Func] {
			if d.fe.rel == ct.File {
				decl = d
				break
			}
		}
		if decl == nil {
			c.Violations = append(c.Violations, "canonicalParams entry "+contractKey(ct.Func, ct.Param)+
				" anchors to "+ct.File+", which declares no function named "+ct.Func+
				". The contract points at nothing; move or delete it.")
			continue
		}
		idx := paramIndex(decl.fn, base)
		if idx < 0 {
			c.Violations = append(c.Violations, "canonicalParams entry "+contractKey(ct.Func, ct.Param)+
				": "+ct.Func+" has no parameter named "+base+". The contract points at nothing; fix it.")
			continue
		}
		byFunc[ct.Func] = append(byFunc[ct.Func], target{contract: ct, paramIdx: idx, field: field})
	}

	// Alias contracts inherit the targets of the function they alias.
	for i := range aliasContracts {
		a := &aliasContracts[i]
		if !scannedRels[a.File] {
			continue
		}
		if ts, ok := byFunc[a.Aliases]; ok {
			byFunc[a.Alias] = append(byFunc[a.Alias], ts...)
		} else {
			c.Violations = append(c.Violations, "aliasContracts entry "+a.key()+
				" aliases "+a.Aliases+", which has no canonicalParams entries — delete the alias entry.")
		}
	}

	for _, fe := range s.files {
		for _, d := range fe.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fnName := funcDisplayName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				targets, ok := byFunc[calleeName(call)]
				if !ok {
					return true
				}
				for _, tg := range targets {
					c.ContractCalls++
					s.checkContractArg(fe, fn, fnName, call, tg.contract, tg.paramIdx, tg.field)
				}
				return true
			})
		}
	}
}

func (s *scanner) checkContractArg(fe *fileEntry, fn *ast.FuncDecl, fnName string, call *ast.CallExpr, ct *ParamContract, paramIdx int, field string) {
	c := s.census
	col := "call:" + ct.Func + "." + ct.Param
	if paramIdx >= len(call.Args) {
		// A same-named function of a different arity — pkg/client's HTTP
		// methods share names with the domain functions they front. The
		// mismatch is exemptible per site, keyed on the arity so the exemption
		// stops matching if the call changes shape.
		arityKey := fmt.Sprintf("<call with %d args>", len(call.Args))
		if s.reg.exemptionFor(fe.rel, fnName, arityKey, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		c.Violations = append(c.Violations, fe.rel+": "+fnName+" calls "+ct.Func+" with fewer "+
			"arguments than its contract position — a same-named function the scanner cannot tell "+
			"apart. Rename one, or add a siteExemptions entry with Arg \""+arityKey+"\" and Col \""+col+"\".")
		return
	}
	arg := call.Args[paramIdx]

	var v verdict
	var argText string
	if field == "" {
		argText = exprText(arg)
		v = s.classifyExpr(fe, fn, arg, 0)
	} else {
		argText, v = s.classifyStructField(fe, fn, call, arg, field)
	}

	switch v.kind {
	case vResolved:
		c.GreenByKind["resolver"]++
	case vMinted:
		c.GreenByKind["minted"]++
	case vRowSourced:
		c.GreenByKind["row-sourced"]++
	case vSentinel:
		c.GreenByKind["sentinel"]++
	case vParam:
		if s.reg.contractFor(fn.Name.Name, v.paramExpr) != nil {
			c.GreenByKind["contract"]++
			return
		}
		if s.reg.exemptionFor(fe.rel, fnName, argText, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		c.Violations = append(c.Violations, fmt.Sprintf(
			"%s: %s passes %s to %s (parameter %s is canonical by contract: %s), and the value is a "+
				"parameter of %s that carries no contract of its own. Register it in canonicalParams "+
				"or resolve before the call.",
			fe.rel, fnName, argText, ct.Func, ct.Param, ct.Reason, fnName))
	default:
		if s.reg.exemptionFor(fe.rel, fnName, argText, col) != nil {
			c.GreenByKind["exempt"]++
			return
		}
		why := v.why
		if why != "" {
			why = " (" + why + ")"
		}
		c.Violations = append(c.Violations, fmt.Sprintf(
			"%s: %s passes %s to %s, whose parameter %s is canonical by contract (%s), and the value "+
				"is not provably canonical%s. This is the aihub#357 shape: a caller resolves the "+
				"reference for its own checks and then hands the callee the ORIGINAL parameter. Pass "+
				"the resolved .ID.",
			fe.rel, fnName, argText, ct.Func, ct.Param, ct.Reason, why))
	}
}

// classifyStructField handles a contract on a request-struct field
// ("req.BlockedWIID"): the argument is &req / req / a composite literal, and
// the LAST write to that field before the call must prove canonical.
func (s *scanner) classifyStructField(fe *fileEntry, fn *ast.FuncDecl, call *ast.CallExpr, arg ast.Expr, field string) (string, verdict) {
	if ue, ok := arg.(*ast.UnaryExpr); ok && ue.Op == token.AND {
		arg = ue.X
	}
	switch a := arg.(type) {
	case *ast.CompositeLit:
		for _, el := range a.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
				return exprText(kv.Value), s.classifyExpr(fe, fn, kv.Value, 0)
			}
		}
		return "<composite literal without " + field + ">", verdict{kind: vViolation,
			why: "the composite literal never sets " + field + ", so the zero value (or whatever the caller sent) is what reaches the column"}
	case *ast.Ident:
		if rhs, n := lastFieldWriteBefore(fn, a.Name, field, call.Pos()); n > 0 {
			if rhs == nil {
				return a.Name + "." + field, verdict{kind: vViolation,
					why: "written by an assignment shape the scanner cannot trace"}
			}
			return a.Name + "." + field + " = " + exprText(rhs), s.classifyExpr(fe, fn, rhs, 0)
		}
		if init := compositeInitOf(fn, a.Name); init != nil {
			for _, el := range init.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
					return exprText(kv.Value), s.classifyExpr(fe, fn, kv.Value, 0)
				}
			}
		}
		return a.Name + "." + field, verdict{kind: vViolation,
			why: "no write to " + a.Name + "." + field + " before the call is visible to the scanner, " +
				"so the field still holds whatever the caller sent (Bind) or the zero value"}
	default:
		return exprText(arg), verdict{kind: vViolation, why: "argument shape the scanner cannot trace to a struct"}
	}
}

// compositeInitOf finds `name := T{...}` / `name := &T{...}` in fn.
func compositeInitOf(fn *ast.FuncDecl, name string) *ast.CompositeLit {
	var out *ast.CompositeLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Name != name || i >= len(as.Rhs) {
				continue
			}
			if cl, ok := as.Rhs[i].(*ast.CompositeLit); ok {
				out = cl
			}
			if ue, ok := as.Rhs[i].(*ast.UnaryExpr); ok && ue.Op == token.AND {
				if cl, ok := ue.X.(*ast.CompositeLit); ok {
					out = cl
				}
			}
		}
		return true
	})
	return out
}

// scanAliasRefs rejects contract functions referenced as values (assigned to
// variables, passed as callbacks) unless the alias is declared: calls through
// an undeclared alias would be invisible to the contract layer.
func (s *scanner) scanAliasRefs() {
	c := s.census
	contractFuncs := map[string]bool{}
	for i := range canonicalParams {
		contractFuncs[canonicalParams[i].Func] = true
	}
	for _, fe := range s.files {
		var stack []ast.Node
		ast.Inspect(fe.file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, n) }()
			var name string
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if contractFuncs[x.Sel.Name] {
					name = x.Sel.Name
				}
			case *ast.Ident:
				if !contractFuncs[x.Name] {
					return true
				}
				if len(stack) > 0 {
					switch p := stack[len(stack)-1].(type) {
					case *ast.SelectorExpr:
						return true // the Sel or base of a selector; handled at the selector
					case *ast.FuncDecl:
						if p.Name == x {
							return true // the declaration itself
						}
					}
				}
				name = x.Name
			default:
				return true
			}
			if name == "" {
				return true
			}
			if len(stack) > 0 {
				if callp, ok := stack[len(stack)-1].(*ast.CallExpr); ok && callp.Fun == n {
					return true // a direct call; layer 2 checks it
				}
			}
			varName := ""
			if len(stack) > 0 {
				switch p := stack[len(stack)-1].(type) {
				case *ast.AssignStmt:
					for i, r := range p.Rhs {
						if r == n && i < len(p.Lhs) {
							if id, ok := p.Lhs[i].(*ast.Ident); ok {
								varName = id.Name
							}
						}
					}
				case *ast.ValueSpec:
					for i, r := range p.Values {
						if r == n && i < len(p.Names) {
							varName = p.Names[i].Name
						}
					}
				}
			}
			if varName != "" && s.reg.aliasFor(fe.rel, varName) != nil {
				return true
			}
			c.Violations = append(c.Violations, fe.rel+": "+name+" (a function with a canonicalParams "+
				"contract) is referenced as a VALUE"+describeAliasTarget(varName)+". Calls through an "+
				"alias are invisible to the contract check; declare it in aliasContracts so those calls "+
				"are checked too, or call the function directly.")
			return true
		})
	}
}

func describeAliasTarget(varName string) string {
	if varName == "" {
		return " (not assigned to a nameable variable)"
	}
	return " (assigned to " + varName + ")"
}

// ─── Shared AST helpers ──────────────────────────────────────────────────────

func isParamName(fn *ast.FuncDecl, name string) bool {
	for _, fl := range []*ast.FieldList{fn.Recv, fn.Type.Params} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			for _, nm := range f.Names {
				if nm.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// isWorkItemTypedName reports whether name is a parameter (or receiver) of fn
// whose type spells a loaded work-item struct. A loaded WorkItem's ID field is
// canonical by construction: the id column only ever holds a `wi_...`.
func isWorkItemTypedName(fn *ast.FuncDecl, name string) bool {
	for _, fl := range []*ast.FieldList{fn.Recv, fn.Type.Params} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			if !typeSpellsWorkItem(f.Type) {
				continue
			}
			for _, nm := range f.Names {
				if nm.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// rangeCollectionOf returns the collection expression when id is the VALUE
// variable of a `for _, id := range coll` statement enclosing id's position.
func rangeCollectionOf(fn *ast.FuncDecl, id *ast.Ident) ast.Expr {
	var out ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		v, ok := rs.Value.(*ast.Ident)
		if !ok || v.Name != id.Name {
			return true
		}
		if rs.Pos() <= id.Pos() && id.Pos() <= rs.End() {
			out = rs.X
		}
		return true
	})
	return out
}

// isWorkItemRangeVar reports whether name is the value variable of a `range`
// over a parameter whose type spells []*WorkItem (or []WorkItem).
func isWorkItemRangeVar(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		v, ok := rs.Value.(*ast.Ident)
		if !ok || v.Name != name {
			return true
		}
		x, ok := rs.X.(*ast.Ident)
		if !ok {
			return true
		}
		for _, f := range fn.Type.Params.List {
			if !typeSpellsWorkItem(f.Type) {
				continue
			}
			for _, nm := range f.Names {
				if nm.Name == x.Name {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// typeSpellsWorkItem strips slices, pointers and package qualifiers and
// accepts the loaded work-item shapes.
func typeSpellsWorkItem(t ast.Expr) bool {
	s := exprText(t)
	for {
		switch {
		case strings.HasPrefix(s, "[]"):
			s = s[2:]
		case strings.HasPrefix(s, "*"):
			s = s[1:]
		default:
			if i := strings.LastIndex(s, "."); i >= 0 {
				s = s[i+1:]
			}
			return s == "WorkItem" || s == "VisibleWorkItemRef"
		}
	}
}

func paramIndex(fn *ast.FuncDecl, name string) int {
	if fn.Type.Params == nil {
		return -1
	}
	idx := 0
	for _, f := range fn.Type.Params.List {
		if len(f.Names) == 0 {
			idx++
			continue
		}
		for _, nm := range f.Names {
			if nm.Name == name {
				return idx
			}
			idx++
		}
	}
	return -1
}

func funcDisplayName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(" + exprText(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

func enclosingFuncName(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Pos() <= pos && pos <= fn.End() {
			return funcDisplayName(fn)
		}
	}
	return "<file scope>"
}

// enclosingBlockChain returns every BlockStmt of fn containing pos, innermost
// first. A position outside every block (a file-scope reference reached
// through const resolution) gets the function body as a fallback so lookups
// still terminate.
func enclosingBlockChain(fn *ast.FuncDecl, pos token.Pos) []*ast.BlockStmt {
	var chain []*ast.BlockStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok && b.Pos() <= pos && pos <= b.End() {
			chain = append(chain, b)
		}
		return true
	})
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	if len(chain) == 0 {
		chain = append(chain, fn.Body)
	}
	return chain
}

// resolveStringExpr resolves e to a compile-time string when possible: a
// string literal, a `+` concatenation of resolvable parts, a local assigned
// exactly once from a resolvable string, or a package-level const/var — from
// ANY file of the same package (foreignLockHolderSQL concatenates a const
// declared two files away). Returns the string, the literal positions
// consumed (only those in fe's own file matter to the caller), and ok.
func (s *scanner) resolveStringExpr(fe *fileEntry, fn *ast.FuncDecl, e ast.Expr, depth int) (string, []token.Pos, bool) {
	if depth > 12 {
		return "", nil, false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", nil, false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return "", nil, false
		}
		return v, []token.Pos{x.Pos()}, true
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", nil, false
		}
		l, lp, lok := s.resolveStringExpr(fe, fn, x.X, depth+1)
		r, rp, rok := s.resolveStringExpr(fe, fn, x.Y, depth+1)
		if !lok || !rok {
			return "", nil, false
		}
		return l + r, append(lp, rp...), true
	case *ast.ParenExpr:
		return s.resolveStringExpr(fe, fn, x.X, depth+1)
	case *ast.Ident:
		if fn != nil && !isParamName(fn, x.Name) {
			if rhs, count := lastAssignmentBefore(fn, x); count == 1 && rhs != nil {
				if v, lp, ok := s.resolveStringExpr(fe, fn, rhs, depth+1); ok {
					return v, lp, true
				}
			}
		}
		dir := filepath.ToSlash(filepath.Dir(fe.rel))
		if ce, ok := s.pkgConsts[dir][x.Name]; ok {
			v, lp, ok2 := s.resolveStringExpr(ce.fe, nil, ce.expr, depth+1)
			if !ok2 {
				return "", nil, false
			}
			if ce.fe != fe {
				// Literal positions in another file: mark them consumed there,
				// so the dynamic-fragment net does not double-report SQL that
				// WAS analysed.
				for _, p := range lp {
					s.consumed[ce.fe][p] = true
				}
				lp = nil
			}
			return v, lp, true
		}
		return "", nil, false
	default:
		return "", nil, false
	}
}

func calleeName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

func describeCallee(name string) string {
	if name == "" {
		return "a call the scanner cannot name (through a variable, or a func literal)"
	}
	return name + "(...)"
}

func exprText(e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), e); err != nil {
		// Not "" — an unprintable expression must not compare equal to an
		// exemption entry, or it would exempt itself.
		return "<unprintable expression>"
	}
	return buf.String()
}

func compactLit(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return "`" + s + "`"
}
