# MCP contract cards

One card per published `pf_*` MCP tool. [`../mcp-tools.md`](../mcp-tools.md) is the
curated index — which tools exist, grouped by the file that registers them. This
directory is the depth beneath it: for each tool, what every published parameter
and response field actually does, hop by hop.

**Hop vocabulary** (from the `aihub#380` / `aihub#385` audits, unchanged):

| hop | what it is |
|---|---|
| 0 | the harness and the MCP SDK |
| 1 | the `InputSchema` text — the only thing an LLM caller sees |
| 2 | what the MCP handler forwards, and in what shape |
| 3 | which server field or struct binds it |
| 4 | what the domain does with it, or returns |
| 5 | the domain response projected back into the MCP result |

## What a card is for

`internal/mcp/universal_contract_gate_test.go` quantifies four mechanical
properties over every tool and every parameter. Its own header states what it
cannot reach: hop 4, "the bound field is ACTED ON", whose census "needs a
tool->domain-function map — a hand-written list, which is the thing this file
exists to replace."

A card is that hand-written list, kept honest at the edges. The two are
complements: the gate proves the mechanical half over the whole surface, the card
records the judgement half for one tool.

## Shape

Each card opens with a **generated** fenced `json` block and continues with
**hand-written** prose under six fixed headings.

```
{
  "tool":                   "<name>",
  "description_sha256":     "<sha256 of the live tool description>",
  "input_schema_sha256":    "<sha256 of the live serialised InputSchema>",
  "params":                 { "<name>": {"type", "required", "enum"} },
  "response_keys_observed":  [...] | null,
  "hop4_coverage":          "written" | "pending"
}
```

- **`description_sha256`, not the description itself.** Copying every tool
  description into `docs/` would be a second copy of the schema's own prose whose
  only failure mode is disagreeing with it. A hash costs one line and goes red on
  exactly the same event; the repair is to re-read the card against the new
  description and then regenerate. It is not meant to be human-readable.
- **`input_schema_sha256` is a second hash and it is not redundant.** It covers
  the whole serialised `InputSchema`, which is where every **per-parameter**
  description lives; `description_sha256` covers only the tool-level description,
  and the contract JSON the `params` rows come from carries no per-property
  descriptions at all. It exists because the first hash was measured insufficient:
  `aihub#433` rewrote `pf_remember`'s `base_strength` description from `(0-1)` to
  `1-5 (default 3)` and `pf_recall`'s `min_strength` with it — falsifying three
  cards — and the gate stayed green. Same repair as above: re-read the card's
  hop 0-1 table against the new text, then regenerate.
- **`response_keys_observed`** is copied from
  [`../audits/aihub-412-corpus-facts/response-keys/`](../audits/aihub-412-corpus-facts/response-keys/)
  and compared against it by the gate, so the copy cannot drift **from that
  record**. `null` means that corpus holds no record for the tool — which is a
  different fact from an empty list, and for a tool the corpus window never
  observed it is itself evidence. This used to name "the three artifact-action
  tools" as that example: `pf_adopt_artifact` / `pf_close_artifact` /
  `pf_ignore_artifact` were unpublished by `aihub#446` and their cards deleted
  with them, so the example outlived what it pointed at. The roster of `null`
  cards is whatever `grep -l '"response_keys_observed": null' docs/mcp-cards/*.md`
  returns, and is not written out here because it moves whenever the corpus is
  re-extracted. What holds the list against a **live** response is K10, in a
  separate DB-gated file; read "K7 is copy-to-copy, K10 is the one that reaches a
  server" below before trusting the list on its own.
- **`docs/mcp-cards/live-response-keys.json`** is the other half of that
  declaration: the keys a live response carries that a card cannot name, because
  the corpus record it copies is generated and a key added after the corpus window
  is not in it. Each entry carries the reason it is not in the corpus.

## The gate

`internal/mcp/contract_cards_gate_test.go`, run by the always-on `Unit
tests` CI step. Arms: **K1** a published tool with no card · **K2** a card for a
tool that is not published · **K3** a card's params, description hash or
`InputSchema` hash disagreeing with the live registry · **K4** a required section
missing, out of order or empty, a parameter never named in the prose, a thin
hop 0-1 on a tool with no parameters, or a `written` card with no hop-4 body ·
**K5** a `pending` card that has one, or more pending cards than the ceiling ·
**K6** a cited `.go` path or symbol that does not resolve, or a bare filename
cited with no directory · **K7** disagreement with the corpus record · **K8**
floors, so a broken walk cannot pass by finding nothing · **K9** a hop 0-1 table
cell quoting text the tool does not publish · **K11** an `## Open` bullet
asserting an unfalsifiable negative, or naming a work item with no date. The
numbering jumps K9 to K11 because K10 is taken by the DB-gated arm below.

**K10** lives elsewhere and does not run in that step:
`internal/mcp/card_response_keys_live_e2e_db_test.go`, gated on `AIHUB_TEST_DB`
and run by ci.yml's `aihub#482 live response-key contract DB tests`. It is the
only arm that reaches a server.

Citations use semantic anchors — a backticked repo-relative Go path, optionally
followed by a parenthesised backticked symbol — because
`scripts/pf_docs_contract_check.py`'s check C1 bans line numbers anywhere
under `docs/`. K6 verifies both halves. C2, the arm that checks a referenced path
exists, does **not** glob this directory, and per `aihub#406` does not verify
symbols anywhere; K6 is why the cards are self-gated rather than waiting on that.
A **bare filename** — a `.go` file cited with no directory at all — is rejected
rather than resolved: this repo has same-named `.go` files across packages, so a
bare name identifies nothing and a glob would silently pick one.

### K9 — quoting the published text, and the marker for withdrawn text

A hop 0-1 table cell that **opens** with a double-quoted string is a claim that
the quote is **verbatim** published text, and K9 requires it to occur in what the
tool publishes — the tool description or any string in the live `InputSchema`.
Containment, not equality, so quoting a prefix of a long description is fine, and
anything you add after the closing quote is a note rather than part of the claim:

```
| `work_item_id` | string | yes | "Work item ID" — a slug or a canonical id |
```

Scoping it to the start of a cell is what makes the arm usable: a quote *inside* a
sentence is the author's own words about the text, and the cards rely on that —
this set quotes the withdrawn `"non-conflicting"` in `pf_get_ready_queue.md`'s
prose, and writes `` `fields="brief"` `` and `ABSENT means "no step state"` inside
sentences. Over the whole hop 0-1 section K9 would report those glosses as drift;
scoped this way it reports none of them. How many real quotes remain in scope is
printed by the arm itself, on the `K9:` line of

```
GOWORK=off go test ./internal/mcp/ -run '^TestContractCardQuotes' -count=1 -v
```

and is deliberately not restated here — this sentence claimed 28 while the arm
printed 23 (`aihub#493`), the same rot the floor comments in
`internal/mcp/contract_cards_gate_test.go` carried until
`TestMeasuredFloorCommentsCarryNoValue` took the values out of them.

If a **row** must quote text the tool no longer publishes, mark that row:

```
| `old_param` | string | no | withdrawn by `aihub#387`; last published as "..." <!-- historical --> |
```

The marker exempts every quote on **that row only**, and a marked row whose quote
turns out to still be live is reported too — a stale exemption is the K5 failure
in another place. `maxHistoricalQuoteRows` in the gate is a **ceiling** on how
many exist, currently `0`, so reaching for the marker also costs an edit somebody
signs. That is deliberate: an escape hatch cheaper than compliance becomes the
compliant path.

K9 exists because **regenerating the hash and leaving the prose was the cheapest
compliant path**. Probed on `origin/main` = `6bd9d80` and reproduced on
`0dca671`: change a published
parameter description, run `PF_CARDS_REGEN=1`, re-run the gate — **green**, with
the card still quoting the old text verbatim in its table and a paragraph
asserting the old value had been fixed. The whole diff was
`input_schema_sha256` moving from one 64-hex string to another, and both are
equally unreadable, so "a card diff a reviewer reads" meant "a reviewer notices a
hash moved and independently decides to go re-read prose the diff does not show".
K3 still fires on the same event and should: it says *something* changed. K9 says
*what*, and it survives regeneration because the generator never touches prose.

### K11 — `## Open` is a form rule, not a fact-checker

`aihub#476` swept every card's `## Open` section against the tree it described and
found **5 of 48 carrying a false assertion**, with the gate green through all
five — the arms above compare a generated machine block against the live schema,
and an Open section is prose no machine block covers. K11 (`aihub#483`) is the arm
that reads it.

It does **not** decide whether a bullet is true. It refuses the two forms in which
a false bullet cannot be found out:

- **An unfalsifiable negative** — `nobody has …`, `is unmeasured`,
  `was not updated by …`. All three come from the five, and between them they
  catch **5 of 5**. There is no date escape: timestamping a claim about what
  anyone anywhere has ever measured still leaves a reader nothing to re-run. Say
  who would hold the answer, or scope the negative to yourself the way
  `pf_emit_event.md` does with *"needs a DB read this line could not make"*.
- **A work-item citation with no date.** An Open bullet naming an `aihub#NNN` is
  claiming something about that item's state — it is open, it decided X, it left Y
  behind — and that state lives in the aihub database, which no arm here can read.
  Undated, the claim is true the day it is written and silently wrong afterwards,
  which is exactly how the five arose. The convention is a plain ISO date saying
  when you last looked: *"still open (`paused`) at the last re-check, 2026-09-08"*,
  *"wrapped 2026-09-07; re-checked 2026-09-08"*, or the inline form the role-ladder
  cards use, *"a live-DB read dated 2026-09-08, which dates rather than pins"*.

⚠️ **The date does not make the claim true**, and the arm cannot tell a considered
as-of from one copied off the line above. What it removes is the undated absolute,
which is the form all five took.

Two things K11 is deliberately **not**. It does not read the aihub database, so a
bullet whose cited work item has since wrapped stays green until a human re-reads
it; doing better needs an `AIHUB_TEST_DB`-gated step and a `ci.yml` manifest entry,
which `aihub#476` recommended against and `aihub#483` did not build. And its phrase
list is short on purpose — `"<subject> was never read"` is the same family and is
left out because on this tree it reds `pf_recall.md`'s *"the knob was never read"*,
a true claim about what the code does, as often as it catches a real one.

The date half has one exemption, `openCitationWaivers` in the gate, which names the
card, the reason, the date and **how many undated citations it covers**, and applies
to the **date rule only** — never the phrase ban. It is falsifiable in both
directions, and `aihub#494` measured on 2026-09-09 that one of those directions was
missing.

A waiver names a **card**; what it excuses is a **citation**, and one card can hold
several. The only entry the map has ever held read *"its one undated citation is
`aihub#459`"* while `pf_remember.md` carried two undated bullets, and the arm logged
*"2 undated citation(s) waived"* directly beneath it. The second bullet was exempt
under a sentence that never mentioned it — and the self-emptying property went with
it, because the tally could not reach zero while that unmentioned bullet stayed
undated, so the stale-waiver report could not fire however completely the gap the
reason *described* had closed. There are now three findings rather than one:

- **`STALE_WAIVER`** — the card has no undated citation left (the original,
  self-emptying case), **or** the reason's count and the arm's tally disagree, in
  either direction.
- **`WAIVER_NO_COUNT`** — the reason states no count, so there is nothing to
  compare against. Required rather than encouraged, for the reason
  `maxHistoricalQuoteRows` is a signed constant: an optional count check is
  satisfied by leaving the count out, so the escape hatch would cost less than
  compliance.
- **`WAIVER_COUNT_AMBIGUOUS`** — the reason states counts that disagree with each
  other, so picking one would make the check depend on sentence order.

The count is read from the reason's own prose — *"its one undated citation is …"*,
*"2 undated citations"* — anchored on the noun rather than on a bare number, so the
`aihub#NNN` references and ISO dates a reason is full of cannot be mistaken for it.
There is deliberately no second, machine-readable copy of the count to drift from:
the sentence a human reads **is** the claim the gate checks.

⚠️ The map is **empty** on a healthy tree, so none of those branches is reachable
from `docs/mcp-cards/` and the arm is green there whether they work or not. They are
exercised against fixtures by `TestOpenCitationWaiverIsCheckedAgainstItsOwnCount`,
the historical entry's verbatim text among them, and the call site itself is pinned
by `TestOpenCitationWaiverCheckIsWiredIntoTheArm` — without that, deleting the call
would change nothing that runs.

### K7 is copy-to-copy, K10 is the one that reaches a server

`response_keys_observed` is checked by K7 against the checked-in
`../audits/aihub-412-corpus-facts/response-keys/<tool>.json` and against nothing
else. **Both sides are files in this repo.** Probed on `origin/main` = `6bd9d80`
and reproduced on `0dca671`: dropping `role` from both `pf_whoami.md` and its
corpus record leaves that gate green, while the live tool still returns `role`.

Two K7/K8 arms bound it and neither closes it: **`CORPUS_NULL_MASKS_RECORD`**
rejects a `null` card value when a record exists (`null` and `[]` are a different
fact, and until `aihub#473` the comparison could not tell them apart, because it
compares lengths), and **`FLOOR_CORPUS`** refuses the green you get by deleting
the records and the card lists together.

**K10** (`aihub#482`) closes it, by driving the tools against a real Postgres,
router, client and MCP session and reading the keys back. Re-run on this tree
with the two-sided delete above applied: K7 green, K10 red naming
`pf_whoami`.`role`.

⚠️ **It is a ratchet in one direction only, and the direction is the opposite of
the one the corpus records used to claim.** Their `purpose` field said "a later
projection may not drop a key on this list", which is unenforceable: a record is
a **union** over hundreds of calls, so it legitimately names keys a single
response does not carry — measured, `pf_recall`'s `next_cursor`,
`request_adjusted` and `unmatched_types`, `pf_claim_work_item`'s `worktrees`, and
seven of `pf_get_memory`'s. Equality with a live response is therefore not a
stricter test but a **wrong** one. What K10 enforces is the converse: **every
top-level key a live response actually carries must be declared** — by the card,
or by `live-response-keys.json` for keys the generated corpus cannot hold. The
declared set may not shrink below what the server emits; growing it stays cheap.

K10 drives **all 45** published tools (`aihub#501`, 2026-09-09). It drove 39 when
`aihub#482` wrote it; the six it could not were `pf_commit`, `pf_diff`, `pf_pr`,
`pf_push`, `pf_ship` and `pf_wrap`, each needing a git worktree, a git remote or
the `gh` CLI, and their key lists rested on the corpus copy alone across 2,388
recorded calls. `internal/mcp/card_response_keys_live_git_e2e_db_test.go` builds what they
wanted — a bare repo as origin and a clone as the worktree, both under
`t.TempDir()` — and folds them into the same walk, so `liveWalkOutOfReach` is now
empty.

Two things about that arm are qualified, and both are stated on the file rather
than left to be discovered:

- **`gh` is a stub on PATH**, because the alternative is a test that opens pull
  requests on GitHub. So the arm cannot see a change in what *GitHub* calls its
  fields — but it does see aihub's whole side of that boundary, and the stub
  answers `pr list --json <fields>` with **exactly the fields it was asked for**,
  so `pf_pr`'s observed keys track `coding.ghGetPRFields` instead of a list
  written into the test. Adding a field there reddens K10 until a card names it.
- **`pf_diff` returns a raw diff**, so it has no top-level keys to compare and its
  card correctly lists none. It is declared in `liveWalkProseOnly`, not in
  `liveWalkOutOfReach` — "carries no keys" and "cannot be driven" are different
  claims, and that arm holds all three parts of the first one: the walk drove it,
  the result was not JSON, and the card claims no keys.

Both lists are checked both ways: a tool that stops being driven without being
added to one fails the arm, and a listed tool that IS driven fails it as a stale
exemption. So the walk cannot shrink except in a diff somebody signs.

The first thing the git arm found is in `live-response-keys.json`:
`pf_wrap.pushed_sha`. The field has existed since 2026-08-14 and the corpus spans
calls to 2026-09-07, yet the key appears in none of the corpus's 90 recorded
`pf_wrap` calls. What that absence proves is narrower than it reads (aihub#550):
the corpus's key union is taken over strict-JSON success results only — 81 of
the 90; the other 9 are prose (slim-render) results that contribute no keys —
and a wrap that FAILS returns an error result carrying no payload keys at all,
including the arm where the push itself succeeded and `complete_attempt` then
failed. A success payload carries `pushed_sha` exactly when `pushed` is true, so
the supported claim is: **every strict-JSON success in the corpus was an
idempotent replay that pushed nothing** — about failed wraps, pushed or not, the
key record is silent either way. Six months of records could not have shown the
key; one live call did.

## Working on a card

```sh
# after any tool schema or description change

# 1. READ FIRST, before running anything: the card's hop 0-1 table and its prose
#    against the new schema text, and fix whatever the change falsified. This
#    step is first because regenerating first produces a GREEN gate with stale
#    prose — see the "Regenerating is not the repair" note below.

# 2. then rewrite the machine block (hashes, params, nothing else)
GOWORK=off PF_CARDS_REGEN=1 go test ./internal/mcp/ -run TestGenerateContractCards

# 3. verify
GOWORK=off go test ./internal/mcp/ -run TestContractCard -v
```

The generator rewrites **only** the machine block. Prose is never generated: a
card whose sections are empty satisfies every mechanical arm and asserts nothing,
which is what K4 exists to stop. Each of the six sections has a small floor and
hop 4 a large one, headings count only at column 0 outside a code fence, and the
order is fixed — because before `aihub#473` all six were certified by the
presence of a heading *string*, so a card reduced to six bare headings, or with
four of them moved inside a ```text fence, was green.

**Regenerating is not the repair.** `PF_CARDS_REGEN=1` moves the hashes and
leaves every sentence in place, so a card can be mechanically consistent with a
schema it describes wrongly. After a schema change, read the card's hop 0-1
table against the new text first; K9 will catch a stale quote in a table cell,
but nothing catches a stale paragraph.

Adjudicated policy is cited by document section and row id — `§6.1 T1-12` in
[`../audits/aihub-411-design-decision-table.md`](../audits/aihub-411-design-decision-table.md)
— never by memory id. The eight items in that document's `§6.4` started **not**
settled, and a card that touches one says so under `## Open`. They are closed one at
a time, not as a set: item 7 by `aihub#440` and item 2 by `aihub#447`, each recorded
in that same section of the card that owns it, naming the work item and what it
measured. So "a card touching a `§6.4` item says so under Open" still holds — what
the entry says there is OPEN or CLOSED, and a blanket "none of them is settled" is
now false.
