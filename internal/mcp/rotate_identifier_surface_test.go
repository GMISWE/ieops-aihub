package mcp_test

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_rotate_identifier.md` hop 2-3
// paragraph and the sentence about the do-not-log notice.
//
//	"`internal/mcp/tools_projects.go` (`registerProjectTools`) rejects an empty
//	 name and calls `pkg/client/client.go` (`RotateProjectIdentifier`) →
//	 `POST /v1/projects/<name>/rotate_identifier` with a **nil body**, bound by
//	 `internal/server/routes_projects.go` (`handleRotateIdentifier`)."
//	"The handler carries an explicit `NOTE: result contains plain token — do NOT
//	 log it` at the call site…"
//
// 🔴 THE SENTENCE THAT WAS FALSE. Until 2026-09-10 the second one ended "…which
// is the only place that instruction can be enforced by a reader." Measured on
// this tree, the instruction is written at FOUR hops, not one:
// internal/mcp/tools_projects.go at the call site, pkg/client/client.go on
// RotateProjectIdentifier's doc comment, internal/server/routes_projects.go twice
// on handleRotateIdentifier, and internal/domain/projects.go where the bcrypt
// hash is taken. Each of those functions holds the plaintext, so each is a place
// a reader could break the rule and each carries its own note. The card now says
// that, and this arm holds all four — which is a strictly better ratchet than the
// original claim would have produced, because "the only place" is satisfied by
// deleting the other three notices.
//
// 🔴 WHY THE WIRE HALF IS NOT A SOURCE SCAN. "with a nil body" and "nothing else
// is sent" are claims about the request that leaves the process. The contrast is
// what makes them worth pinning: pf_create_project on the same file forwards its
// WHOLE argument map, so "this one sends nothing but the path" is a real
// difference between two tools written side by side, and a copy-paste in either
// direction is invisible to every existing gate.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestRotateIdentifier|TestTheDoNotLog' -count=1

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	rotateCardName = "pf_rotate_identifier"

	// rotateProbeProject is a project name, and fixtureIdentifier is a token this
	// file invented for the fake to hand back. Neither is a credential: nothing
	// in this package can mint or read a real one.
	rotateProbeProject = "probe-project"
	fixtureIdentifier  = "pi_FIXTURE_NOT_A_REAL_TOKEN"
)

// rotateWirePath is the route the card names, built from the project name so that
// the path-segment claim is what is being asserted rather than assumed.
func rotateWirePath(project string) string {
	return "/v1/projects/" + project + "/rotate_identifier"
}

// TestRotateIdentifierSendsOnlyTheProjectNameAndRefusesAnEmptyOneLocally is the
// hop 2-3 paragraph, encoded as the four things it tells a reader.
//
// MUTANTS (applied to this tree; the verdict is what RAN, not what was expected):
//
//	M1 enforcement: pass `args` as the body instead of nil
//	                                             RED  the_request_carries_no_body
//	                                                  + nothing_but_the_name_is_sent
//	M2 enforcement: neuter the empty-name guard so the call goes out anyway
//	                                             RED  an_empty_name_is_refused_without_asking_the_server,
//	                                                  on both halves — the tool answered ok
//	                                                  and a request was recorded
//	M3 enforcement: point client.RotateProjectIdentifier at another route
//	                                             RED  the_route_is_the_one_the_card_names
//	M4 enforcement: switch the request to PATCH
//	                                             RED  the_route_is_the_one_the_card_names
//	M5 publication: reword the card's route so it no longer matches
//	                                             RED  the_route_is_the_one_the_card_names —
//	                                                  the path is read out of the card, so
//	                                                  editing either side alone is red
//	M6 publication: delete the card's hop 2-3 paragraph
//	                                             RED  the extraction floor in
//	                                                  rotateCardRoute
//	G1 control:     rename the fixture project    GREEN  the path is built from the name the
//	                                                  caller sent, not from a literal
func TestRotateIdentifierSendsOnlyTheProjectNameAndRefusesAnEmptyOneLocally(t *testing.T) {
	t.Run("the_route_is_the_one_the_card_names", func(t *testing.T) {
		method, template := rotateCardRoute(t)
		if method != http.MethodPost {
			t.Errorf("the card names %s and this arm drives POST", method)
		}
		f := newFakeAihub(t)
		f.on(rotateWirePath(rotateProbeProject), rotateFakeResponse)
		if _, isErr := callTool(t, f, rotateCardName, map[string]any{"name": rotateProbeProject}); isErr {
			t.Fatal("pf_rotate_identifier failed against the fake, so there is no request to read")
		}
		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("%d request(s) recorded, want exactly 1 (%v). The card describes one call "+
				"with a nil body; a second request is a hop the card does not mention.",
				len(calls), f.paths())
		}
		want := strings.Replace(template, "<name>", rotateProbeProject, 1)
		if calls[0].Path != want || calls[0].Method != method {
			t.Errorf("the tool called %s %s and the card says %s %s. The project name is the "+
				"whole request — there is no body to carry it — so a route that does not "+
				"interpolate it rotates the wrong project or none.",
				calls[0].Method, calls[0].Path, method, want)
		}
	})

	t.Run("the_request_carries_no_body", func(t *testing.T) {
		f := newFakeAihub(t)
		f.on(rotateWirePath(rotateProbeProject), rotateFakeResponse)
		if _, isErr := callTool(t, f, rotateCardName, map[string]any{"name": rotateProbeProject}); isErr {
			t.Fatal("pf_rotate_identifier failed against the fake")
		}
		calls := f.recorded()
		if len(calls) == 0 {
			t.Fatal("no request was recorded, so \"no body\" would be true of nothing")
		}
		if len(calls[0].Body) != 0 {
			t.Errorf("the rotate request carried a body: %v. The card says nil, and that is why "+
				"the paragraph goes on to say there is nothing at hop 2 that can be dropped — a "+
				"body would put that reasoning back in play for every key in it.",
				sortedBodyKeys(calls[0].Body))
		}
	})

	t.Run("nothing_but_the_name_is_sent", func(t *testing.T) {
		// The contrast with pf_create_project on the same file, which forwards its
		// whole argument map. An unpublished key here must reach nothing at all.
		f := newFakeAihub(t)
		f.on(rotateWirePath(rotateProbeProject), rotateFakeResponse)
		result, isErr := callTool(t, f, rotateCardName, map[string]any{
			"name": rotateProbeProject, "aihub543_probe_key": "must-reach-nothing",
		})
		if isErr {
			t.Fatalf("pf_rotate_identifier failed: %v", result)
		}
		for _, c := range f.recorded() {
			for key := range c.Body {
				t.Errorf("the rotate request carried %q. \"Nothing else is sent\" is the card's "+
					"phrase, and it is what makes this tool's hop 2 surface exactly one path "+
					"segment.", key)
			}
			if strings.Contains(c.Path, "probe_key") {
				t.Errorf("the unpublished key reached the path: %s", c.Path)
			}
		}
	})

	t.Run("an_empty_name_is_refused_without_asking_the_server", func(t *testing.T) {
		f := newFakeAihub(t)
		result, isErr := callTool(t, f, rotateCardName, map[string]any{"name": ""})
		if !isErr {
			t.Errorf("pf_rotate_identifier answered %v for an empty name. The card says the tool "+
				"REJECTS it; an empty name would otherwise be interpolated into the path and "+
				"reach a route that does not exist.", result)
		}
		if n := len(f.recorded()); n != 0 {
			t.Errorf("the tool made %d request(s) (%v) for an empty name. The card's claim is "+
				"that the check happens HERE — an error that came back from the server looks the "+
				"same to a caller and is a different contract, because it costs a round trip and "+
				"depends on the server keeping a validation this tool promises locally.",
				n, f.paths())
		}
	})
}

// rotateFakeResponse is the fake's answer: the two keys aihub#482's K10 pins
// against a live server, with a fixture token in place of a credential.
func rotateFakeResponse(map[string]any) (int, any) {
	return http.StatusOK, map[string]any{"plain": fixtureIdentifier, "prefix": "pi_FIXTURE"}
}

// rotateCardRouteRe reads the method and route template out of the card's hop 2-3
// paragraph.
var rotateCardRouteRe = regexp.MustCompile("`(GET|POST|PATCH|PUT|DELETE) (/v1/[^`]+)`")

// rotateCardRoute returns the method and path template the card publishes.
func rotateCardRoute(t *testing.T) (string, string) {
	t.Helper()
	m := rotateCardRouteRe.FindStringSubmatch(cardText(t, rotateCardName))
	if m == nil {
		t.Fatal("the pf_rotate_identifier card names no `<METHOD> /v1/…` route. The route is " +
			"what this arm compares the observed request against; without it every comparison " +
			"below is against a string this file made up.")
	}
	if !strings.Contains(m[2], "<name>") {
		t.Fatalf("the card's route %q carries no <name> placeholder. The project name is the "+
			"entire request, so a route without it is not this tool's route.", m[2])
	}
	return m[1], m[2]
}

// doNotLogHop is one function that holds the plain token, with the file it lives
// in and the notice it must carry.
type doNotLogHop struct {
	path string
	// cardRef is the repo-relative path as the CARD writes it. Separate from path,
	// which is relative to this package, and compared instead of the base name:
	// mutant M12 (dropping internal/domain/projects.go from the card's notice
	// sentence) came back GREEN while this arm compared base names, because
	// "routes_projects.go" contains "projects.go" as a substring and the remaining
	// hop satisfied the check for the deleted one.
	cardRef string
	// opening is the text the function's region starts at. Regions are bounded by
	// their own opening rather than by a line number for the reason C1 gives for
	// docs: a line anchor stops matching silently, or starts matching whatever
	// moved onto that line.
	opening string
	// notice is a phrase the region must contain. Each hop words it differently
	// and that is left alone: what matters is that each one says it, and copying
	// a single wording into four files is the kind of edit that gets reverted.
	notice string
}

// doNotLogHops is every function that has the plaintext identifier in hand.
//
// 🔴 Both directions matter and only one is cheap. The list is the population
// this arm walks, so a hop missing from it is a hop nobody checks — which is why
// the walk also asserts, below, that the whole rotate path is these four and that
// the card names them.
var doNotLogHops = []doNotLogHop{
	{
		path:    filepath.Join("tools_projects.go"),
		cardRef: "internal/mcp/tools_projects.go",
		opening: `Name:        "pf_rotate_identifier"`,
		notice:  "do NOT log it",
	},
	{
		path:    filepath.Join("..", "..", "pkg", "client", "client.go"),
		cardRef: "pkg/client/client.go",
		opening: "// RotateProjectIdentifier calls",
		notice:  "must not be logged",
	},
	{
		path:    filepath.Join("..", "server", "routes_projects.go"),
		cardRef: "internal/server/routes_projects.go",
		opening: "// handleRotateIdentifier handles",
		notice:  "NEVER logged or stored",
	},
	{
		path:    filepath.Join("..", "domain", "projects.go"),
		cardRef: "internal/domain/projects.go",
		opening: "func RotateIdentifier(",
		notice:  "plain never stored",
	},
}

// loggingCalls are the ways this repo reaches a log from a function body. A hop
// holding the plaintext must use none of them.
var loggingCalls = []string{"log.", "slog.", "logger.", "fmt.Print", "println("}

// TestTheDoNotLogNoticeRidesEveryHopThatHoldsThePlainToken is the corrected
// sentence, encoded.
//
// 🔴 WHY THIS IS WORTH AN ARM AT ALL. A comment cannot be enforced by anything
// but a reader, which is exactly why the four of them rot invisibly: nothing
// fails when one is deleted, and the value they are about is a credential that
// reaches a caller's transcript. So the arm does two things a reader cannot: it
// requires the notice at every hop that holds the plaintext, and it requires none
// of those four functions to reach a logger — which is the property the notices
// are asking for, checked instead of requested.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M7 enforcement: delete the call-site NOTE from internal/mcp/tools_projects.go
//	                                             RED  every_hop_carries_the_notice/tools_projects.go
//	M8 enforcement: delete the notice from pkg/client/client.go's doc comment
//	                                             RED  every_hop_carries_the_notice/client.go
//	M9 enforcement: add a log line printing the rotate result in the MCP handler
//	                                             RED  no_hop_reaches_a_logger/tools_projects.go
//	M10 enforcement: log `plain` in domain.RotateIdentifier
//	                                             RED  no_hop_reaches_a_logger/projects.go
//	M11 publication: reword the card's quoted notice (drop "result contains")
//	                                             RED  the_card_quotes_the_call_site_notice
//	M12 publication: reword the card so it names three hops instead of four
//	                                             RED  the_card_names_every_hop
//	   🔴 GREEN twice on earlier versions of this arm, and both reasons are the same
//	   mistake at different scopes: it compared BASE names, and "routes_projects.go"
//	   contains "projects.go"; and it searched the WHOLE card, which names
//	   internal/domain/projects.go in another bullet. Repo-relative paths, inside the
//	   notice paragraph only, because of this run.
//	M12b publication: name only `internal/server/routes_projects.go` and keep the
//	     word "four" — the masking itself, with the count left alone
//	                                             RED  the_card_names_every_hop
//	G2 control:     add a comment mentioning logging to an unrelated function in
//	    one of the four files                   GREEN  each region is bounded by its own
//	                                                  opening, so a neighbour's text is
//	                                                  outside it
func TestTheDoNotLogNoticeRidesEveryHopThatHoldsThePlainToken(t *testing.T) {
	if len(doNotLogHops) != 4 {
		t.Fatalf("this arm walks %d hop(s); the card names four. A shorter list checks fewer "+
			"places than the sentence claims, which is the failure the floors in this package "+
			"exist to refuse", len(doNotLogHops))
	}
	// 🔴 Scoped to the NOTICE PARAGRAPH, not to the whole card. The card names
	// internal/domain/projects.go in a different hop-4 bullet as well, so a
	// whole-card search would report the notice sentence as naming a hop the
	// sentence had stopped naming — the same masking M12 found one level down.
	notice := rotateNoticeParagraph(t)

	t.Run("the_card_quotes_the_call_site_notice", func(t *testing.T) {
		m := regexp.MustCompile("an explicit `([^`]+)` at the call site").FindStringSubmatch(notice)
		if m == nil {
			t.Fatal("the card no longer quotes the call-site notice in the form \"an explicit " +
				"`…` at the call site\". That quote is the string this arm looks for in the " +
				"source, so without it the search below is for a phrase this file chose.")
		}
		src := readFileForArm(t, doNotLogHops[0].path, "the MCP rotate handler")
		if !strings.Contains(src, m[1]) {
			t.Errorf("internal/mcp/tools_projects.go does not carry the card's quoted notice "+
				"%q. The card presents it as verbatim source; where the two differ, a reader "+
				"looking for that comment does not find it.", m[1])
		}
	})

	t.Run("the_card_names_every_hop", func(t *testing.T) {
		var missing []string
		for _, hop := range doNotLogHops[1:] {
			if hop.cardRef == "" {
				t.Fatalf("the hop at %s declares no cardRef, so this census would skip it "+
					"silently — which is the failure it exists to catch", hop.path)
			}
			if !strings.Contains(notice, "`"+hop.cardRef+"`") {
				missing = append(missing, hop.cardRef)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("the card's notice sentence does not name %v. It used to say the call site "+
				"was \"the only place that instruction can be enforced by a reader\", which was "+
				"measurably false — the instruction is at four hops. Naming them is what makes "+
				"this arm's population the card's population rather than one this file chose.",
				missing)
		}
	})

	t.Run("every_hop_carries_the_notice", func(t *testing.T) {
		for _, hop := range doNotLogHops {
			t.Run(filepath.Base(hop.path), func(t *testing.T) {
				region := doNotLogRegion(t, hop)
				if !strings.Contains(region, hop.notice) {
					t.Errorf("the region opening at %q in %s does not say %q:\n%s\nEvery hop on "+
						"this path holds the plaintext, and a notice deleted from one of them is "+
						"the only warning a reader of that file was going to get.",
						hop.opening, hop.path, hop.notice, region)
				}
			})
		}
	})

	t.Run("no_hop_reaches_a_logger", func(t *testing.T) {
		for _, hop := range doNotLogHops {
			t.Run(filepath.Base(hop.path), func(t *testing.T) {
				region := doNotLogRegion(t, hop)
				for _, call := range loggingCalls {
					if strings.Contains(region, call) {
						t.Errorf("the region opening at %q in %s reaches %s. It has the plain token "+
							"in hand: whatever the call prints, a log line on this path puts a live "+
							"credential somewhere the caller cannot rotate it out of, and the "+
							"comment next to it asks for exactly the opposite.",
							hop.opening, hop.path, call)
					}
				}
			})
		}
	})
}

// rotateNoticeParagraph returns the card paragraph that carries the do-not-log
// sentence, whitespace-collapsed.
//
// A paragraph rather than the whole card, and found by its own opening words
// rather than by a position: the assertions above are about what THAT sentence
// names, and the card names two of the same files elsewhere.
func rotateNoticeParagraph(t *testing.T) string {
	t.Helper()
	raw := readFileForArm(t, filepath.Join("..", "..", "docs", "mcp-cards",
		rotateCardName+".md"), "the rotate card")
	i := strings.Index(raw, "The handler carries an explicit")
	if i < 0 {
		t.Fatal("the pf_rotate_identifier card has no paragraph opening \"The handler carries " +
			"an explicit\". That paragraph is this arm's subject; without it every assertion " +
			"below would be about the rest of the card, which names two of the same files.")
	}
	para := raw[i:]
	if j := strings.Index(para, "\n\n"); j > 0 {
		para = para[:j]
	}
	flat := strings.Join(strings.Fields(para), " ")
	if len(flat) < 80 {
		t.Fatalf("the notice paragraph reads %q — too short to be the sentence this arm is "+
			"named for, so the bound has slipped", flat)
	}
	return flat
}

// doNotLogRegion returns the source between a hop's opening and the end of the
// declaration it opens, and fails if the opening is not there.
//
// The end is the next top-level declaration or, for the MCP handler, the next
// tool registration — the handler is an anonymous function inside
// registerProjectTools, so "the function" is the addTool call it belongs to. The
// region is required to contain the rotate call itself, which is the floor: a
// region that missed it would be some other code, and every assertion about it
// would be about the wrong text.
func doNotLogRegion(t *testing.T, hop doNotLogHop) string {
	t.Helper()
	src := readFileForArm(t, hop.path, "a hop that holds the plain token")
	i := strings.Index(src, hop.opening)
	if i < 0 {
		t.Fatalf("%s does not contain %q. The card's sentence is about that function; if it has "+
			"been renamed or moved, this arm is reading a region that is not it.",
			hop.path, hop.opening)
	}
	region := src[i:]
	for _, boundary := range []string{"\n\t// pf_", "\n}\n\n", "\n\nfunc "} {
		if j := strings.Index(region, boundary); j > 0 {
			region = region[:j]
			break
		}
	}
	if !strings.Contains(region, "RotateProjectIdentifier") &&
		!strings.Contains(region, "RotateIdentifier") &&
		!strings.Contains(region, "rotate_identifier") {
		t.Fatalf("the region opening at %q in %s does not mention the rotate call:\n%s\nThe "+
			"bound has slipped, so the notice and logger assertions would be about neighbouring "+
			"code.", hop.opening, hop.path, region)
	}
	return region
}

// cardNamesOnDisk lists every card under docs/mcp-cards, excluding the README.
// The cross-card censuses in this package walk it so that "no other card says
// this" is a statement about the whole set.
func cardNamesOnDisk(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "docs", "mcp-cards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v — a census that cannot list its population must fail rather than "+
			"report an empty one", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(out)
	return out
}

// cardOpenSection returns a card's `## Open` section body.
func cardOpenSection(t *testing.T, card string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-cards", card+".md"))
	if err != nil {
		t.Fatalf("read the %s card: %v", card, err)
	}
	body := string(raw)
	i := strings.Index(body, "\n## Open")
	if i < 0 {
		t.Fatalf("the %s card has no `## Open` section. K4 reports a missing section; this arm "+
			"fails rather than treating the absence as an empty section that satisfies every "+
			"phrase check by containing nothing.", card)
	}
	section := body[i+len("\n## Open"):]
	if j := strings.Index(section, "\n## "); j > 0 {
		section = section[:j]
	}
	if len(strings.TrimSpace(section)) < 20 {
		t.Fatalf("the %s card's `## Open` section is %q — too short to hold a bullet", card, section)
	}
	return section
}
