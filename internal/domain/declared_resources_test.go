package domain

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// aihub#238: a declared_resources entry whose `type` resourceToLock does not
// recognize used to be silently skipped by all four call sites, so the wi
// acquired NO locks and pf_predict_conflicts returned a fake all-clear
// (predictions:[]). Unrecognized input must now be rejected loudly on the
// caller-supplied paths.

// The exact mistake from the report: `file_scope` is a *lock* type, not a
// *declared* type, and the field is `uri`, not `value`. Both halves were silent.
func TestValidateDeclaredResources_RejectsLockTypeUsedAsDeclaredType(t *testing.T) {
	raw := json.RawMessage(`[{"type":"file_scope","value":"tether:internal/session/attach.go"}]`)
	err := ValidateDeclaredResources(raw)
	if err == nil {
		t.Fatal("ValidateDeclaredResources accepted type=file_scope — this is the aihub#238 silent-no-lock bug")
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400 (must not surface as 500)", err.HTTPStatus)
	}
}

// The error must name the offending value AND enumerate the legal ones. The
// report called out that the old 500 was actively misleading ("it made me think
// file_scope was disallowed, when the constraint does list file_scope — the
// empty field was resource_type"), so a bare rejection is not good enough.
func TestValidateDeclaredResources_ErrorEnumeratesLegalTypes(t *testing.T) {
	raw := json.RawMessage(`[{"type":"file_scope","uri":"file:a.go"}]`)
	err := ValidateDeclaredResources(raw)
	if err == nil {
		t.Fatal("expected rejection")
	}
	msg := err.Message
	if d, ok := err.Details.(map[string]any); ok {
		if b, mErr := json.Marshal(d); mErr == nil {
			msg += " " + string(b)
		}
	}
	if !strings.Contains(msg, "file_scope") {
		t.Errorf("error does not name the offending type; got %q", msg)
	}
	for _, legal := range []string{"repo", "path", "document", "section", "service", "external_ref"} {
		if !strings.Contains(msg, legal) {
			t.Errorf("error does not enumerate legal type %q; got %q", legal, msg)
		}
	}
}

// Every type resourceToLock maps must be accepted — including external_ref,
// which is the trap: it is a KNOWN type that legitimately maps to NO lock, so
// "resourceToLock returned empty" can never itself be the error signal.
func TestValidateDeclaredResources_AcceptsEveryMappedType(t *testing.T) {
	cases := []string{
		`{"type":"repo","uri":"repo:aihub","intent":"write"}`,
		`{"type":"path","uri":"file:internal/domain/conflicts.go","intent":"write"}`,
		`{"type":"document","uri":"file:docs/design.md","intent":"read"}`,
		`{"type":"section","uri":"file:docs/design.md#s23","intent":"write"}`,
		`{"type":"service","uri":"service:tot","intent":"write"}`,
		`{"type":"external_ref","uri":"https://example.com/issue/1","intent":"read"}`,
	}
	for _, c := range cases {
		if err := ValidateDeclaredResources(json.RawMessage("[" + c + "]")); err != nil {
			t.Errorf("rejected a legal entry %s: %v", c, err.Message)
		}
	}
}

// external_ref maps to no lock by design. Guard it explicitly so a future
// refactor cannot "simplify" validation into `lockType == "" -> error`.
func TestValidateDeclaredResources_ExternalRefAcceptedThoughItTakesNoLock(t *testing.T) {
	res := DeclaredResourceItem{Type: "external_ref", URI: "https://example.com/x"}
	if lt, _ := resourceToLock(res, "aihub"); lt != "" {
		t.Fatalf("precondition changed: external_ref now maps to lock type %q", lt)
	}
	if err := ValidateDeclaredResources(json.RawMessage(`[{"type":"external_ref","uri":"https://example.com/x"}]`)); err != nil {
		t.Errorf("external_ref must stay valid even though it takes no lock: %v", err.Message)
	}
}

// An absent `type` is the single most common real-world shape (29 of 217 entries
// in aihub's last 60 wis used {access, uri} with no type at all) and it produced
// no lock just as silently. It must be rejected too.
func TestValidateDeclaredResources_RejectsAbsentType(t *testing.T) {
	raw := json.RawMessage(`[{"access":"write","uri":"repo:aihub/internal/render/sanitize.go"}]`)
	err := ValidateDeclaredResources(raw)
	if err == nil {
		t.Fatal("accepted an entry with no `type` — that shape acquires no lock silently")
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
}

// The other half of the reporter's mistake: right type, wrong field name. A
// `path` entry with no uri yields lock key "<project>:" — a live footgun.
func TestValidateDeclaredResources_RejectsMissingURI(t *testing.T) {
	if err := ValidateDeclaredResources(json.RawMessage(`[{"type":"path","value":"internal/a.go"}]`)); err == nil {
		t.Fatal("accepted type=path with no uri (field was `value`) — lock key would be \"<project>:\"")
	}
}

// Empty and absent resource lists stay legal — validation must not break the
// common no-declared-resources wi.
func TestValidateDeclaredResources_EmptyIsLegal(t *testing.T) {
	for _, raw := range []string{``, `null`, `[]`} {
		if err := ValidateDeclaredResources(json.RawMessage(raw)); err != nil {
			t.Errorf("ValidateDeclaredResources(%q) = %v, want nil", raw, err.Message)
		}
	}
}

// Malformed JSON must be a 400, not a panic or a 500.
func TestValidateDeclaredResources_MalformedJSONIs400(t *testing.T) {
	err := ValidateDeclaredResources(json.RawMessage(`{"not":"an array"}`))
	if err == nil {
		t.Fatal("accepted non-array declared_resources")
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
}

// THE headline defect: pf_predict_conflicts is the call pf-work uses as its
// pre-claim gate, and with a mistyped resource it used to answer
// {"predictions":[],"severity":"info"} — a fake all-clear. It must now refuse.
//
// A nil pool is deliberate: it proves validation runs BEFORE any database
// access. If validation ever moves below the first query this test panics
// instead of passing, which is the signal we want.
func TestPredictConflicts_RejectsUnknownTypeBeforeTouchingDB(t *testing.T) {
	req := &PredictConflictsRequest{
		Project:           "tether",
		DeclaredResources: json.RawMessage(`[{"type":"file_scope","value":"tether:internal/session/attach.go"}]`),
		DryRun:            true,
	}
	resp, err := PredictConflicts(context.Background(), nil, req, map[string]string{"tether": "writer"})
	if err == nil {
		t.Fatalf("PredictConflicts returned no error; resp=%+v — this is the fake all-clear", resp)
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
	if resp != nil && len(resp.Predictions) == 0 && err == nil {
		t.Error("returned an empty prediction set for input it could not understand")
	}
}

// ---------------------------------------------------------------------------
// requested_locks (defect 2): a malformed value reached Postgres and came back
// as 500 INTERNAL_ERROR + raw SQLSTATE 23514.
// ---------------------------------------------------------------------------

// Guessing the neighbouring {type, value} shape leaves resource_type empty,
// which used to hit the CHECK constraint and surface as a 500.
func TestValidateRequestedLocks_RejectsEmptyResourceTypeWith400(t *testing.T) {
	err := ValidateRequestedLocks([]ResourceLockReq{{ResourceType: "", ResourceKey: "aihub:internal/a.go"}})
	if err == nil {
		t.Fatal("accepted an empty resource_type — this reaches Postgres as SQLSTATE 23514 / HTTP 500")
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400 (was 500 INTERNAL_ERROR + bare SQLSTATE)", err.HTTPStatus)
	}
	msg := err.Message
	if d, ok := err.Details.(map[string]any); ok {
		if b, mErr := json.Marshal(d); mErr == nil {
			msg += " " + string(b)
		}
	}
	for _, legal := range []string{"git_branch", "worktree", "file_scope", "tcp_port", "deploy_env"} {
		if !strings.Contains(msg, legal) {
			t.Errorf("error must list legal lock type %q; got %q", legal, msg)
		}
	}
}

func TestValidateRequestedLocks_RejectsUnknownLockType(t *testing.T) {
	if err := ValidateRequestedLocks([]ResourceLockReq{{ResourceType: "path", ResourceKey: "x"}}); err == nil {
		t.Fatal("accepted resource_type=path (a DECLARED type, not a lock type)")
	}
}

func TestValidateRequestedLocks_RejectsEmptyResourceKey(t *testing.T) {
	if err := ValidateRequestedLocks([]ResourceLockReq{{ResourceType: "file_scope", ResourceKey: ""}}); err == nil {
		t.Fatal("accepted an empty resource_key — NOT NULL/CHECK violation downstream")
	}
}

func TestValidateRequestedLocks_AcceptsEveryConstraintType(t *testing.T) {
	for lt := range resourceLockTypes {
		if err := ValidateRequestedLocks([]ResourceLockReq{{ResourceType: lt, ResourceKey: "k"}}); err != nil {
			t.Errorf("rejected legal lock type %q: %v", lt, err.Message)
		}
	}
}

func TestValidateRequestedLocks_EmptySliceIsLegal(t *testing.T) {
	if err := ValidateRequestedLocks(nil); err != nil {
		t.Errorf("nil requested_locks must be legal (server derives them): %v", err.Message)
	}
}

// The Go lock-type set and the SQL CHECK constraint are two copies of one
// invariant; aihub has been bitten before by a value replicated across places
// that drifted (mem_i9I2g8Hv). Parse the migration and compare, so adding a
// type in one place without the other fails here rather than in production.
func TestResourceLockTypesMatchMigrationCheckConstraint(t *testing.T) {
	const migration = "../db/migrations/0004_run_attempts.sql"
	b, readErr := os.ReadFile(migration)
	if readErr != nil {
		t.Fatalf("cannot read %s: %v", migration, readErr)
	}
	// CHECK (resource_type IN ('git_branch', 'worktree', 'file_scope', ...))
	re := regexp.MustCompile(`(?s)CHECK\s*\(\s*resource_type\s+IN\s*\((.*?)\)\s*\)`)
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatal("could not locate the resource_type CHECK constraint in " + migration)
	}
	var fromSQL []string
	for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(string(m[1]), -1) {
		fromSQL = append(fromSQL, q[1])
	}
	var fromGo []string
	for lt := range resourceLockTypes {
		fromGo = append(fromGo, lt)
	}
	sort.Strings(fromSQL)
	sort.Strings(fromGo)
	if strings.Join(fromSQL, ",") != strings.Join(fromGo, ",") {
		t.Errorf("lock types drifted:\n  SQL CHECK: %v\n  Go set:    %v", fromSQL, fromGo)
	}
}

// ---------------------------------------------------------------------------
// Stored-data paths must stay non-fatal (loud, not breaking).
// ---------------------------------------------------------------------------

// 14% of existing entries are mistyped. Claim / force_takeover / acquire_locks
// all read ALREADY-STORED declared_resources, so hard-failing there would make
// those wis unclaimable. They must report instead.
func TestUnrecognizedDeclaredResources_ReportsWithoutFailing(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"path","uri":"file:internal/a.go","intent":"write"},
		{"type":"file_scope","value":"aihub:internal/b.go"},
		{"access":"write","uri":"repo:aihub/internal/c.go"}
	]`)
	got := UnrecognizedDeclaredResources(raw)
	if len(got) != 2 {
		t.Fatalf("UnrecognizedDeclaredResources = %v (len %d), want 2 entries", got, len(got))
	}
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "file_scope") {
		t.Errorf("report should name the bad type file_scope; got %q", joined)
	}
	// The legal entry must not be reported.
	if strings.Contains(joined, "internal/a.go") {
		t.Errorf("report includes a VALID entry; got %q", joined)
	}
}

func TestUnrecognizedDeclaredResources_CleanInputReportsNothing(t *testing.T) {
	raw := json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"},
	                         {"type":"external_ref","uri":"https://x/1"}]`)
	if got := UnrecognizedDeclaredResources(raw); len(got) != 0 {
		t.Errorf("clean input reported as unrecognized: %v", got)
	}
}

// Must not panic on garbage — it runs inside claim, which must not break on
// unparseable stored data. Assert the EXACT count per input: an earlier version
// only failed on len > 1, so a spurious warning on empty input still passed.
func TestUnrecognizedDeclaredResources_MalformedIsSafe(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{``, 0},                  // absent
		{`null`, 0},              // explicit null
		{`[]`, 0},                // empty list
		{`{"not":"array"}`, 1},   // not a list at all — one summary warning
		{`[[]]`, 1},              // list of non-objects — unmarshal fails, one warning
		{`[{"type":"path"}]`, 1}, // valid type, missing uri
	}
	for _, c := range cases {
		got := UnrecognizedDeclaredResources(json.RawMessage(c.raw))
		if len(got) != c.want {
			t.Errorf("UnrecognizedDeclaredResources(%q) returned %d entries %v, want %d", c.raw, len(got), got, c.want)
		}
	}
}

// aihub#238 review finding 2: a RECOGNIZED type with no uri is the second silent
// shape. It maps to a well-typed lock with an empty key, which claim now refuses
// to insert — so it must be reported, or the skip is as quiet as the original bug.
func TestUnrecognizedDeclaredResources_ReportsRecognizedTypeMissingURI(t *testing.T) {
	got := UnrecognizedDeclaredResources(json.RawMessage(`[{"type":"service","intent":"write"}]`))
	if len(got) != 1 {
		t.Fatalf("got %d entries %v, want 1 — a service entry with no uri derives (\"deploy_env\",\"\") and locks nothing", len(got), got)
	}
	if !strings.Contains(got[0], "uri") {
		t.Errorf("report should point at the missing `uri`; got %q", got[0])
	}
}

// external_ref takes no lock by design, so a missing uri there is not a
// lock-related defect and must not be reported as one.
func TestUnrecognizedDeclaredResources_ExternalRefMissingURINotReported(t *testing.T) {
	if got := UnrecognizedDeclaredResources(json.RawMessage(`[{"type":"external_ref"}]`)); len(got) != 0 {
		t.Errorf("external_ref takes no lock either way; should not be reported: %v", got)
	}
}

// ─── aihub#395 part 4: the per-type uri scheme, as a MECHANISM ──────────────
//
// The scheme has been PUBLISHED per type since aihub#238 —
//
//	"file:<repo-relative-path>" for path/document/section
//	"repo:<repo-name>"          for repo
//	"service:<name>"            for service
//	a plain URL                 for external_ref
//
// — and, until this change, validated nowhere. ValidateDeclaredResources checked
// that `uri` was non-empty and stopped there, while resourceToLock derives the
// lock key with strings.TrimPrefix, which is a NO-OP on a wrong prefix. So
// `{"type":"repo","uri":"file:x"}` was accepted with a 200 and keyed its
// git_branch lock as "file:x/main" — a lock nobody else will ever collide with,
// on a work item whose author believes the repo is guarded. That is the same
// silent-lockless shape aihub#238 was filed about, one level down: there the
// TYPE was unrecognised, here the type is fine and the KEY is nonsense.
//
// ⚠️ Why the failure is invisible without a check. A wrong scheme does not
// produce an error, an empty lock type, or an empty key — the three things any
// existing guard looks at. It produces a well-formed lock on the wrong key, and
// `acquired_locks` in the claim response lists it, so the caller's evidence says
// the declaration worked.
//
// ─── Quantified over the type set, not over a list of names ────────────────
//
// declaredResourceURISchemeFixtures is keyed by declared type and the test below
// asserts it covers DeclaredResourceTypeList() exactly, so a type added to the
// vocabulary tomorrow fails here until somebody states its scheme — rather than
// slipping through unvalidated, which is what "add the type, forget the rule"
// looks like from the outside.

// declaredResourceURISchemeFixtures gives, per declared type, a uri that is
// correctly schemed and one that is not. The wrong ones deliberately borrow a
// scheme that belongs to a DIFFERENT declared type, because that is the real
// mistake: the vocabularies are adjacent and a caller reaches for the wrong one.
var declaredResourceURISchemeFixtures = map[string]struct{ good, bad string }{
	"repo":         {good: "repo:aihub", bad: "file:aihub"},
	"path":         {good: "file:internal/a.go", bad: "repo:internal/a.go"},
	"document":     {good: "file:docs/design.md", bad: "internal/a.go"},
	"section":      {good: "file:docs/design.md#s23", bad: "service:docs/design.md#s23"},
	"service":      {good: "service:tot", bad: "file:tot"},
	"external_ref": {good: "https://example.com/issue/1", bad: "file:docs/x.md"},
}

// TestValidateDeclaredResources_ValidatesTheURISchemePerType is THE gate for
// aihub#395 part 4. It FAILS on the pre-fix tree for every one of the six types,
// because nothing looked at the scheme at all.
func TestValidateDeclaredResources_ValidatesTheURISchemePerType(t *testing.T) {
	types := DeclaredResourceTypeList()
	if len(types) == 0 {
		t.Fatal("DeclaredResourceTypeList() is empty — every case below would be vacuous")
	}
	// Coverage in both directions, so neither table can drift out from under the
	// other: a new declared type with no fixture, and a fixture for a type that
	// is no longer declarable, both fail here.
	for _, typ := range types {
		if _, ok := declaredResourceURISchemeFixtures[typ]; !ok {
			t.Errorf("declared type %q has no uri-scheme fixture — a type added to the "+
				"vocabulary without a scheme rule is a type whose uri is validated by nothing, "+
				"which is aihub#395 part 4 reopening. Add it to "+
				"declaredResourceURISchemeFixtures and give it a rule in "+
				"declaredResourceURISchemes.", typ)
		}
	}
	declarable := map[string]bool{}
	for _, typ := range types {
		declarable[typ] = true
	}
	for typ := range declaredResourceURISchemeFixtures {
		if !declarable[typ] {
			t.Errorf("declaredResourceURISchemeFixtures covers %q, which is no longer a declared "+
				"type — stale fixture", typ)
		}
	}

	for _, typ := range types {
		fx, ok := declaredResourceURISchemeFixtures[typ]
		if !ok {
			continue
		}
		t.Run(typ, func(t *testing.T) {
			good := json.RawMessage(`[{"type":"` + typ + `","uri":"` + fx.good + `"}]`)
			if err := ValidateDeclaredResources(good); err != nil {
				t.Errorf("rejected the CORRECTLY schemed uri %q for type %q: %s — a scheme check "+
					"that refuses legal input is worse than none, because the fix for it is to "+
					"delete the check", fx.good, typ, err.Message)
			}

			bad := json.RawMessage(`[{"type":"` + typ + `","uri":"` + fx.bad + `"}]`)
			err := ValidateDeclaredResources(bad)
			if err == nil {
				t.Fatalf("accepted uri %q for type %q. The scheme is PUBLISHED for this type and "+
					"resourceToLock derives the key with strings.TrimPrefix, a no-op on a wrong "+
					"prefix — so this entry takes a well-formed lock on a nonsense key and the "+
					"claim response reports it as acquired. Nothing else in the system will ever "+
					"collide with it.", fx.bad, typ)
			}
			if err.HTTPStatus != 400 {
				t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
			}
			// The rejection has to NAME the offending entry, or a caller with a
			// twenty-entry payload learns only that one of them is wrong.
			if !strings.Contains(err.Message, "declared_resources[0]") {
				t.Errorf("message %q does not name the offending entry by index", err.Message)
			}
			if !strings.Contains(err.Message, fx.bad) {
				t.Errorf("message %q does not quote the uri it rejected (%q)", err.Message, fx.bad)
			}
			details, ok := err.Details.(map[string]any)
			if !ok {
				t.Fatalf("details are %T, want map[string]any: %#v", err.Details, err.Details)
			}
			for _, want := range []string{"index", "got_type", "got_uri", "expected_scheme"} {
				if _, present := details[want]; !present {
					t.Errorf("details are missing %q: %#v", want, details)
				}
			}
		})
	}
}

// TestDeclaredResourceURISchemes_CoverEveryDeclarableType pins the production
// table itself, independently of the fixtures above. The fixture table proves the
// BEHAVIOUR for each type; this proves there is a RULE for each type, which is
// what stops a new type from being added with no rule and no failing test.
func TestDeclaredResourceURISchemes_CoverEveryDeclarableType(t *testing.T) {
	for _, typ := range DeclaredResourceTypeList() {
		if _, ok := declaredResourceURISchemes[typ]; !ok {
			t.Errorf("declared type %q has no entry in declaredResourceURISchemes, so its uri "+
				"scheme is unvalidated", typ)
		}
	}
	for typ := range declaredResourceURISchemes {
		if !declaredResourceTypes[typ] {
			t.Errorf("declaredResourceURISchemes has a rule for %q, which is not a declared type",
				typ)
		}
	}
	// The published sentence is GENERATED from this table (the MCP layer calls
	// DeclaredResourceURISchemeDoc), so the contract cannot drift from the
	// validator. Assert the generator actually names every type, or the
	// generation would be decorative.
	doc := DeclaredResourceURISchemeDoc()
	for _, typ := range DeclaredResourceTypeList() {
		if !strings.Contains(doc, typ) {
			t.Errorf("DeclaredResourceURISchemeDoc() does not mention type %q: %q", typ, doc)
		}
	}
	for _, scheme := range declaredResourceURISchemes {
		if scheme != "" && !strings.Contains(doc, scheme) {
			t.Errorf("DeclaredResourceURISchemeDoc() does not mention scheme %q: %q", scheme, doc)
		}
	}
}
