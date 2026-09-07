package mcp_test

// aihub#388 — the pf_claim_work_item RESPONSE projection, as a mechanism.
//
// The claim handler built its answer with a KEEP-LIST:
//
//	safeResult := map[string]any{"attempt_id": …, "claim_epoch": …, "ok": true}
//	for _, k := range []string{"expires_at", "acquired_locks",
//	    "current_attempt_epoch", "slug", "project", "unrecognized_resources"} { … }
//
// so every field of domain.ClaimResponse that nobody remembered to list was
// dropped in silence. Four were: `requires_human_session`, `wi_type`, `id` and
// `step_recovery_hint`. The first is the one the post-claim routing rule
// BRANCHES ON — `false` dispatches /pf-execute unattended, `true` stops and waits
// for a human — so the response omitted exactly the field the caller needs to
// decide what to do next. Measured live 2026-09-07: three real claims
// (aihub#380/#382/#383) came back with none of the three, and the claim of THIS
// work item did the same.
//
// The list had also rotted in the other direction: `expires_at` is not a field
// of ClaimResponse at all (v1.21 removed it — the force_takeover handler thirty
// lines up says "no expires_at; do not surface that field"), so the keep-list was
// carrying a name the response can never have while dropping four it always can.
//
// ─── Why the fix is the SHAPE, not the four names ───────────────────────────
//
// This is instances four through seven of one pattern, and the file next door
// already wrote down the diagnosis: recall_slim.go's keep-list silently swallowed
// a newly-added field three times (`total` aihub#249, the truncation pair
// aihub#269, `unmatched_types` aihub#289), which is why list_wi_slim.go
// (aihub#281) was deliberately built the other way round — its header says "so
// it cannot become the fourth instance". The claim projection sat in the SAME
// file as a single-field patch for the same failure (aihub#238 pinned
// `unrecognized_resources` into the list, with a comment explaining that dropping
// it makes the whole remedy inert). Somebody recognised the hazard, patched one
// field, and left the machine that produces instances running.
//
// So the gate below is quantified over domain.ClaimResponse's fields rather than
// over the four names, and one test asserts the SHAPE directly: a key the struct
// does not have yet must still arrive. A keep-list cannot pass that test however
// complete it is today, which is what makes it a fix rather than a fifth patch.
//
// ─── Why the assertions drive the real tool ────────────────────────────────
//
// A test on the projection function alone would be the aihub#309 trap this
// package has already been bitten by: a mutant one layer away from the defect
// left four pure-function tests green while the defect stood. Re-gating the
// projection at its CALL SITE — `if false { safeResult = slimClaimResult(...) }` —
// is exactly such a mutant. Everything here therefore goes through the registered
// MCP tool, a fake aihub, a real .polyforge.yaml and a real git clone, so what is
// asserted is the observable output of pf_claim_work_item.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestClaimResult -v

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// claimKeysWithheldFromTheModel is the SPECIFICATION of what the claim response
// may withhold: a key here must not reach the model, everything else must.
//
// It lives in the test rather than being read out of the production delete-list
// on purpose — same reason recallLocalOnlyParams does. Reading the
// implementation's own list would make the test agree with whatever the
// implementation happens to say; stating it here makes the two independent, so a
// field withheld in production without a line here goes red, and a line here for
// a field that is in fact forwarded goes red too.
//
// Each entry needs a reason, because "not in the response" is otherwise
// indistinguishable from the defect this file exists to catch.
var claimKeysWithheldFromTheModel = map[string]string{
	"session_secret": "THE original purpose of this projection (the handler's own comment: " +
		"\"Don't return session_secret to LLM (decision A)\"). The secret is minted in this " +
		"process and persisted to the state file at mode 0600; putting it in a tool result " +
		"would paste a live credential into a transcript. It is not a ClaimResponse field " +
		"today — the server never echoes it — so stripping it is defence in depth against a " +
		"server that starts to, which is precisely the risk a delete-list takes on and must " +
		"therefore answer for.",
	"goal": "consumed locally, not forwarded — domain.ClaimResponse.Goal's own doc comment says " +
		"so: it exists only so the claim can name the task branch " +
		"polyforge/<project>-<seq>-<kebab goal> without a second round-trip (aihub#322). It is " +
		"mutable work-item content that pf_get_work_item serves, and echoing it here would pay " +
		"for the goal text on every claim to tell the caller something it can already read.",
}

// claimResponseRealisticValues pins the few fields whose VALUE the handler acts
// on, so the claim stays on its happy path: `id` becomes the state file's key,
// and `slug`/`project` derive the branch name and the worktree path. Everything
// else is probed with a generated value.
//
// Every key here must be a real ClaimResponse field — asserted below, so this
// map cannot rot into the `expires_at` shape it exists to replace.
var claimResponseRealisticValues = map[string]any{
	"id":      "wi_01JCLAIMPROJECTION",
	"slug":    "aihub#388",
	"project": "aihub",
}

// claimResponseJSONFields returns domain.ClaimResponse's json tag -> field type.
// Reflection, not a hand-written list: a field added tomorrow is covered the day
// it is added, which is the entire point of the exercise.
func claimResponseJSONFields(t *testing.T) map[string]reflect.Type {
	t.Helper()
	typ := reflect.TypeOf(domain.ClaimResponse{})
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	if len(out) == 0 {
		t.Fatal("domain.ClaimResponse has no json fields at all — the reflection is broken, " +
			"not the struct, and every assertion below would be vacuous")
	}
	return out
}

// claimProbeValue invents a wire value for one field type. `requires_human_session`
// gets `false` deliberately: it is the routing-critical value AND the one a
// truthiness-based copy (`if v, ok := result[k]; ok && v != nil`-style, or any
// `if v {}`) would silently eat, so probing `true` would leave that bug green.
func claimProbeValue(t *testing.T, tag string, ft reflect.Type) any {
	t.Helper()
	if v, ok := claimResponseRealisticValues[tag]; ok {
		return v
	}
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	switch ft.Kind() {
	case reflect.String:
		return "probe-" + tag
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return float64(7)
	case reflect.Slice:
		if ft.Elem().Kind() == reflect.String {
			return []any{"probe-" + tag}
		}
		return []any{map[string]any{"resource_type": "file_scope", "resource_key": "probe:" + tag}}
	case reflect.Map:
		return map[string]any{"probe": tag}
	}
	t.Fatalf("no probe shape for ClaimResponse field %q of type %s — extend claimProbeValue, "+
		"or this field is silently excluded from the census", tag, ft)
	return nil
}

// fullClaimResponse is a claim response carrying EVERY ClaimResponse field, built
// from the struct.
func fullClaimResponse(t *testing.T) map[string]any {
	t.Helper()
	fields := claimResponseJSONFields(t)
	for tag := range claimResponseRealisticValues {
		if _, ok := fields[tag]; !ok {
			t.Errorf("claimResponseRealisticValues pins %q, which is not a ClaimResponse field — "+
				"stale entry, the exact rot that left \"expires_at\" in the old keep-list", tag)
		}
	}
	payload := map[string]any{}
	for tag, ft := range fields {
		payload[tag] = claimProbeValue(t, tag, ft)
	}
	return payload
}

// claimAgainstFullResponse drives the real pf_claim_work_item against a fake
// aihub that answers with `payload`, and returns the tool result.
func claimAgainstFullResponse(t *testing.T, idem string, payload map[string]any) map[string]any {
	t.Helper()
	newClaimWorkspace(t) // isolates POLYFORGE_WORKSPACE_ROOT and lays down the clone

	wiID, _ := payload["id"].(string)
	if wiID == "" {
		t.Fatal("the probe payload has no id, so the claim URL cannot be built")
	}

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return 200, payload
	})

	result, isErr := claimToolResult(t, f, wiID, idem)
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}
	if len(result) == 0 {
		t.Fatal("pf_claim_work_item returned an empty result — every assertion below would be vacuous")
	}
	return result
}

func claimToolResult(t *testing.T, f *fakeAihub, wiID, idem string) (map[string]any, bool) {
	t.Helper()
	return callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": idem,
	})
}

// TestClaimResultCarriesEveryClaimResponseField is THE gate.
//
// It FAILS on the pre-fix tree, naming `requires_human_session`, `wi_type`, `id`
// and `step_recovery_hint`. That is the point: it is the regression test for a
// projection that dropped the field the caller routes on, not a description of
// code that already worked.
func TestClaimResultCarriesEveryClaimResponseField(t *testing.T) {
	fields := claimResponseJSONFields(t)
	payload := fullClaimResponse(t)
	result := claimAgainstFullResponse(t, "idem-claim-projection-1", payload)

	forwarded := 0
	for tag := range fields {
		if reason, withheld := claimKeysWithheldFromTheModel[tag]; withheld {
			t.Logf("%s: withheld by design — %s", tag, reason)
			continue
		}
		if _, present := result[tag]; !present {
			t.Errorf("the server sent ClaimResponse field %q (%#v) and pf_claim_work_item's result "+
				"does not carry it.\n"+
				"The projection drops it with no error, so the caller cannot tell "+
				"\"absent\" from \"false\"/\"empty\" — and for requires_human_session that decision is "+
				"whether to dispatch an agent unattended or stop and wait for a human. This is "+
				"aihub#388's signature, and the fix is the SHAPE of the projection: make exposure "+
				"the default and deletion the thing that has to be written down (see aihub#281's "+
				"list_wi_slim.go). If this field genuinely must be withheld, add it to "+
				"claimKeysWithheldFromTheModel with the reason.",
				tag, payload[tag])
			continue
		}
		forwarded++
	}
	if forwarded == 0 {
		t.Fatal("not one field was forwarded — this test is measuring nothing")
	}
	t.Logf("%d of %d ClaimResponse fields reached the model; %d withheld by design",
		forwarded, len(fields), len(claimKeysWithheldFromTheModel))
}

// TestClaimResultWithholdsExactlyTheDocumentedKeys is the other direction, and it
// is the half that keeps the fix from introducing the bug it is fixing: widening
// the projection must not start leaking the credential the narrow version existed
// to hold back.
//
// `session_secret` is planted in the server's answer even though the real server
// never sends one — a delete-list forwards by default, so "the server does not
// send it" is not a property this code may rely on.
func TestClaimResultWithholdsExactlyTheDocumentedKeys(t *testing.T) {
	payload := fullClaimResponse(t)
	const plantedSecret = "deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef"
	payload["session_secret"] = plantedSecret
	result := claimAgainstFullResponse(t, "idem-claim-projection-2", payload)

	for tag, reason := range claimKeysWithheldFromTheModel {
		if v, present := result[tag]; present {
			t.Errorf("%q is documented as withheld (%s) but the result carries it as %#v",
				tag, reason, v)
		}
	}

	// Belt and braces on the credential specifically: not just absent under its
	// own key, but absent from the serialised result entirely — a copy under
	// another name, or nested inside a passed-through object, leaks just as well.
	blob, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(blob), plantedSecret) {
		t.Errorf("the session secret appears in the claim result: %s", blob)
	}
}

// TestClaimResultPassesThroughAFieldTheStructDoesNotHaveYet asserts the SHAPE,
// which is the only assertion here that a merely-complete keep-list cannot pass.
//
// A field added to domain.ClaimResponse by a future server must reach the model
// without anybody editing the projection. That is the whole difference between
// this fix and aihub#238's single-field patch, and stating it as an argument in a
// comment is what would rot — so it is stated as a test. Same role as
// TestSlimListWorkItems_KeepsUnknownTopLevelKeys plays for the list projection.
func TestClaimResultPassesThroughAFieldTheStructDoesNotHaveYet(t *testing.T) {
	fields := claimResponseJSONFields(t)
	const future = "some_field_added_after_this_test_was_written"
	if _, ok := fields[future]; ok {
		t.Fatalf("%q is now a real ClaimResponse field; pick another name for this probe", future)
	}

	payload := fullClaimResponse(t)
	payload[future] = "arrived"
	result := claimAgainstFullResponse(t, "idem-claim-projection-3", payload)

	if got, present := result[future]; !present || got != "arrived" {
		t.Errorf("a field the server added and this projection has never heard of did not reach "+
			"the model (got %#v, present=%v).\n"+
			"That is the keep-list shape: exposure requires an edit, so the cheapest outcome of "+
			"adding a field is that it silently disappears — four times already in this package "+
			"(aihub#249/#269/#289 in recall_slim.go, and aihub#388's own four fields here). The "+
			"projection must DELETE named keys and pass everything else, not the reverse. "+
			"Result: %v", got, present, result)
	}
}

// TestClaimResultKeepsTheStateFileAuthoritativeKeys pins what the widening must
// NOT change: the three keys the handler asserts itself rather than relaying.
func TestClaimResultKeepsTheStateFileAuthoritativeKeys(t *testing.T) {
	payload := fullClaimResponse(t)
	payload["attempt_id"] = "ra_projection"
	payload["claim_epoch"] = float64(3)
	result := claimAgainstFullResponse(t, "idem-claim-projection-4", payload)

	if result["ok"] != true {
		t.Errorf("ok is %#v, want true — the claim succeeded", result["ok"])
	}
	for _, tc := range []struct{ key, want string }{
		{"attempt_id", "ra_projection"},
		{"claim_epoch", "3"},
	} {
		if got := fmt.Sprint(result[tc.key]); got != tc.want {
			t.Errorf("%s = %q, want %q — these come from the state file this claim just wrote, "+
				"and every later credential-checked pf_* call authenticates with them",
				tc.key, got, tc.want)
		}
	}
}
