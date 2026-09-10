package mcp_test

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_api_key.md` and
// `docs/mcp-cards/pf_revoke_api_key.md` claims about who the tools are for, what
// leaves the process, and what the surface as a whole offers.
//
//	"**Returns the plain key once — store it securely.**" `user_id` here names the
//	 key's **owner**, which is one of the three identities §6.2 T2-18 says every
//	 `user_id`-shaped parameter must disambiguate."
//	"`user_id` is the path segment; `name` and `project_scope` are body fields."
//	""Revoke an API key (**admin only**)." `user_id` names the key's **owner** — the
//	 same identity `pf_create_api_key`'s does…"
//	"`key_id` is the id `pf_create_api_key` returned; the plaintext is never
//	 accepted here, which is why revocation does not need the secret."
//	"There is no un-revoke, and no listing of keys on this surface…"
//	"**§6.2 T2-18** — the identity `user_id` names is stated above."
//
// 🔴 SECRETS. Every key value in this file is a FIXTURE the fake server invents;
// no arm here reads, prints or stores a real credential, and none can — the fake
// aihub answers from a handler table and never reaches a database. The one arm
// that asserts a secret is relayed verbatim lives next door in
// `internal/mcp/secret_response_relay_test.go` and uses the same fixtures.
//
// 🔴 WHY THE EXISTING GATES SEE NONE OF THIS. K9 checks a card's verbatim quotes
// only inside a hop 0-1 TABLE CELL, and both descriptions are quoted in PROSE.
// The universal gate drives each published parameter and asserts it arrives — it
// cannot tell a path segment from a body field, which is the whole content of the
// hop 2-3 sentences, and it says nothing about a parameter that is absent by
// design. And "the same identity" is a relation between two cards, which no
// per-card gate holds.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestApiKey|TestTheApiKey' -count=1

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	createKeyCardName = "pf_create_api_key"
	revokeKeyCardName = "pf_revoke_api_key"

	// apiKeyOwner and the two fixture ids below are the only key-shaped values in
	// this file, and none of them is a credential: the fake aihub hands them back
	// because a handler in this file told it to.
	apiKeyOwner       = "u_probe_owner"
	fixtureKeyID      = "k_FIXTURE0"
	fixtureRawKey     = "pf_k1_FIXTURE_NOT_A_REAL_KEY"
	apiKeyAdminPrefix = "/v1/admin/users/"
)

// cardText returns a card's whole text with whitespace collapsed, which is what
// a reader of the rendered card sees: the quotes below wrap across line breaks.
func cardText(t *testing.T, card string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-cards", card+".md"))
	if err != nil {
		t.Fatalf("read the %s card: %v — the card is one side of every comparison in this "+
			"file, so a missing one is a failure and not an empty pass", card, err)
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

// cardEmphasis strips markdown bold, which the cards use to mark the clause they
// are drawing attention to inside an otherwise verbatim quote.
var cardEmphasis = regexp.MustCompile(`\*\*([^*]+)\*\*`)

// cardQuoteOpening returns the card's verbatim quote of a published description,
// found by its opening words rather than by its position.
//
// 🔴 The trailing sentence period is trimmed. Both cards close the quote with
// `."` in the US convention — pf_revoke_api_key's live description carries no
// final period at all — so an exact-equality arm would report a punctuation
// convention as contract drift. Trimming ONE trailing period is the narrowest
// normalisation that admits both cards, and it is stated here rather than done
// quietly because it is the only place this arm is not byte-exact.
func cardQuoteOpening(t *testing.T, card, opening string) string {
	t.Helper()
	flat := cardText(t, card)
	re := regexp.MustCompile(`"(` + regexp.QuoteMeta(opening) + `[^"]*)"`)
	m := re.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("the %s card quotes no published sentence opening %q. Either the paragraph "+
			"was rewritten — in which case this arm is comparing the tool against nothing — or "+
			"the claim it held is gone and this file should go with it.", card, opening)
	}
	quote := strings.TrimSpace(cardEmphasis.ReplaceAllString(m[1], "$1"))
	quote = strings.TrimSuffix(quote, ".")
	if len(quote) < 20 {
		t.Fatalf("the %s card's quote is %q — too short to be a published description, so the "+
			"extraction is broken rather than the tool", card, quote)
	}
	return quote
}

// serverRouterSource is internal/server's router file, read for the route-group
// question below.
func serverRouterSource(t *testing.T) string {
	t.Helper()
	return readFileForArm(t, filepath.Join("..", "server", "router.go"),
		"the file registering the admin route group")
}

// TestApiKeyToolsPublishTheOwnersIdentityUnderTheAdminGroup is the hop 0-1 half
// of both cards.
//
// Three claims, none implying another: each card quotes its tool's description
// verbatim (including the admin-only notice and, on creation, the handling
// instruction); `user_id` on both tools names the key's OWNER, which is what the
// wire says by making it the owner's path segment rather than a filter; and
// "admin only" is a route-group fact, not a wish.
//
// The identity claim is asserted on the WIRE rather than on the description's
// wording, because "names the owner" is what T2-18 asks each card to state and a
// description can say it while the request filters on something else. The path
// `/v1/admin/users/<user_id>/keys` is a collection BELONGING to that user; a
// filter would be a query parameter or a body field, which is what the second
// arm in this file measures.
//
// MUTANTS (applied to this tree; the verdict is what RAN, not what was expected):
//
//	M1 enforcement: drop " (admin only)" from pf_revoke_api_key's Description
//	                                             RED  each_card_quotes_its_tool_verbatim/pf_revoke_api_key
//	M2 enforcement: move the two key routes off the admin group onto v1
//	                                             RED  admin_only_is_a_route_group
//	M3 enforcement: create the admin group without RequireAdmin()
//	                                             RED  admin_only_is_a_route_group
//	M4 enforcement: rename the create tool's `user_id` parameter to `owner_id`
//	                                             RED  both_tools_publish_user_id
//	M5 publication: reword the create card's quote (drop "store it securely")
//	                                             RED  each_card_quotes_its_tool_verbatim/pf_create_api_key
//	   🔴 GREEN on this arm's FIRST version, which asked HasPrefix: a truncated quote
//	   is a prefix of the real description, so a card could quote the opening clause
//	   and drop the handling instruction with nothing to say so. The comparison is an
//	   equality because of this run.
//	M5b publication: widen the create card's quote with a clause the tool does not
//	    publish — the other direction an equality closes
//	                                             RED  each_card_quotes_its_tool_verbatim/pf_create_api_key
//	M6 publication: delete the revoke card's quoted sentence
//	                                             RED  the extraction floor in cardQuoteOpening
//	G1 control:     reword an unrelated sentence in either card
//	                                           GREEN  each quote is found by its opening
//	                                                  words, not by a position
func TestApiKeyToolsPublishTheOwnersIdentityUnderTheAdminGroup(t *testing.T) {
	quotes := map[string]string{
		createKeyCardName: cardQuoteOpening(t, createKeyCardName, "Create an API key for a user"),
		revokeKeyCardName: cardQuoteOpening(t, revokeKeyCardName, "Revoke an API key"),
	}

	t.Run("each_card_quotes_its_tool_verbatim", func(t *testing.T) {
		for tool, quote := range quotes {
			t.Run(tool, func(t *testing.T) {
				// 🔴 EQUALITY, NOT HasPrefix, AND MUTANT M5 IS WHY. The first version of this
				// subtest asked whether the live description STARTED WITH the card's quote,
				// which is satisfied by a card that quotes only the opening clause: rewriting
				// the create card's quote to "…Returns the plain key once." — dropping "store
				// it securely" — came back GREEN. A card presents that string as the whole
				// description a caller is shown, so a quote that is a prefix of it is a card
				// telling a reader less than the tool says, silently. Both sides get one
				// trailing period trimmed (see cardQuoteOpening) and are then compared whole.
				live := strings.TrimSuffix(strings.TrimSpace(publishedTool(t, tool).Description), ".")
				if live != quote {
					t.Errorf("%s publishes %q and its card quotes %q.\nThe card presents that "+
						"string as the whole description a caller is shown; where the two differ — "+
						"including where the quote merely STOPS EARLY — the card is telling a "+
						"reader something other than what the tool says, and the admin-only notice "+
						"and the handling instruction both live inside it.",
						tool, live, quote)
				}
				if !strings.Contains(strings.ToLower(live), "admin only") {
					t.Errorf("%s's description no longer says \"admin only\": %q. Both cards lead "+
						"with that notice, and it is the only warning at hop 1 that a non-admin "+
						"caller will be refused.", tool, live)
				}
			})
		}
	})

	t.Run("the_create_card_carries_the_handling_instruction", func(t *testing.T) {
		// The half unique to creation: the plaintext comes back ONCE. Asserted
		// separately because it is the claim the Open sections of both secret cards
		// are about, and because a description that kept "admin only" and lost this
		// would satisfy the arm above.
		live := publishedTool(t, createKeyCardName).Description
		if !strings.Contains(live, "once") || !strings.Contains(live, "securely") {
			t.Errorf("pf_create_api_key's description is %q and no longer tells the caller the "+
				"plain key is returned once and must be stored securely. There is no second "+
				"read: the server keeps a hash, so a caller who did not save it has to create "+
				"another key.", live)
		}
	})

	t.Run("both_tools_publish_user_id", func(t *testing.T) {
		for _, tool := range []string{createKeyCardName, revokeKeyCardName} {
			props := publishedParamDescriptions(t, tool)
			desc, ok := props[apiKeyUserParam]
			if !ok {
				t.Errorf("%s publishes no %q parameter (it publishes %v). Both cards state which "+
					"of the three T2-18 identities it names; an absent parameter makes that "+
					"statement about nothing.", tool, apiKeyUserParam, sortedPropNames(props))
				continue
			}
			if strings.TrimSpace(desc) == "" {
				t.Errorf("%s's %q parameter publishes no description. T2-18 asks each such "+
					"parameter to say which identity it filters, and an empty string says nothing.",
					tool, apiKeyUserParam)
			}
		}
	})

	t.Run("admin_only_is_a_route_group", func(t *testing.T) {
		src := serverRouterSource(t)
		if !strings.Contains(src, `v1.Group("/admin", RequireAdmin())`) {
			t.Error("internal/server/router.go no longer creates the /admin group with " +
				"RequireAdmin(). Both cards publish \"admin only\", and that middleware is the " +
				"whole enforcement behind the phrase — without it the notice is a comment.")
		}
		for _, route := range []string{
			`admin.POST("/users/:id/keys"`,
			`admin.DELETE("/users/:id/keys/:key_id"`,
		} {
			if !strings.Contains(src, route) {
				t.Errorf("internal/server/router.go does not register %s on the admin group. A "+
					"key route registered anywhere else does not pass RequireAdmin, and both "+
					"cards tell a caller that it does.", route)
			}
		}
	})
}

// apiKeyUserParam is the parameter both cards disambiguate.
const apiKeyUserParam = "user_id"

// TestApiKeyWireShapePutsTheOwnerInThePathAndTheRestInTheBody is the hop 2-3
// sentence of each card, measured on an observed request.
//
// 🔴 A SOURCE SCAN CANNOT ANSWER THIS. "path segment" versus "body field" is a
// property of the request that leaves the process; a handler that read `user_id`
// out of the map and ALSO left it in the body would pass every source check for
// "the path is built from user_id" while sending an argument the card says is
// not sent. So the verdict comes from the recorder.
//
// The revocation half closes with the round trip the card describes: the `key_id`
// creation returns is the value revocation takes, and it travels as a path
// segment. No plaintext is involved anywhere in that loop, which is the card's
// "revocation does not need the secret" stated as something that can fail.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M7 enforcement: stop deleting `user_id` from the create body
//	                                             RED  creation_sends_the_owner_only_in_the_path
//	M8 enforcement: build the create body from the two published names instead of
//	    copying the map                          RED  creation_forwards_the_remaining_arguments
//	M9 enforcement: send a body on the revoke request
//	                                             RED  revocation_sends_no_body_at_all
//	M10 enforcement: swap the two revoke path segments
//	                                             RED  revocation_takes_the_id_creation_returned
//	M11 enforcement: publish a third parameter on pf_revoke_api_key
//	                                             RED  revocation_publishes_exactly_two_parameters
//	M12 publication: add a `raw_key` parameter to pf_revoke_api_key's schema — the
//	    shape the card denies                    RED  revocation_accepts_no_plaintext
//	G2 control:     change the fixture key id     GREEN  the round trip reads the id out of
//	                                                  the creation response rather than
//	                                                  assuming it
func TestApiKeyWireShapePutsTheOwnerInThePathAndTheRestInTheBody(t *testing.T) {
	t.Run("creation_sends_the_owner_only_in_the_path", func(t *testing.T) {
		f := newFakeAihub(t)
		path := apiKeyAdminPrefix + apiKeyOwner + "/keys"
		f.on(path, func(map[string]any) (int, any) {
			return http.StatusCreated, map[string]any{"key_id": fixtureKeyID, "raw_key": fixtureRawKey}
		})
		if _, isErr := callTool(t, f, createKeyCardName, map[string]any{
			apiKeyUserParam: apiKeyOwner, "name": "probe key", "project_scope": "aihub",
		}); isErr {
			t.Fatal("pf_create_api_key failed against the fake, so there is no request to read")
		}
		body := lastBodyFor(t, f, path)
		if _, present := body[apiKeyUserParam]; present {
			t.Errorf("the create body carries %q (it carries %v). The card says the owner is the "+
				"PATH segment and the body holds the rest; an owner in both places is two "+
				"sources of truth for which user the key belongs to, and the server binds only "+
				"one of them.", apiKeyUserParam, sortedBodyKeys(body))
		}
	})

	t.Run("creation_forwards_the_remaining_arguments", func(t *testing.T) {
		f := newFakeAihub(t)
		path := apiKeyAdminPrefix + apiKeyOwner + "/keys"
		f.on(path, func(map[string]any) (int, any) {
			return http.StatusCreated, map[string]any{"key_id": fixtureKeyID, "raw_key": fixtureRawKey}
		})
		args := map[string]any{
			apiKeyUserParam: apiKeyOwner, "name": "probe key", "project_scope": "aihub",
		}
		if _, isErr := callTool(t, f, createKeyCardName, args); isErr {
			t.Fatal("pf_create_api_key failed against the fake")
		}
		body := lastBodyFor(t, f, path)
		for _, field := range []string{"name", "project_scope"} {
			got, present := body[field]
			if !present {
				t.Errorf("the create body carries no %q (it carries %v). The card names both as "+
					"body fields; `project_scope` in particular decides what the key can reach, "+
					"so a scope that is dropped on the way out mints an UNSCOPED key from a "+
					"request that asked for a scoped one.", field, sortedBodyKeys(body))
				continue
			}
			if got != args[field] {
				t.Errorf("the create body carries %q = %#v and the caller sent %#v", field, got, args[field])
			}
		}
	})

	t.Run("revocation_publishes_exactly_two_parameters", func(t *testing.T) {
		props := publishedParamDescriptions(t, revokeKeyCardName)
		if len(props) != 2 {
			t.Errorf("pf_revoke_api_key publishes %d parameter(s) (%v), and the card's table names "+
				"two. A third is a third thing a caller can send to a tool whose whole request is "+
				"two path segments.", len(props), sortedPropNames(props))
		}
		required := publishedRequired(t, revokeKeyCardName)
		if len(required) != 2 {
			t.Errorf("pf_revoke_api_key marks %v required, and the card says both parameters are. "+
				"An optional one on a two-segment path is a request that cannot be built.", required)
		}
	})

	t.Run("revocation_accepts_no_plaintext", func(t *testing.T) {
		props := publishedParamDescriptions(t, revokeKeyCardName)
		// The name creation returns the secret under. Read from the create card's
		// machine block rather than written here, so the two stay one fact.
		secretKey := createCardSecretResponseKey(t)
		if _, present := props[secretKey]; present {
			t.Errorf("pf_revoke_api_key publishes a %q parameter. The card's point is that the "+
				"plaintext is NEVER accepted here — that is why a caller who lost the key can "+
				"still revoke it, and why a revocation request is safe to log.", secretKey)
		}
		for name := range props {
			if strings.Contains(name, "plain") || strings.Contains(name, "secret") {
				t.Errorf("pf_revoke_api_key publishes a parameter named %q, which reads as a "+
					"request for the credential itself", name)
			}
		}
	})

	t.Run("revocation_sends_no_body_at_all", func(t *testing.T) {
		keyID, calls := driveCreateThenRevoke(t)
		revoke := calls[len(calls)-1]
		if revoke.Method != http.MethodDelete {
			t.Errorf("revocation used %s, and the card says DELETE", revoke.Method)
		}
		if len(revoke.Body) != 0 {
			t.Errorf("the revoke request carried a body: %v. The card says there is no body, so "+
				"there is no forwarding table and nothing at hop 2 that can be dropped — a body "+
				"here would be a second place a caller's arguments could go, uncovered by that "+
				"reasoning.", sortedBodyKeys(revoke.Body))
		}
		t.Run("revocation_takes_the_id_creation_returned", func(t *testing.T) {
			want := apiKeyAdminPrefix + apiKeyOwner + "/keys/" + keyID
			if revoke.Path != want {
				t.Errorf("revocation called %s, want %s. The card's claim is that `key_id` is the "+
					"id creation handed back; a path built from anything else means the two tools "+
					"do not compose, and there is no way to list a user's keys to recover from it.",
					revoke.Path, want)
			}
		})
	})
}

// driveCreateThenRevoke runs one creation and feeds its `key_id` straight into a
// revocation, returning the id and every recorded request.
//
// The floor lives here: a creation that answered no `key_id` would leave the
// revocation with nothing to send, and an arm that then asserted on the path
// would be asserting about a request the caller could not have made.
func driveCreateThenRevoke(t *testing.T) (string, []recordedCall) {
	t.Helper()
	f := newFakeAihub(t)
	createPath := apiKeyAdminPrefix + apiKeyOwner + "/keys"
	f.on(createPath, func(map[string]any) (int, any) {
		return http.StatusCreated, map[string]any{"key_id": fixtureKeyID, "raw_key": fixtureRawKey}
	})
	created, isErr := callTool(t, f, createKeyCardName, map[string]any{
		apiKeyUserParam: apiKeyOwner, "name": "probe key",
	})
	if isErr {
		t.Fatalf("pf_create_api_key failed: %v", created)
	}
	keyID, _ := created["key_id"].(string)
	if keyID == "" {
		t.Fatalf("pf_create_api_key answered no key_id (%v). The round trip below is the card's "+
			"claim that revocation consumes what creation produced; with nothing produced there "+
			"is nothing to consume and the assertion would be about a value this test invented.",
			sortedBodyKeys(created))
	}
	if _, isErr := callTool(t, f, revokeKeyCardName, map[string]any{
		apiKeyUserParam: apiKeyOwner, "key_id": keyID,
	}); isErr {
		t.Fatal("pf_revoke_api_key failed against the fake")
	}
	calls := f.recorded()
	if len(calls) < 2 {
		t.Fatalf("only %d request(s) were recorded; the round trip needs both", len(calls))
	}
	return keyID, calls
}

// createCardSecretResponseKey reads the name the creation response carries the
// plaintext under, out of the create card's machine block.
func createCardSecretResponseKey(t *testing.T) string {
	t.Helper()
	flat := cardText(t, createKeyCardName)
	if !strings.Contains(flat, `"raw_key"`) {
		t.Fatal("the pf_create_api_key card's machine block no longer lists a raw_key response " +
			"key. That name is what the arms around it call the secret; if creation stopped " +
			"returning it, the cards' whole secret-handling section is about something else.")
	}
	return "raw_key"
}

// TestTheApiKeySurfaceOffersOnlyCreationAndRevocation is the revoke card's
// reachability bullet.
//
//	"There is no un-revoke, and no listing of keys on this surface:
//	 `pf_list_users` returns users rather than their keys, so a caller must already
//	 hold the `key_id` from creation."
//
// 🔴 A NEGATIVE CENSUS, WRITTEN SO THAT IT CANNOT PASS BY LOOKING AT NOTHING.
// "There is no un-revoke" is the unfalsifiable-negative shape K11 bans in prose,
// and it is exactly answerable as a census: revocation is a soft delete, so the
// question is what any statement in this repo does with `revoked_at`. Every
// occurrence is enumerated and each one must match a recognised shape — the one
// statement that SETS it, or a read that treats it as a null-check. Anything else
// fails, including a statement that clears it, which is the un-revoke the card
// says does not exist.
//
// The listing half is the same in miniature: the users query names its columns,
// and `api_keys` is not among them.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M13 enforcement: add `api_keys` to handleListUsers' SELECT
//	                                             RED  listing_users_does_not_list_their_keys
//	M14 enforcement: add a statement clearing revoked_at (the un-revoke)
//	                                             RED  nothing_clears_the_revocation_marker
//	M15 enforcement: publish a third api-key tool
//	                                             RED  the_surface_is_two_tools
//	M16 publication: reword the card's bullet to claim a listing exists
//	                                             RED  the_card_still_denies_both
//	G3 control:     add a comment mentioning revoked_at
//	                                           GREEN  comments are excluded from the
//	                                                  statement census, and named as
//	                                                  excluded rather than silently
//	                                                  skipped
func TestTheApiKeySurfaceOffersOnlyCreationAndRevocation(t *testing.T) {
	t.Run("the_card_still_denies_both", func(t *testing.T) {
		flat := cardText(t, revokeKeyCardName)
		for _, phrase := range []string{"no un-revoke", "no listing of keys"} {
			if !strings.Contains(flat, phrase) {
				t.Errorf("the pf_revoke_api_key card no longer says %q. This arm's subject comes "+
					"from that bullet; with the bullet gone it is policing an absence nobody "+
					"claimed, and it should go with it.", phrase)
			}
		}
	})

	t.Run("the_surface_is_two_tools", func(t *testing.T) {
		var keyTools []string
		for _, tool := range publishedToolList(t) {
			if strings.Contains(tool.Name, "api_key") {
				keyTools = append(keyTools, tool.Name)
			}
		}
		if len(keyTools) != 2 {
			t.Errorf("the registry publishes %d api-key tool(s): %v. The card tells a caller the "+
				"surface is creation and revocation and nothing else — an un-revoke or a listing "+
				"would be the third, and it would change the answer to \"what happens if I lose "+
				"the key_id\".", len(keyTools), keyTools)
		}
		for _, want := range []string{createKeyCardName, revokeKeyCardName} {
			found := false
			for _, got := range keyTools {
				if got == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s is not among the published api-key tools %v", want, keyTools)
			}
		}
	})

	t.Run("listing_users_does_not_list_their_keys", func(t *testing.T) {
		src := serverRouterSource(t)
		i := strings.Index(src, "func handleListUsers(")
		if i < 0 {
			t.Fatal("internal/server/router.go declares no handleListUsers. The card's claim is " +
				"about what THAT handler returns; without it this subtest is about nothing.")
		}
		body := src[i:]
		if j := strings.Index(body, "\nfunc "); j > 0 {
			body = body[:j]
		}
		if !strings.Contains(body, "SELECT id, email, display_name, user_type, role") {
			t.Errorf("handleListUsers no longer selects the five user columns the card's claim "+
				"rests on. Its query is:\n%s", body)
		}
		if strings.Contains(body, "api_keys") {
			t.Errorf("handleListUsers mentions api_keys. The card says this surface returns users " +
				"rather than their keys — a key list here is a set of credentials' ids in every " +
				"admin listing, and it would also make the card's \"a caller must already hold " +
				"the key_id\" false.")
		}
	})

	t.Run("nothing_clears_the_revocation_marker", func(t *testing.T) {
		sites := revokedAtStatementSites(t)
		if len(sites) < 3 {
			t.Fatalf("the census found %d statement(s) touching revoked_at (%v). The marker is "+
				"written once and read in at least two places; a walk that found fewer has "+
				"stopped seeing its population, and every absence below would be an absence "+
				"from an empty set.", len(sites), sites)
		}
		setters := 0
		for _, site := range sites {
			switch {
			case strings.Contains(site.text, "jsonb_build_object('revoked_at'"):
				setters++
			case strings.Contains(site.text, "IS NULL"),
				strings.Contains(site.text, "as revoked_at"),
				strings.Contains(site.text, "`json:\"revoked_at,omitempty\"`"):
				// a read: the null-check that ends authentication, the /ui projections,
				// and the bearer struct field. None of them writes.
			default:
				t.Errorf("%s carries a statement touching revoked_at that this census does not "+
					"recognise:\n    %s\nThe card says there is no un-revoke. A new shape here is "+
					"either that un-revoke or a read the census should learn — and the failure is "+
					"loud on purpose, because the alternative is a walk that quietly stops "+
					"covering the thing it is named for.", site.file, site.text)
			}
			if strings.Contains(site.text, "- 'revoked_at'") ||
				strings.Contains(site.text, "revoked_at = NULL") ||
				strings.Contains(site.text, "revoked_at', null") {
				t.Errorf("%s appears to CLEAR revoked_at:\n    %s\nThat is the un-revoke the card "+
					"says does not exist, and it would silently re-authenticate a credential "+
					"somebody deliberately withdrew.", site.file, site.text)
			}
		}
		if setters != 1 {
			t.Errorf("%d statement(s) set revoked_at, want exactly 1. Revocation is a soft delete "+
				"with one writer; a second one is a second definition of what revoked means.",
				setters)
		}
	})
}

// revokedAtSite is one non-comment line mentioning revoked_at.
type revokedAtSite struct {
	file string
	text string
}

// revokedAtStatementSites enumerates every non-comment mention of revoked_at
// under internal/, which is the population the un-revoke claim is about.
//
// ⚠️ Comments and _test.go files are excluded, and named here rather than
// silently skipped: a comment cannot clear a column, and a test that writes one
// is writing to its own fixture.
func revokedAtStatementSites(t *testing.T) []revokedAtSite {
	t.Helper()
	var out []revokedAtSite
	root := filepath.Join("..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".sql") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, "revoked_at") {
				continue
			}
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
				continue
			}
			out = append(out, revokedAtSite{file: filepath.ToSlash(path), text: trimmed})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v — a census that cannot read its population must fail rather than "+
			"report an empty one", root, err)
	}
	return out
}
