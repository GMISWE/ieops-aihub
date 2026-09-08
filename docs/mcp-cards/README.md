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
- **`response_keys_observed`** is copied from
  [`../audits/aihub-412-corpus-facts/response-keys/`](../audits/aihub-412-corpus-facts/response-keys/)
  and compared against it by the gate, so the copy cannot drift. `null` means
  that corpus holds no record for the tool — which is a different fact from an
  empty list, and for the three artifact-action tools it is itself evidence.

## The gate

`internal/mcp/contract_cards_gate_test.go`, run by the always-on `Unit
tests` CI step. Arms: **K1** a published tool with no card · **K2** a card for a
tool that is not published · **K3** a card's params or description hash
disagreeing with the live registry · **K4** a missing heading, a parameter never
named in the prose, or a `written` card with no hop-4 body · **K5** a `pending`
card that has one, or more pending cards than the ceiling · **K6** a cited `.go`
path or symbol that does not resolve · **K7** disagreement with the corpus
record · **K8** floors, so a broken walk cannot pass by finding nothing.

Citations use semantic anchors — a backticked repo-relative Go path, optionally
followed by a parenthesised backticked symbol — because
`scripts/pf_docs_contract_check.py`'s check C1 bans line numbers anywhere
under `docs/`. K6 verifies both halves. C2, the arm that checks a referenced path
exists, does **not** glob this directory, and per `aihub#406` does not verify
symbols anywhere; K6 is why the cards are self-gated rather than waiting on that.

## Working on a card

```sh
# after any tool schema or description change
GOWORK=off PF_CARDS_REGEN=1 go test ./internal/mcp/ -run TestGenerateContractCards
GOWORK=off go test ./internal/mcp/ -run TestContractCard -v
```

The generator rewrites **only** the machine block. Prose is never generated: a
card whose hop-4 section is empty passes every mechanical arm and asserts
nothing, which is what K4 exists to stop.

Adjudicated policy is cited by document section and row id — `§6.1 T1-12` in
[`../audits/aihub-411-design-decision-table.md`](../audits/aihub-411-design-decision-table.md)
— never by memory id. The eight items in that document's `§6.4` are **not**
settled, and a card that touches one says so under `## Open`.
