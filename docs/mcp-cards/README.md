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
  different fact from an empty list, and for the three artifact-action tools it is
  itself evidence. What holds the list against a **live** response is K10, in a
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
cell quoting text the tool does not publish.

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
sentences. Over the whole hop 0-1 section K9 would report six such glosses as
drift; scoped this way it checks 28 real quotes and reports none of them.

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

Of the 48 published tools, K10 drives **42**. The six it cannot are `pf_commit`,
`pf_diff`, `pf_pr`, `pf_push`, `pf_ship` and `pf_wrap`, each needing a git
worktree, a git remote or the `gh` CLI. They are named in `liveWalkOutOfReach`,
and that list is checked both ways: a tool that stops being driven without being
added to it fails the arm, and a listed tool that IS driven fails it as a stale
exemption. So the walk cannot shrink except in a diff somebody signs.

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
— never by memory id. The eight items in that document's `§6.4` are **not**
settled, and a card that touches one says so under `## Open`.
