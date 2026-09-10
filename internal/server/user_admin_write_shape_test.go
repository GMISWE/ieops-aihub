package server

// aihub#543 probe wave 2, band 2 (identity/authz) — the
// `docs/mcp-cards/pf_create_user.md` and `docs/mcp-cards/pf_update_user.md`
// sentences about what the two admin-user handlers WRITE and what they answer
// with.
//
//	pf_create_user.md
//	  "the description says it is required for human users … so it is prose and
//	   the server enforces it" / "`machine` users get a generated email"
//	      -> TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines
//	  "The response does **not** include an API key" / the corrected corpus
//	   sentence about why `author_aliases` is absent
//	      -> TestCreateUserResponseIsTheHandlersOwnProjection
//	pf_update_user.md
//	  "an omitted field leaves the column alone while `[]` … CLEARS it" /
//	   "`user_type` is **not updatable** on this path at all"
//	      -> TestUpdateUserBindsThreeFieldsAndDropsTheRest
//
// The nil pool is the instrument, as in create_user_vocab_test.go and
// update_user_vocab_test.go: any DB access panics, so "reached the statement" and
// "was refused before it" are distinguishable with no database. That matters more
// than usual here — every DB-gated test in this repo SKIPs on `go test ./...`
// while still reading as coverage, and these are the claims a caller acts on.
//
//	GOWORK=off go test ./internal/server/ -run 'TestCreateUserEmailIsRequired|TestUpdateUserBindsThreeFields|TestCreateUserResponseIsTheHandlers' -count=1

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// reachesTheStatement drives a handler call and reports whether it touched the
// nil pool. A panic means the request got past every guard above the statement;
// no panic means it was refused, and the recorder then holds the answer.
//
// Returned rather than asserted, so one arm can require both directions of the
// same distinction — which is the whole content of the "absent is not empty"
// claim and of the "user_type is dropped" claim.
func reachesTheStatement(call func()) (reached bool) {
	defer func() {
		if r := recover(); r != nil {
			reached = true
		}
	}()
	call()
	return false
}

// TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines holds
// pf_create_user's `email` row from the enforcement side.
//
// The card says the conditional requirement is unexpressible in a flat
// `required` list, "so it is prose and the server enforces it". This is that
// enforcement, and it is asserted as the PAIR rather than as either half:
// "humans without an email are refused" is satisfied by a handler that refuses
// everybody, and "machine users need no email" is satisfied by a handler that
// requires nothing. Only the two together say the requirement is conditional.
//
// ⚠️ What this arm does NOT hold, stated because an unstated limit reads as
// coverage: the generated address's SHAPE. `machine-<slug>@polyforge.internal`
// is built immediately before the INSERT, so no answer carries it and the nil
// pool cannot show it. What is observable — and what the card's sentence is
// actually about — is that a machine user needs no mailbox: the request gets
// past the email gate that stops a human.
//
// MUTANTS:
//
//	M31 enforcement: delete the `if email == nil` refusal
//	                                          RED  human_without_email_is_refused
//	M32 enforcement: move the machine-email branch BELOW that refusal
//	                                          RED  machine_without_email_proceeds
//	M33 enforcement: require an email for machine users too
//	                                          RED  machine_without_email_proceeds
//	M34 publication: add "email" to pf_create_user's required list
//	                                          RED  in internal/mcp,
//	                                               TestCreateUserPublishesEmailAsProseRatherThanRequired
//	M35 control: none — the human_with_email leg IS the control, and neutering it
//	     (refusing everybody) reddens it
func TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines(t *testing.T) {
	t.Run("human_without_email_is_refused", func(t *testing.T) {
		var rec = new(recorderHolder)
		reached := reachesTheStatement(func() {
			rec.r = createUserRequest(t, nil, map[string]any{"display_name": "Probe 580"})
		})
		if reached {
			t.Fatal("a human user with no email reached the INSERT. email is NOT in the " +
				"published required array — the conditional requirement is prose — so this " +
				"handler is the only thing enforcing it, and a row with a NULL email is what " +
				"gets written instead of a 400.")
		}
		if rec.r.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (body: %s)", rec.r.Code, rec.r.Body.String())
		}
		if !strings.Contains(rec.r.Body.String(), "email") {
			t.Errorf("the refusal does not name `email`, so a caller cannot tell which field "+
				"was missing: %s", rec.r.Body.String())
		}
	})

	t.Run("machine_without_email_proceeds", func(t *testing.T) {
		reached := reachesTheStatement(func() {
			_ = createUserRequest(t, nil, map[string]any{
				"display_name": "Probe 580 Machine", "user_type": "machine",
			})
		})
		if !reached {
			t.Error("a machine user with no email was refused before the INSERT. A machine " +
				"identity exists without a mailbox precisely because the handler generates the " +
				"address, and pf_create_user.md's `user_type` row promises that; refusing it " +
				"makes every agent identity need a real inbox.")
		}
	})

	// The control, in the direction that catches a handler which refuses
	// everything: a human WITH an email must get through.
	t.Run("human_with_email_proceeds", func(t *testing.T) {
		reached := reachesTheStatement(func() {
			_ = createUserRequest(t, nil, map[string]any{
				"display_name": "Probe 580 Human", "email": "probe580@example.com",
			})
		})
		if !reached {
			t.Error("a human user WITH an email was refused before the INSERT, so the arm above " +
				"is green because nothing gets through rather than because the requirement is " +
				"conditional")
		}
	})
}

// recorderHolder carries a recorder out of the closure reachesTheStatement
// drives, so the refusing legs can assert on the answer as well as on the
// absence of a panic.
type recorderHolder struct{ r *httptest.ResponseRecorder }

// TestUpdateUserBindsThreeFieldsAndDropsTheRest holds two pf_update_user claims
// with one instrument, because they are the same fact seen from two sides: the
// handler acts on the fields its request struct binds, and only those.
//
// 🔴 The discriminating observable is "did this request produce a WRITE", and the
// nil pool answers it without a database. `if len(sets) == 0` refuses with 400
// "no fields to update", so:
//
//	{"author_aliases": []}       must WRITE  — [] is non-nil, so it clears
//	{"author_aliases": null}     must NOT    — null decodes to nil, same as absent
//	{}                           must NOT    — the control for "no write"
//	{"user_type": "machine"}     must NOT    — the field is not bound at all
//	{"display_name": "x"}        must WRITE  — the control for "a write happens"
//
// The `[]` versus `null` pair is the whole of "absent and empty are different
// instructions here". A handler testing `len(req.AuthorAliases) > 0` instead of
// `!= nil` would make them identical, and the caller who sent `[]` meaning "no
// change" would be right by accident — after having been told, by the published
// description, that it clears.
//
// The `user_type` case is the same instrument used as a census: the value is
// silently DROPPED rather than refused, so the request behaves exactly as though
// the caller had sent nothing. That is a stronger statement than "the struct has
// three fields", and it is the one pf_update_user.md's Open section makes.
//
// MUTANTS:
//
//	M36 enforcement: `if len(req.AuthorAliases) > 0` in place of `!= nil`
//	                                          RED  empty_array_is_a_write
//	M37 enforcement: bind UserType in the request struct and add its SET clause
//	                                          RED  user_type_is_not_a_write
//	M38 enforcement: drop the `len(sets) == 0` refusal
//	                                          RED  no_fields_is_not_a_write and
//	                                               null_alias_list_is_not_a_write
//	M39 publication: drop "[]"/"clear" from the published author_aliases
//	     description                          RED  in internal/mcp,
//	                                               TestUpdateUserAuthorAliasesIsPublished
//	M40 publication: publish `user_type` on pf_update_user
//	                                          RED  K3 (the card's param list) and
//	                                               the layer-1 published-field gate
func TestUpdateUserBindsThreeFieldsAndDropsTheRest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      map[string]any
		wantWrite bool
		why       string
	}{
		{
			name: "empty_array_is_a_write", body: map[string]any{"author_aliases": []any{}},
			wantWrite: true,
			why: "[] decodes to a non-nil empty slice and the handler tests req.AuthorAliases " +
				"!= nil, so it CLEARS the column. That is what the published description " +
				"promises; a handler that treated it as \"nothing to do\" would leave the " +
				"aliases in place and answer 400 to a caller who asked to clear them.",
		},
		{
			name: "null_alias_list_is_not_a_write", body: map[string]any{"author_aliases": nil},
			wantWrite: false,
			why: "null decodes to a nil slice, which is what an OMITTED field also decodes to. " +
				"Absent and empty being different instructions is the whole claim, and null " +
				"has to land on the absent side of it — the MCP tool never sends null (see " +
				"TestUpdateUserClearingAliasesIsDistinctFromOmitting), so this is the HTTP " +
				"surface's half.",
		},
		{
			name: "no_fields_is_not_a_write", body: map[string]any{},
			wantWrite: false,
			why: "the control for the \"not a write\" verdict: with no bound field present the " +
				"handler must refuse rather than run an UPDATE with an empty SET list.",
		},
		{
			name: "user_type_is_not_a_write", body: map[string]any{"user_type": "machine"},
			wantWrite: false,
			why: "the request struct binds display_name, role and author_aliases only, so " +
				"user_type is silently dropped — the request behaves as though it carried " +
				"nothing. pf_update_user.md records that as an HTTP-only exposure precisely " +
				"because the tool does not publish the field; binding it here without " +
				"publishing it would be the aihub#419 BOUND_FIELD_UNPUBLISHED shape.",
		},
		{
			name: "display_name_is_a_write", body: map[string]any{"display_name": "Probe 580"},
			wantWrite: true,
			why: "the control for the \"is a write\" verdict: a bound field must reach the " +
				"UPDATE, or every \"not a write\" above is green because nothing ever writes.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec = new(recorderHolder)
			reached := reachesTheStatement(func() {
				rec.r = updateUserRequest(t, nil, tc.body)
			})
			if reached == tc.wantWrite {
				return
			}
			if tc.wantWrite {
				t.Errorf("%v did NOT reach the UPDATE; the handler answered %d (%s).\n%s",
					tc.body, rec.r.Code, strings.TrimSpace(rec.r.Body.String()), tc.why)
				return
			}
			t.Errorf("%v reached the UPDATE.\n%s", tc.body, tc.why)
		})
	}
}

// TestCreateUserResponseIsTheHandlersOwnProjection is the correction aihub#543
// wave 2 made to pf_create_user.md's hop 5.
//
// 🔴 The card used to reason that `author_aliases` is absent from
// `response_keys_observed` because "under a non-projecting response" the six
// corpus calls did not set one. Measured 2026-09-10: the response IS projected,
// just not by this process. `jsonResult` forwards whatever the server sent, and
// handleCreateUser answers with a hand-built five-key map rather than the
// inserted row — so `author_aliases` could not appear however many callers set
// it, and neither could `created_at` or `updated_at`. The corpus record is
// evidence about the HANDLER's key set, not about caller behaviour.
//
// The response literal is compared against aihub#412's corpus record in BOTH
// directions, so this cannot be satisfied by editing one of them: a key added to
// the handler is red, and a key added to the corpus record is red. K7 already
// pins the card's copy to that record, which makes the three one value.
//
// The `author_aliases` asymmetry is asserted directly, because it is the sentence
// being corrected: the column is in the INSERT and not in the answer.
//
// MUTANTS:
//
//	M41 enforcement: add "author_aliases": req.AuthorAliases to the response map
//	                                          RED  response_keys_match_the_corpus
//	                                               AND the_written_column_is_not
//	                                               _returned
//	M42 enforcement: return the inserted row instead of the literal
//	                                          RED  the_response_is_a_literal — the
//	                                               claim would then be false and
//	                                               the card has to be reworded
//	M43 publication: add a key to the corpus record
//	                                          RED  response_keys_match_the_corpus
//	                                               (and K7 for the card's copy)
//	M44 enforcement: drop author_aliases from the INSERT column list
//	                                          RED  the_column_is_written
//	M45 floor: point the AST walk at a handler name router.go does not declare
//	                                          RED  the fatal in
//	                                               createUserResponseAndInsert. ⚠️
//	                                               RENAMING the handler instead does
//	                                               not reach that fatal:
//	                                               createUserRequest in
//	                                               create_user_vocab_test.go names
//	                                               the function, so the package
//	                                               stops compiling first. The
//	                                               compiler is the first line of
//	                                               defence here and this fatal the
//	                                               second, for the case where the
//	                                               walk's target moves without the
//	                                               symbol doing so.
func TestCreateUserResponseIsTheHandlersOwnProjection(t *testing.T) {
	const column = "author_aliases"
	responseKeys, insertColumns := createUserResponseAndInsert(t)

	t.Run("the_response_is_a_literal", func(t *testing.T) {
		if len(responseKeys) < 4 {
			t.Fatalf("handleCreateUser's success answer has %d literal key(s) %v. It builds a "+
				"five-key map; fewer means either the walk is not reading the handler or the "+
				"answer stopped being a literal — and if it now returns the row, "+
				"pf_create_user.md's hop 5 has to say so, because the reasoning about "+
				"%s's absence depends on the projection.", len(responseKeys), responseKeys, column)
		}
	})

	t.Run("the_column_is_written", func(t *testing.T) {
		found := false
		for _, c := range insertColumns {
			if c == column {
				found = true
			}
		}
		if !found {
			t.Fatalf("handleCreateUser's INSERT column list is %v and does not include %q. The "+
				"whole point of the corrected sentence is that the column is WRITTEN and not "+
				"RETURNED; without the write half there is no asymmetry to describe.",
				insertColumns, column)
		}
	})

	t.Run("response_keys_match_the_corpus", func(t *testing.T) {
		corpus := corpusObservedKeys(t, "pf_create_user")
		if len(corpus) == 0 {
			t.Fatalf("aihub#412's corpus record for pf_create_user lists no observed keys, so " +
				"the comparison below is satisfied by any response at all")
		}
		if strings.Join(responseKeys, ",") != strings.Join(corpus, ",") {
			t.Errorf("handleCreateUser answers with keys %v; aihub#412's corpus record observed "+
				"%v.\nThe corpus is a union over 6 real calls, so for a handler whose answer is "+
				"a FIXED literal the two are the same set — which is exactly why the card can "+
				"reason from the record to the handler. A disagreement means one of them moved: "+
				"if the handler gained a key, the record and the card's copy need regenerating "+
				"(PF_CARDS_REGEN=1); if the record gained one, it was edited by hand.",
				responseKeys, corpus)
		}
	})

	t.Run("the_written_column_is_not_returned", func(t *testing.T) {
		for _, k := range responseKeys {
			if k == column {
				t.Errorf("handleCreateUser now returns %q. pf_create_user.md says it cannot, "+
					"and reasons from that to why the corpus record does not list it — reword "+
					"hop 5 in the same change, and regenerate the corpus record and the card's "+
					"copy.", column)
			}
		}
	})

	// pf_create_user.md's "The response does not include an API key: that is
	// pf_create_api_key, a separate call." Same literal, different claim, and it
	// is the security-relevant one: a key minted here would be handed to whoever
	// could call the tool, with no separate audit point.
	t.Run("no_credential_is_returned", func(t *testing.T) {
		for _, k := range responseKeys {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "key") || strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") {
				t.Errorf("handleCreateUser's answer carries %q. Creating a user and issuing a "+
					"credential are deliberately two calls (pf_create_api_key), so a key in "+
					"this response is a credential nobody asked for and nothing separately "+
					"records.", k)
			}
		}
	})
}

// insertColumnList pulls the parenthesised column list out of an INSERT.
var insertColumnList = regexp.MustCompile(`(?is)insert\s+into\s+users\s*\(([^)]*)\)`)

// createUserResponseAndInsert reads handleCreateUser's success-answer key set and
// its INSERT column list out of the source.
//
// AST rather than a text scan of the file: `map[string]any{…}` literals and
// INSERT statements both appear in several handlers in router.go, and a scan that
// found the wrong one would report a key set belonging to another endpoint while
// looking exactly like a correct read.
func createUserResponseAndInsert(t *testing.T) (responseKeys, insertColumns []string) {
	t.Helper()
	const rel = "router.go"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — a parse failure yields empty key sets, which every comparison "+
			"below would treat as agreement", rel, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "handleCreateUser" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatalf("handleCreateUser is not declared in %s. It is the subject of every assertion "+
			"in this arm, so a rename has to be followed here rather than silently reported as "+
			"a clean answer.", rel)
	}

	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CompositeLit:
			// map[string]any{...} — the success answer. Collected from every such
			// literal in the function; handleCreateUser has exactly one.
			m, ok := v.Type.(*ast.MapType)
			if !ok {
				return true
			}
			if ident, isIdent := m.Key.(*ast.Ident); !isIdent || ident.Name != "string" {
				return true
			}
			for _, elt := range v.Elts {
				kv, isKV := elt.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				lit, isLit := kv.Key.(*ast.BasicLit)
				if !isLit || lit.Kind != token.STRING {
					continue
				}
				if s, uerr := strconv.Unquote(lit.Value); uerr == nil {
					responseKeys = append(responseKeys, s)
				}
			}
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(v.Value)
			if uerr != nil {
				return true
			}
			if m := insertColumnList.FindStringSubmatch(s); m != nil {
				for _, col := range strings.Split(m[1], ",") {
					if c := strings.TrimSpace(col); c != "" {
						insertColumns = append(insertColumns, c)
					}
				}
			}
		}
		return true
	})

	sort.Strings(responseKeys)
	sort.Strings(insertColumns)
	return responseKeys, insertColumns
}

// corpusObservedKeys reads aihub#412's per-tool hop-5 record, sorted.
func corpusObservedKeys(t *testing.T, tool string) []string {
	t.Helper()
	path := "../../docs/audits/aihub-412-corpus-facts/response-keys/" + tool + ".json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the record is what the card copies and K7 pins, so a missing "+
			"file is a broken comparison rather than a tool with no history", path, err)
	}
	var rec struct {
		Keys []string `json:"observed_top_level_keys"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	out := append([]string(nil), rec.Keys...)
	sort.Strings(out)
	return out
}
