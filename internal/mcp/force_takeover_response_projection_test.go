package mcp_test

// aihub#422 — the pf_force_takeover RESPONSE projection, as a mechanism.
//
// The takeover handler built its answer with a KEEP-LIST:
//
//	safeResult := map[string]any{
//	    "prior_attempt_id": …, "prior_actor_display": …,
//	    "new_attempt_id": …, "new_claim_epoch": …, "ok": …,
//	}
//
// so every field of domain.ForceTakeoverResponse that nobody remembered to list
// was dropped in silence. Three were: `id`, `slug` and `project` — the canonical
// identity aihub#149 ADDED to that struct precisely so a slug-addressed takeover
// could be keyed correctly. The handler reads all three (they become the state
// file's key, Slug and Project) and then told the model none of them, so the
// answer withheld the identity the call had just established — a caller that took
// over by slug could not name the work_items.id it now owns, because the only
// copy is in a state file the model does not read.
//
// ⚠️ Not because a downstream tool demands it: pf_read_events resolves id-or-slug
// since aihub#343 and pf_recall since aihub#363, so the "both return nothing for
// a slug" line still carried by pf_get_step's tool description is stale. Under a
// delete-list nobody has to prove a field is needed — that is the whole shape.
//
// ─── Why the fix is the SHAPE, not the three names ──────────────────────────
//
// This is the fifth-plus instance of one pattern: recall_slim.go's keep-list
// silently swallowed a newly-added field three times (`total` aihub#249, the
// truncation pair aihub#269, `unmatched_types` aihub#289) and aihub#388 found
// the same shape in the CLAIM handler, registered earlier in the same file.
// #388's own header quotes THIS handler's "v1.21 ownership-only: no expires_at; do not
// surface that field" comment while fixing the neighbour, and left this keep-list
// standing. aihub#419's G3 arm then found it mechanically by planting an unknown
// field in the server's answer: measured on this branch, 42 of the 46 inspected
// tool results forwarded it before this change and 43 after — the remaining three
// compose their own result and say so in the gate.
//
// So the gate below is quantified over domain.ForceTakeoverResponse's fields
// rather than over the three names, and one test asserts the SHAPE directly: a
// key the struct does not have yet must still arrive. A keep-list cannot pass
// that however complete it is today, which is what makes this a fix rather than
// a fourth patch.
//
// ─── Why the assertions drive the real tool ─────────────────────────────────
//
// Same reason claim_response_projection_test.go gives: a test on
// slimForceTakeoverResult alone would be the aihub#309 trap, where a mutant one
// layer away (`if false { safeResult = slimForceTakeoverResult(...) }` at the
// call site) leaves every pure-function test green while the defect stands.
// Everything here goes through the registered MCP tool against a fake aihub, so
// what is asserted is the observable output of pf_force_takeover.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestForceTakeoverResult -v

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// forceTakeoverKeysWithheldFromTheModel is the SPECIFICATION of what the takeover
// response may withhold: a key here must not reach the model, everything else
// must.
//
// It states the rule here rather than reading the production delete-list, for the
// reason claimKeysWithheldFromTheModel gives: reading the implementation's own
// list would make the test agree with whatever the implementation happens to
// say. Stated independently, a key withheld in production without a line here
// goes red, and a line here for a key that is in fact forwarded goes red too.
//
// Each entry needs a reason, and the reason has to be TRUE of this code — an
// entry justified by a fact that does not hold is a keep-list wearing a
// delete-list's clothes. Both halves of the one below were read out of the source
// before it was written: the secret is minted in tools_lifecycle.go's handler by
// generateSessionSecret() and persisted by config.WriteStateFile at mode 0600
// (internal/config/state.go:77), and domain.ForceTakeoverResponse.NewSessionSecret
// is tagged `json:"-"`, so today's server cannot send it back.
var forceTakeoverKeysWithheldFromTheModel = map[string]string{
	"session_secret": "a live credential minted IN THIS PROCESS (generateSessionSecret in the " +
		"pf_force_takeover handler) and written to the state file at mode 0600; putting it in a " +
		"tool result would paste it into a transcript. domain.ForceTakeoverResponse tags " +
		"NewSessionSecret `json:\"-\"` — the client supplied the plaintext, so the server never " +
		"echoes it — which is exactly why the delete is defence in depth rather than a no-op: a " +
		"delete-list forwards by default, and the server versions independently of this binary.",
}

// forceTakeoverRealisticValues pins the fields whose VALUE the handler acts on,
// so the takeover stays on its happy path: `id` becomes the state file's key and
// the two credential fields are what the file records.
//
// Every key here must be a real ForceTakeoverResponse field — asserted below, so
// this map cannot rot into the `expires_at` shape it exists to replace.
var forceTakeoverRealisticValues = map[string]any{
	"id":              ftCanonicalID,
	"slug":            "aihub#422",
	"project":         "aihub",
	"new_attempt_id":  "ra_forced_422",
	"new_claim_epoch": float64(11),
}

const (
	// ftCanonicalID is the work_items.id the fake server echoes, and therefore the
	// key the state file is written under.
	ftCanonicalID = "wi_ft422"
	// ftReason is required by the tool schema; the server validates it, not the
	// handler, so any non-empty string keeps the call on its happy path.
	ftReason = "the holder went stale"
)

// forceTakeoverResponseJSONFields returns domain.ForceTakeoverResponse's json tag
// -> field type. Reflection, not a hand-written list: a field added tomorrow is
// covered the day it is added, which is the entire point of the exercise.
func forceTakeoverResponseJSONFields(t *testing.T) map[string]reflect.Type {
	t.Helper()
	typ := reflect.TypeOf(domain.ForceTakeoverResponse{})
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			// NewSessionSecret. Not on the wire at all, so it is not a field this
			// projection can forward or withhold.
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	if len(out) == 0 {
		t.Fatal("domain.ForceTakeoverResponse has no json fields at all — the reflection is " +
			"broken, not the struct, and every assertion below would be vacuous")
	}
	return out
}

// forceTakeoverProbeValue invents a wire value for one field type. Bools probe as
// `false` for the same reason claimProbeValue does: a truthiness-based copy
// (`if v := result[k]; v != nil && v != false`) would silently eat it, and
// probing `true` would leave that bug green.
func forceTakeoverProbeValue(t *testing.T, tag string, ft reflect.Type) any {
	t.Helper()
	if v, ok := forceTakeoverRealisticValues[tag]; ok {
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
		return []any{"probe-" + tag}
	case reflect.Map:
		return map[string]any{"probe": tag}
	}
	t.Fatalf("no probe shape for ForceTakeoverResponse field %q of type %s — extend "+
		"forceTakeoverProbeValue, or this field is silently excluded from the census", tag, ft)
	return nil
}

// fullForceTakeoverResponse is a takeover response carrying EVERY
// ForceTakeoverResponse field, built from the struct.
func fullForceTakeoverResponse(t *testing.T) map[string]any {
	t.Helper()
	fields := forceTakeoverResponseJSONFields(t)
	for tag := range forceTakeoverRealisticValues {
		if _, ok := fields[tag]; !ok {
			t.Errorf("forceTakeoverRealisticValues pins %q, which is not a ForceTakeoverResponse "+
				"field — stale entry, the exact rot that left \"expires_at\" in the old keep-list", tag)
		}
	}
	payload := map[string]any{}
	for tag, ft := range fields {
		payload[tag] = forceTakeoverProbeValue(t, tag, ft)
	}
	return payload
}

// takeoverAgainstFullResponse drives the real pf_force_takeover against a fake
// aihub that answers with `payload`, and returns the tool result.
//
// The wi is addressed by SLUG, which is the case the dropped fields matter in:
// the caller then has no canonical id of its own, and `id` is the only thing in
// the answer that could give it one — the other copy goes to a state file on
// disk, which the model never sees.
func takeoverAgainstFullResponse(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	newResolveWorkspace(t) // isolates POLYFORGE_WORKSPACE_ROOT

	slug, _ := payload["slug"].(string)
	if slug == "" {
		t.Fatal("the probe payload has no slug, so the takeover URL cannot be built")
	}

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+slug+"/force_takeover", func(map[string]any) (int, any) {
		return 200, payload
	})

	result, isErr := callToolBounded(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": slug, "reason": ftReason,
	}, 20*time.Second)
	if isErr {
		t.Fatalf("pf_force_takeover failed: %v", result)
	}
	if len(result) == 0 {
		t.Fatal("pf_force_takeover returned an empty result — every assertion below would be vacuous")
	}
	return result
}

// TestForceTakeoverResultCarriesEveryResponseField is THE gate.
//
// It FAILS on the pre-fix tree, naming `id`, `slug` and `project`. That is the
// point: it is the regression test for a projection that dropped the canonical
// identity aihub#149 put in the response on purpose, not a description of code
// that already worked.
func TestForceTakeoverResultCarriesEveryResponseField(t *testing.T) {
	fields := forceTakeoverResponseJSONFields(t)
	payload := fullForceTakeoverResponse(t)
	result := takeoverAgainstFullResponse(t, payload)

	forwarded := 0
	for tag := range fields {
		if reason, withheld := forceTakeoverKeysWithheldFromTheModel[tag]; withheld {
			t.Logf("%s: withheld by design — %s", tag, reason)
			continue
		}
		if _, present := result[tag]; !present {
			t.Errorf("the server sent ForceTakeoverResponse field %q (%#v) and "+
				"pf_force_takeover's result does not carry it.\n"+
				"The projection drops it with no error, so a caller that addressed the wi by slug "+
				"never learns the canonical work_items.id this takeover just keyed its state file "+
				"on. The fix is the SHAPE of the projection: make exposure the "+
				"default and deletion the thing that has to be written down (aihub#281's "+
				"list_wi_slim.go, aihub#388's claim_response_slim.go). If this field genuinely must "+
				"be withheld, add it to forceTakeoverKeysWithheldFromTheModel with the reason.",
				tag, payload[tag])
			continue
		}
		forwarded++
	}
	if forwarded == 0 {
		t.Fatal("not one field was forwarded — this test is measuring nothing")
	}
	t.Logf("%d of %d ForceTakeoverResponse fields reached the model; %d withheld by design",
		forwarded, len(fields), len(forceTakeoverKeysWithheldFromTheModel))
}

// TestForceTakeoverResultWithholdsExactlyTheDocumentedKeys is the other
// direction, and it is the half that keeps the fix from committing the bug it is
// fixing: widening the projection must not start leaking the credential the
// narrow version existed to hold back.
//
// `session_secret` is planted in the server's answer even though the real server
// never sends one — a delete-list forwards by default, so "the server does not
// send it" is a property of the other side of an HTTP boundary and not something
// this code may rely on.
func TestForceTakeoverResultWithholdsExactlyTheDocumentedKeys(t *testing.T) {
	payload := fullForceTakeoverResponse(t)
	const plantedSecret = "deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef"
	payload["session_secret"] = plantedSecret
	result := takeoverAgainstFullResponse(t, payload)

	for tag, reason := range forceTakeoverKeysWithheldFromTheModel {
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
		t.Errorf("the planted session secret appears in the takeover result: %s", blob)
	}

	// And the secret this process MINTED for the takeover — the one that is
	// actually live — must not be in the result either. It is not in the server's
	// answer at all, so nothing above can see it; it is read back out of the state
	// file the handler just wrote.
	sf, err := config.ReadStateFile(ftCanonicalID)
	if err != nil {
		t.Fatalf("no state file under %s: %v — the takeover did not complete, so this "+
			"assertion would be vacuous", ftCanonicalID, err)
	}
	if sf.SessionSecret == "" {
		t.Fatal("the state file records no session_secret, so searching the result for it " +
			"would pass on an empty needle")
	}
	if strings.Contains(string(blob), sf.SessionSecret) {
		t.Errorf("the LIVE session secret this process minted appears in the takeover result: %s", blob)
	}
}

// TestForceTakeoverResultPassesThroughAFieldTheStructDoesNotHaveYet asserts the
// SHAPE, which is the only assertion here that a merely-complete keep-list cannot
// pass.
//
// A field added to domain.ForceTakeoverResponse by a future server must reach the
// model without anybody editing the projection. Stating that as an argument in a
// comment is what would rot, so it is stated as a test — the same role
// TestClaimResultPassesThroughAFieldTheStructDoesNotHaveYet plays next door, and
// the same property aihub#419's G3 arm quantifies over every tool.
func TestForceTakeoverResultPassesThroughAFieldTheStructDoesNotHaveYet(t *testing.T) {
	fields := forceTakeoverResponseJSONFields(t)
	const future = "some_field_added_after_this_test_was_written"
	if _, ok := fields[future]; ok {
		t.Fatalf("%q is now a real ForceTakeoverResponse field; pick another name for this probe", future)
	}

	payload := fullForceTakeoverResponse(t)
	payload[future] = "arrived"
	result := takeoverAgainstFullResponse(t, payload)

	if got, present := result[future]; !present || got != "arrived" {
		t.Errorf("a field the server added and this projection has never heard of did not reach "+
			"the model (got %#v, present=%v).\n"+
			"That is the keep-list shape: exposure requires an edit, so the cheapest outcome of "+
			"adding a field is that it silently disappears — five times already in this package "+
			"(aihub#249/#269/#289 in recall_slim.go, aihub#388's four claim fields, and this "+
			"handler's own three). The projection must DELETE named keys and pass everything "+
			"else, not the reverse. Result: %v", got, present, result)
	}
}

// TestForceTakeoverResultReportsTheCredentialsTheStateFileHOLDS pins what the
// widening must NOT change: the two credential keys are ASSERTED from the state
// file this call just wrote, not relayed from the server's answer.
//
// The two are equal on every ordinary response, which is why the discriminating
// arm sends an epoch the handler cannot parse. tools_lifecycle.go's type switch
// accepts float64 and int64 only, so a STRING epoch leaves sf.ClaimEpoch at 0 —
// and 0 is then what the state file holds and what every later credential-checked
// pf_* call will send. A relayed "13" would tell the model a number this machine
// will never use, which is the one reading under which the caller cannot explain
// the 409 it is about to get. Without this arm the test is vacuous: a pure
// pass-through and an asserted value are byte-identical on a well-formed
// response.
func TestForceTakeoverResultReportsTheCredentialsTheStateFileHolds(t *testing.T) {
	t.Run("well-formed response", func(t *testing.T) {
		payload := fullForceTakeoverResponse(t)
		result := takeoverAgainstFullResponse(t, payload)

		sf, err := config.ReadStateFile(ftCanonicalID)
		if err != nil {
			t.Fatalf("no state file under %s: %v", ftCanonicalID, err)
		}
		if got := fmt.Sprint(result["new_attempt_id"]); got != sf.AttemptID {
			t.Errorf("new_attempt_id = %q, state file holds %q — the result must state the "+
				"credentials later pf_* calls will authenticate with", got, sf.AttemptID)
		}
		if got := fmt.Sprint(result["new_claim_epoch"]); got != fmt.Sprint(sf.ClaimEpoch) {
			t.Errorf("new_claim_epoch = %q, state file holds %d", got, sf.ClaimEpoch)
		}
		// `ok` is RELAYED, not asserted — unlike the claim handler, which sets it
		// itself. The probe sends false precisely so a projection that manufactures
		// `"ok": true` fails here: on this route the server's own OK field is the
		// only statement of success, and overwriting it would report a takeover the
		// server did not confirm.
		if got, want := result["ok"], payload["ok"]; got != want {
			t.Errorf("ok = %#v, want %#v — the server's value, relayed", got, want)
		}
	})

	t.Run("epoch the handler cannot parse", func(t *testing.T) {
		payload := fullForceTakeoverResponse(t)
		payload["new_claim_epoch"] = "13" // a string: neither arm of the handler's type switch
		result := takeoverAgainstFullResponse(t, payload)

		sf, err := config.ReadStateFile(ftCanonicalID)
		if err != nil {
			t.Fatalf("no state file under %s: %v", ftCanonicalID, err)
		}
		if sf.ClaimEpoch != 0 {
			t.Fatalf("the state file recorded epoch %d from a string — the handler's type switch "+
				"now parses strings, so this arm no longer separates asserted from relayed; "+
				"give it an input the handler still cannot parse", sf.ClaimEpoch)
		}
		if got := fmt.Sprint(result["new_claim_epoch"]); got != "0" {
			t.Errorf("new_claim_epoch = %q, want 0 — the value the state file holds and every "+
				"later pf_* call will send. %q is the server's unparsed string relayed straight "+
				"through, which tells the model a credential this machine does not have",
				got, got)
		}
	})
}
