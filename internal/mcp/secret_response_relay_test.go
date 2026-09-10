package mcp_test

// aihub#543 probe wave 2 — the hop-5 claims of the three cards whose responses
// are relayed whole, two of which carry a credential.
//
//	pf_create_api_key:  "`jsonResult`, no projection: the observed keys are
//	                     `key_id` and `raw_key`."
//	                    "🔴 `raw_key` is a live credential in a non-projected
//	                     response, so it lands in the calling agent's transcript."
//	                    "Same property as `pf_rotate_identifier`…"
//	pf_revoke_api_key:  "`jsonResult`, no projection: the single observed key is `ok`."
//	pf_rotate_identifier: the same 🔴 line, from the other side.
//
// 🔴 WHY "NO PROJECTION" NEEDS AN ARM AND WHY THIS ONE IS UNCOMFORTABLE. A
// projection is a keep-list, and the failure it produces is silence: a key the
// server sends and the tool drops looks exactly like a key the server did not
// send. K7/K10 hold the keys a response DOES carry against the corpus and the
// live server, which is the presence direction over an enumerated list — neither
// can see that an UNLISTED key would also come through, and that is what "no
// projection" claims. So the fake returns a key no card lists and the arm
// requires it to arrive.
//
// The uncomfortable half is that on two of these tools the relayed value is a
// credential. The card states that as a property of the surface, and pinning it
// means asserting that a secret is passed through unmodified — which is the
// correct thing to assert about a contract that says so, and the reason the
// cards' Open sections both record that nobody has ruled on whether it should be.
// 🔴 EVERY key value below is a FIXTURE this file invented and the fake handed
// back; no arm here reads a real credential, prints one, or can reach one — the
// fake answers from a handler table with no database behind it.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestSecretReturning|TestBothSecret' -count=1

import (
	"net/http"
	"strings"
	"testing"
)

// secretRelayFixtures are the three tools this arm drives, with the response the
// fake gives each one.
//
// A map of tool names is unavoidable here — the population is "the tools whose
// card claims an unprojected hop 5", and no registry field records that — so the
// two rules for one apply: the out-of-scope tools are named rather than silently
// skipped, and the map is checked BOTH ways. The check is
// `the_secret_cards_are_the_two_this_arm_drives`, the first subtest of the arm that
// USES this list — measured placement, not a preference: while that census lived in
// the Open-section arm next door, mutants M5 and M6 below (deleting the phrase from
// one card, adding it to a third) both came back GREEN against the relay arm they
// are listed under, because nothing the relay arm ran looked at the population at
// all. A census belongs in the arm whose scope it bounds. The third entry here is the
// non-secret control, and it is in the list because its card makes the same
// no-projection claim about a one-key response, which is the shape where a
// projection would be least visible.
var secretRelayFixtures = []struct {
	tool       string
	path       string
	status     int
	documented map[string]any
	// secretKey names the response key carrying a credential, or "" when the
	// tool returns none. It is what the verbatim-relay assertion reads.
	secretKey string
}{
	{
		tool:   createKeyCardName,
		path:   apiKeyAdminPrefix + apiKeyOwner + "/keys",
		status: http.StatusCreated,
		documented: map[string]any{
			"key_id": fixtureKeyID, "raw_key": fixtureRawKey,
		},
		secretKey: "raw_key",
	},
	{
		tool:   revokeKeyCardName,
		path:   apiKeyAdminPrefix + apiKeyOwner + "/keys/" + fixtureKeyID,
		status: http.StatusOK,
		documented: map[string]any{
			"ok": true,
		},
	},
	{
		tool:   rotateCardName,
		path:   "/v1/projects/" + rotateProbeProject + "/rotate_identifier",
		status: http.StatusOK,
		documented: map[string]any{
			"plain": fixtureIdentifier, "prefix": "pi_FIXTURE",
		},
		secretKey: "plain",
	},
}

// secretRelayArgs is the published-only argument set for each tool. Published
// only, deliberately: an unpublished key would make the tool add aihub#389's
// `request_adjusted` to the result, and this arm asserts on what the SERVER's
// keys do rather than on what the echo layer adds.
func secretRelayArgs(tool string) map[string]any {
	switch tool {
	case createKeyCardName:
		return map[string]any{apiKeyUserParam: apiKeyOwner, "name": "probe key"}
	case revokeKeyCardName:
		return map[string]any{apiKeyUserParam: apiKeyOwner, "key_id": fixtureKeyID}
	default:
		return map[string]any{"name": rotateProbeProject}
	}
}

// unlistedServerKey is the key no card lists. Its whole job is to be absent from
// every keep-list a projection could be written with, so that a projection
// appearing here is red rather than invisible.
const unlistedServerKey = "aihub543_unlisted_server_key"

// TestSecretReturningToolsRelayTheServerResponseUnprojected drives all three and
// asserts nothing is dropped.
//
// MUTANTS (applied to this tree; the verdict is what RAN, not what was expected):
//
//	M1 enforcement: project pf_create_api_key's result down to {key_id, raw_key}
//	                                             RED  relays_every_server_key/pf_create_api_key
//	M2 enforcement: project pf_revoke_api_key's result down to {ok}
//	                                             RED  relays_every_server_key/pf_revoke_api_key
//	M3 enforcement: redact the plaintext at hop 5 — the change the cards' Open
//	   sections say nobody has ruled on
//	                                             RED  relays_the_credential_verbatim on both
//	                                                  secret tools. 🔴 That is this arm
//	                                                  working, not an argument against
//	                                                  the redaction: the cards say the
//	                                                  plaintext is relayed, so a redaction
//	                                                  is a contract change and has to move
//	                                                  the cards with it
//	M4 enforcement: truncate the relayed plaintext to its prefix
//	                                             RED  relays_the_credential_verbatim
//	M5 publication: delete the "secret-returning tool" line from the rotate card
//	                                             RED  the_secret_cards_are_the_two_this_arm_drives
//	M6 publication: add that line to a third card
//	                                             RED  the_secret_cards_are_the_two_this_arm_drives
//	G1 control:     add a fourth key to a fixture response
//	                                           GREEN  the arm requires every server key to
//	                                                  arrive, not a fixed count
func TestSecretReturningToolsRelayTheServerResponseUnprojected(t *testing.T) {
	if len(secretRelayFixtures) < 3 {
		t.Fatalf("this arm drives %d tool(s); the three cards it is named for need three. A "+
			"shorter list is a smaller measurement than the doc comment claims, which is the "+
			"failure every floor in this package exists to refuse", len(secretRelayFixtures))
	}

	t.Run("the_secret_cards_are_the_two_this_arm_drives", func(t *testing.T) {
		carriers := cardsContaining(t, "secret-returning tool")
		want := map[string]bool{createKeyCardName: true, rotateCardName: true}
		if len(carriers) != len(want) {
			t.Errorf("%d card(s) use the phrase \"secret-returning tool\": %v. This arm's "+
				"population is the tools whose card says their hop 5 carries a credential "+
				"unprojected; a third one is a third tool relaying a secret with nothing "+
				"driving it, and a missing one is a claim that lost its arm.", len(carriers), carriers)
		}
		for _, got := range carriers {
			if !want[got] {
				t.Errorf("%s uses the phrase and is not among the tools this arm drives. Add it "+
					"to secretRelayFixtures — an unlisted secret-returning tool is exactly the "+
					"case a hand-written map skips.", got)
			}
		}
	})

	t.Run("relays_every_server_key", func(t *testing.T) {
		for _, fx := range secretRelayFixtures {
			t.Run(fx.tool, func(t *testing.T) {
				f := newFakeAihub(t)
				payload := map[string]any{unlistedServerKey: "relayed-verbatim"}
				for k, v := range fx.documented {
					payload[k] = v
				}
				f.on(fx.path, func(map[string]any) (int, any) { return fx.status, payload })

				result, isErr := callTool(t, f, fx.tool, secretRelayArgs(fx.tool))
				if isErr {
					t.Fatalf("%s failed: %v — a failed call has no response for the assertions "+
						"below to be about", fx.tool, result)
				}
				if len(f.recorded()) == 0 {
					t.Fatalf("%s made no request, so the response above came from nowhere", fx.tool)
				}

				for key, want := range fx.documented {
					got, present := result[key]
					if !present {
						t.Errorf("%s's result carries no %q (it carries %v). The card lists that key "+
							"at hop 5.", fx.tool, key, sortedBodyKeys(result))
						continue
					}
					if got != want {
						t.Errorf("%s's result carries %q = %#v and the server sent %#v — an "+
							"unprojected relay changes no value on the way through",
							fx.tool, key, got, want)
					}
				}
				if got, present := result[unlistedServerKey]; !present {
					t.Errorf("%s dropped %q, a key the server sent and no card lists (result: %v). "+
						"That is a PROJECTION, and the card says there is none. A keep-list here "+
						"makes a new server field invisible to every caller until somebody "+
						"remembers to widen it, which is the aihub#387 shape.",
						fx.tool, unlistedServerKey, sortedBodyKeys(result))
				} else if got != "relayed-verbatim" {
					t.Errorf("%s relayed %q as %#v", fx.tool, unlistedServerKey, got)
				}
			})
		}
	})

	t.Run("relays_the_credential_verbatim", func(t *testing.T) {
		driven := 0
		for _, fx := range secretRelayFixtures {
			if fx.secretKey == "" {
				continue
			}
			driven++
			t.Run(fx.tool, func(t *testing.T) {
				fixture, _ := fx.documented[fx.secretKey].(string)
				if fixture == "" {
					t.Fatalf("%s's fixture response carries no %q string, so there is no value to "+
						"follow through the relay", fx.tool, fx.secretKey)
				}
				f := newFakeAihub(t)
				f.on(fx.path, func(map[string]any) (int, any) { return fx.status, fx.documented })
				result, isErr := callTool(t, f, fx.tool, secretRelayArgs(fx.tool))
				if isErr {
					t.Fatalf("%s failed: %v", fx.tool, result)
				}
				got, _ := result[fx.secretKey].(string)
				if got != fixture {
					t.Errorf("%s's %q arrived as a value the server did not send. The card's red "+
						"line says the plaintext reaches the caller unchanged, which is what makes "+
						"\"store it securely\" the caller's job and \"it lands in the transcript\" a "+
						"property of the surface. If this changed on purpose, the two cards and the "+
						"published description have to change with it — the caller currently has "+
						"no second chance to read the value.", fx.tool, fx.secretKey)
				}
			})
		}
		if driven != 2 {
			t.Errorf("%d of the fixtures name a secret response key, want 2 (the two cards "+
				"carrying the red line). A list that lost one would report green having checked "+
				"the relay on the tool that returns no credential at all.", driven)
		}
	})
}

// TestBothSecretCardsRecordTheSameUnadjudicatedOpenItem is the cross-card half:
// the create card's "the same open item `pf_rotate_identifier` carries" and the
// rotate card's own Open bullet.
//
// 🔴 This is a claim about a RELATION between two cards, which is the gap K9 and
// the two `description_sha256` fields structurally cannot cover: each pins a
// string to itself, so the two Open sections can drift apart with every hash
// unchanged and every gate green. K8/K11 police each Open bullet's FORM — dated,
// falsifiable — and say nothing about two cards agreeing.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M7 publication: delete the rotate card's Open bullet
//	                                             RED  both_open_sections_carry_it
//	M8 publication: reword the create card's bullet to claim the question IS
//	    adjudicated                              RED  both_open_sections_carry_it
//	G2 control:     reword either card's hop 4  GREEN  the arm reads the Open sections
//
// ⚠️ The population census that decides WHICH cards are the secret ones is not here;
// it is the first subtest of TestSecretReturningToolsRelayTheServerResponseUnprojected,
// which is the arm it bounds. It was here first, and mutants M5/M6 of that arm went
// green because of it.
func TestBothSecretCardsRecordTheSameUnadjudicatedOpenItem(t *testing.T) {
	t.Run("both_open_sections_carry_it", func(t *testing.T) {
		for _, card := range []string{createKeyCardName, rotateCardName} {
			t.Run(card, func(t *testing.T) {
				open := cardOpenSection(t, card)
				// Whitespace-collapsed before the phrase checks: a markdown line break is a
				// rendering artifact, and both cards wrap this bullet at a different word.
				flat := strings.Join(strings.Fields(open), " ")
				for _, phrase := range []string{
					"secret-returning tool",
					"hop 5",
					"not covered by any adjudicated row",
				} {
					if !strings.Contains(flat, phrase) {
						t.Errorf("%s's `## Open` section does not say %q:\n%s\nBoth cards record the "+
							"same undecided question, and the create card says so in as many words "+
							"— an Open section that drops it leaves one card claiming the other "+
							"carries an item it does not.", card, phrase, open)
					}
				}
			})
		}
	})
}

// cardsContaining returns the cards whose text carries a phrase, which is how the
// population of a cross-card claim is enumerated.
func cardsContaining(t *testing.T, phrase string) []string {
	t.Helper()
	cards := cardNamesOnDisk(t)
	if len(cards) < 40 {
		t.Fatalf("only %d card(s) were read; the repo holds all 45. A census over a short list "+
			"reports \"no other card says this\" about cards it never opened.", len(cards))
	}
	var out []string
	for _, card := range cards {
		if strings.Contains(cardText(t, card), phrase) {
			out = append(out, card)
		}
	}
	return out
}
