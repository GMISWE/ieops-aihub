#!/usr/bin/env python3
"""Gate docs/ against the executable authorities it describes (aihub#352).

Five checks. Each one goes RED on a real drift, and each exists because the
drift it catches is otherwise SILENT — nothing in this repo turns red today when
a doc's copy of a code fact stops matching the code.

  C1  No line-number citations anywhere in docs/, in any of three shapes:
      a named file plus a line, a code-formatted filename-less range, and a
      filename-less anchor written in prose.
      A line number is an anchor that rots on the next refactor with no
      compiler, test, or linter noticing. Measured on 2026-09-03 against
      e8fbfcb: of the 23 real line-number citations in docs/superpowers/, 18
      pointed at the wrong line and 3 more were off by one or two. The
      replacement is a semantic anchor — file plus symbol name — which C2
      verifies. This check exists so the class stays closed.

      The required form is also stated for doc AUTHORS, in README.md under
      "How docs cite code" (aihub#439). A gate is the wrong place for a rule
      to live alone: an executor brief that mandates line citations is
      reachable by anyone briefing a documentation task, and that is exactly
      what turned aihub#411's first push red with 156 C1 errors. Keep the two
      statements in step — this file's error messages name that section.

  C2  Every path-qualified `*.go` path referenced in docs/superpowers/ exists.
      Semantic anchors are only better than line numbers if something checks
      them. Without C2, `internal/domain/memory.go (UpdateMemory)` rots exactly
      as silently as `internal/domain/memory.go:1444` did — just less visibly.

  C3  docs/mcp-tools.md's tool inventory equals the MCP schema dump's.
      The doc carries a total ("50 pf_* tools") and a per-section count in every
      heading. Those are copies of a fact whose authority is
      `polyforge dump-mcp-schemas`. This asserts SET EQUALITY, not just the
      totals: a count matching while the names diverge is the failure mode a
      count-only check cannot see, because a count is a proxy and the
      deliverable is a classification.

  C4  Section 6 of docs/audits/aihub-411-design-decision-table.md agrees with
      its own rows. That section prints 35 ruling rows and then, some 30 lines
      further down, four hand-maintained counts over them. aihub#488 appended
      `already-landed:aihub#419` to the T1-10 row and left the tally's
      `already-landed` at 5; nothing went red, and a clean-context reviewer
      caught it by recounting by hand (aihub#489). C4 is that recount.

      Like C3 it asserts SET equality and not just totals, and for the same
      reason: the tally names four tags, so a tag carried by a row the
      paragraph never mentions is a drift no sum can see.

  C5  The design doc's lock_acquired + lock_released `cause:` unions cover
      exactly the `lockCause*` constants in internal/domain/resource_events.go.
      The doc restates that vocabulary as a TypeScript union, and the copy went
      stale TWICE without anything noticing: `wi_cancelled` (aihub#355) and
      `derivation_retired` (aihub#416) each added a constant, each was written
      into the changelog and into docs/mcp-cards/, and neither reached the
      union. Both were found by a human review a wave later (aihub#518).

      Set equality on the UNION of the two doc unions, deliberately: which
      event a cause rides on is not derivable from the constant block —
      `derivation_retired` has no Go writer at all, migration 0038 emits it —
      so splitting it here would make this file a THIRD copy of the fact. The
      limit is stated in the doc beside the tables so nobody reads a green C5
      as "the split is checked".

Usage:
    python3 scripts/pf_docs_contract_check.py --schemas /tmp/pf-tool-schemas.json
    python3 scripts/pf_docs_contract_check.py --self-test
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import sys
import tempfile

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(REPO_ROOT, "docs")
MCP_TOOLS_MD = os.path.join(DOCS, "mcp-tools.md")
SUPERPOWERS = os.path.join(DOCS, "superpowers")
AUDIT_411_REL = "docs/audits/aihub-411-design-decision-table.md"
AUDIT_411_MD = os.path.join(REPO_ROOT, AUDIT_411_REL)

# ─── C1 ───────────────────────────────────────────────────────────────────────

# SHAPE 1. A citation is a filename followed by a line number. A filename plus
# extension plus `:digits` in prose is a line citation, whereas a bare `:8080` is
# a port. Verified against all of docs/ on 2026-09-03 — zero false positives.
#
# `md` is in the set as of aihub#439. It was left out originally as "source
# only", but a line citation into a MARKDOWN file rots the same way and faster:
# docs/design/polyforge-v1-design.md is 4,620 lines, 208 KB, and actively
# edited, so a citation into it goes stale on an unrelated prose reflow with
# nothing going red. Measured on 2026-09-08 against 385dcef: adding `md` finds
# zero existing citations in docs/, so this shape lands closed and stays closed.
FILE_LINE_CITATION = re.compile(
    r"(?<![\w/.-])([\w/.-]+\.(?:go|sql|py|ts|tsx|js|sh|yml|yaml|md)):(\d+)"
)

# Inline code spans, with backtick runs of any length. Used to blank out code
# before SHAPE 3 reads what is left as prose. The run length must be honoured:
# `` `:563` `` is a two-backtick span CONTAINING a one-backtick span, and has to
# strip as one unit or its inner `:563` would surface as prose.
CODE_SPAN = re.compile(r"(`+)(.+?)\1")

# SHAPE 2. The filename-less form written inside a code span, e.g. `:1195-1202`.
# Same rot, less information: it does not even say which file, so it is
# uncheckable by construction.
#
# Inside a code span only the RANGE form is matched. A code-formatted single
# `:563` is indistinguishable from a port (`:8080`, `:8085` — docs/deployment.md
# has five such, all ports, all code-formatted), and a regex wide enough to catch
# it reports those five as citations, burying the real signal in false positives.
# A range is unambiguous: no port is written `:N-M`.
#
# The code-formatted SINGLE anchor is therefore still NOT gated. That is not an
# oversight and not this gate's call to make: it is the open question owned by
# aihub#406, and aihub#439's ruling was explicit that it stays there. Measured
# 2026-09-08: docs/ holds 7 code-formatted single anchors — 5 ports in
# docs/deployment.md, and 2 that are deliberate illustrations OF the shape (the
# aihub#411 table quoting the gap, and its §5 quoting the episode). Not one is a
# live citation, which is a second reason not to gate this form here.
CODE_SPAN_RANGE_ANCHOR = re.compile(r"`:(\d+)-(\d+)`")

# SHAPE 3 (aihub#439). The filename-less anchor written in PROSE — ` :157`,
# `at :2698-2709`, `**:981**`. Both single and range, because outside a code span
# the port ambiguity that stops SHAPE 2 does not arise.
#
# THE DISCRIMINATOR IS FORMATTING, NOT MAGNITUDE. Two halves, both measured
# against all 63 markdown files under docs/ on 2026-09-08 (385dcef):
#
#   (i) Code spans are blanked first, so every port in docs/deployment.md and
#       every host-qualified `localhost:8080` / `<host>:8080` / `-p 8080:8080`
#       is out of scope by construction. This is the "discriminator that clears
#       docs/deployment.md" the SHAPE 2 note above asked for.
#  (ii) The colon must OPEN a token: it is preceded by whitespace, by `*` (bold
#       wrapping, as in `**:981**`), by `(` or `[`, or by the line start. A colon
#       preceded by anything else is a separator inside a larger token, which is
#       what `viewer:1, writer:2, maintainer:3`, `{"current":2,"total":4}`,
#       `http://<host>:8080` and `10:30` all are. Those four classes are live in
#       docs/ today and none of them fires.
#
# FENCED CODE BLOCKS ARE DELIBERATELY IN SCOPE. Skipping them would be the
# obvious simplification and it is wrong: 4 of the 15 real bare anchors in docs/
# live inside one — a ```d2 diagram in
# docs/superpowers/archive/2026-08-06-memory-recall-ranking-plan.md, whose own
# banner claims "All line numbers have been removed (aihub#352)" precisely
# because the gate could not see into the fence. A d2 label is authored prose
# that renders to an image, not quoted code. 27% of the findings is too large a
# hole to open for tidiness.
#
# THE PRICE, stated because it is real: a port written in prose shape inside a
# fence fires. There was exactly one in docs/ (`releases :8080`, in a shell
# comment in docs/deployment.md), and aihub#439 reworded it to `port 8080` rather
# than allowlist it — `port 8080` is unambiguous prose, and a doc that has to
# write a bare port has a shorter way to say it than a citation ever does.
PROSE_LINE_ANCHOR = re.compile(
    r"(?:^|(?<=\s)|(?<=[*(\[])):(\d+)(?:-(\d+))?(?![\w%-])"
)

# Matches that are NOT citations, keyed (docs-relative path, exact token).
#
# THIS IS THE ONLY ESCAPE HATCH IN C1, AND IT LIVES IN THE GATE'S OWN FILE ON
# PURPOSE: widening it is a reviewable edit to this script, not a doc edit that
# slips through under a docs-only diff. Each entry needs a reason. Do not add an
# entry to make a real citation pass — replace the citation with a semantic
# anchor instead, which is the whole point of C1. aihub#439 widened C1 by three
# shapes and converted 15 anchors across two files; it added no entry here, and
# it covers only SHAPE 1 by design, so widening a shape is never payable from
# this map.
#
# Keyed by (path, token) rather than by exact line text so that reflowing the
# surrounding prose does not break the build. docs/design/polyforge-v1-design.md
# is 208 KB and actively edited; an exact-line key would be a tripwire on
# unrelated work.
C1_ALLOWED = {
    (
        "design/polyforge-v1-design.md",
        "auth.go:42",
    ): "Illustrative sample payload, not a citation: appears inside two fenced "
    "examples of an artifact_summary / previous_context body ('修了 auth.go:42 "
    "的空指针'). There is no auth.go in this repo at all, and there never was — "
    "the string is stand-in prose for 'some file, some line' in a schema "
    "example. Nothing rots when the code moves.",
}


FORM = (
    "The required form is a semantic anchor: the path-qualified file plus the "
    "symbol in parentheses, e.g. `internal/domain/memory.go` (`UpdateMemory`) "
    "— C2 verifies the file half. See README.md, \"How docs cite code\"."
)

# The heading and the first sentence of the section FORM points at. Two tokens,
# not one: a heading on its own can survive the section being emptied.
CITATION_FORM_HEADING = "### How docs cite code"
CITATION_FORM_RULE = "Cite the file plus the symbol"


def check_citation_form_is_documented() -> list[str]:
    """The rule must have a home outside this gate, and the pointer must resolve.

    Every C1 error above sends the author to README.md's "How docs cite code".
    A pointer nothing checks is the same class of rot C1 exists to close, so it
    is checked: if that section is renamed or deleted, this fails as INSTRUMENT
    failure rather than letting the gate keep citing a section that is gone.

    The section lives in README.md and deliberately NOT under docs/, because
    stating the forbidden form requires writing it out — `memory.go:1444` and
    the two bare-anchor shapes — and C1 would fire on all three inside docs/.
    """
    path = os.path.join(REPO_ROOT, "README.md")
    if not os.path.exists(path):
        return [
            f"{path} is missing, so C1's error messages point doc authors at a "
            "file that does not exist. The citation rule needs a home outside "
            "this gate (aihub#439): a rule only a linter states is a rule "
            "nobody is briefed on."
        ]
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    missing = [
        token
        for token in (CITATION_FORM_HEADING, CITATION_FORM_RULE)
        if token not in text
    ]
    if missing:
        return [
            "README.md no longer contains "
            + " or ".join(repr(token) for token in missing)
            + ", but every C1 error message sends the author to that section. "
            "Restore it, or move it and update FORM in this file — do not leave "
            "the pointer dangling (aihub#439)."
        ]
    return []


def check_c1_no_line_citations(paths: list[str]) -> list[str]:
    """No line-number citations in docs/, in any of C1's three shapes."""
    errors = []
    for path in paths:
        rel = os.path.relpath(path, DOCS)
        with open(path, encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, 1):
                for match in FILE_LINE_CITATION.finditer(line):
                    token = f"{match.group(1)}:{match.group(2)}"
                    if (rel, token) in C1_ALLOWED:
                        continue
                    errors.append(
                        f"docs/{rel}:{lineno}: line-number citation `{token}`. "
                        "Line numbers rot silently — nothing goes red when the "
                        f"file moves under it. {FORM}"
                    )
                for match in CODE_SPAN_RANGE_ANCHOR.finditer(line):
                    errors.append(
                        f"docs/{rel}:{lineno}: filename-less line anchor "
                        f"`:{match.group(1)}-{match.group(2)}`. It does not say "
                        f"which file, so it can never be checked or repaired. "
                        f"{FORM}"
                    )
                # SHAPE 3 reads the line with its code spans blanked out, so a
                # port or a host-qualified address inside backticks is out of
                # scope. Blank to spaces of equal width, not to nothing: the
                # column in the message has to keep pointing at the real text.
                prose = CODE_SPAN.sub(lambda m: " " * len(m.group(0)), line)
                for match in PROSE_LINE_ANCHOR.finditer(prose):
                    errors.append(
                        f"docs/{rel}:{lineno}: filename-less line anchor "
                        f"`{match.group(0)}` in prose. It does not say which "
                        "file, so it can never be checked or repaired, and it "
                        "usually sits next to a symbol name that already says "
                        f"where to look — delete it or replace it. {FORM} "
                        "If this is a PORT and not a line number, write it as "
                        "`port 8080` or code-format it (`` `:8080` ``); a bare "
                        "`:8080` in prose is the citation shape."
                    )
    return errors


# ─── C2 ───────────────────────────────────────────────────────────────────────

# Path-qualified Go files only: `internal/domain/memory.go`, not a bare
# `memory.go`. A bare filename is ambiguous across packages, so requiring the
# path is what makes the reference checkable at all.
GO_PATH_REF = re.compile(r"(?<![\w/.-])((?:[\w.-]+/)+[\w.-]+\.go)(?![\w/.-])")


def check_c2_referenced_go_files_exist(paths: list[str]) -> list[str]:
    """Every path-qualified *.go referenced in docs/superpowers/ must exist."""
    errors = []
    for path in paths:
        rel = os.path.relpath(path, DOCS)
        seen: dict[str, int] = {}
        with open(path, encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, 1):
                for match in GO_PATH_REF.finditer(line):
                    seen.setdefault(match.group(1), lineno)
        for ref, lineno in sorted(seen.items()):
            if not os.path.exists(os.path.join(REPO_ROOT, ref)):
                errors.append(
                    f"docs/{rel}:{lineno}: references `{ref}`, which does not "
                    "exist. Either the file moved (update the reference) or the "
                    "doc describes code that never landed (say so in the doc)."
                )
    return errors


# ─── C3 ───────────────────────────────────────────────────────────────────────

DOC_TOTAL = re.compile(r"\*\*(\d+) `pf_\*` tools\*\*")
DOC_SECTION = re.compile(r"^## (.+?) \((\d+)\)")
DOC_TOOL_ROW = re.compile(r"^\|\s*`(pf_[a-z_]+)`\s*\|")


def parse_mcp_tools_md(text: str):
    """Return (declared_total, [(section, declared_count, [tools])])."""
    total_match = DOC_TOTAL.search(text)
    total = int(total_match.group(1)) if total_match else None

    sections: list[tuple[str, int, list[str]]] = []
    for line in text.splitlines():
        section_match = DOC_SECTION.match(line)
        if section_match:
            sections.append(
                (section_match.group(1), int(section_match.group(2)), [])
            )
            continue
        row_match = DOC_TOOL_ROW.match(line)
        if row_match and sections:
            sections[-1][2].append(row_match.group(1))
    return total, sections


def check_c3_tool_inventory(doc_text: str, schema_tools: set[str]) -> list[str]:
    """docs/mcp-tools.md's tool inventory must equal the schema dump's."""
    errors = []
    total, sections = parse_mcp_tools_md(doc_text)

    documented: list[str] = []
    for name, declared, tools in sections:
        documented.extend(tools)
        if declared != len(tools):
            errors.append(
                f"docs/mcp-tools.md: section '{name}' heading declares "
                f"({declared}) but the table has {len(tools)} tool rows."
            )

    # Set equality, not just counts: equal counts with diverging names is
    # exactly what a count-only check cannot see.
    doc_set = set(documented)
    if len(doc_set) != len(documented):
        dupes = sorted({t for t in documented if documented.count(t) > 1})
        errors.append(
            f"docs/mcp-tools.md: tool(s) listed more than once: {', '.join(dupes)}"
        )

    missing = sorted(schema_tools - doc_set)
    if missing:
        errors.append(
            "docs/mcp-tools.md: these tools are registered but undocumented: "
            + ", ".join(missing)
            + ". `polyforge dump-mcp-schemas` is the authority; add a table row."
        )

    extra = sorted(doc_set - schema_tools)
    if extra:
        errors.append(
            "docs/mcp-tools.md: these tools are documented but not registered: "
            + ", ".join(extra)
            + ". They were removed or renamed; drop the row."
        )

    if total is None:
        errors.append(
            "docs/mcp-tools.md: could not find the '**N `pf_*` tools**' total. "
            "It is the headline copy of the registry size and must stay checkable."
        )
    elif total != len(schema_tools):
        errors.append(
            f"docs/mcp-tools.md: header says **{total} `pf_*` tools** but "
            f"{len(schema_tools)} are registered."
        )

    return errors


# ─── C4 ───────────────────────────────────────────────────────────────────────


class InstrumentFailure(Exception):
    """A check could not run, which is NOT the same as it finding nothing wrong.

    Raised by C4 and C5. A recount over zero rows satisfies any tally, and a set
    comparison against an empty set passes for the same empty reason, so the
    instrument has to prove it found what it grades before its silence can mean
    anything. Raised rather than appended to the error list so main() can exit 2
    (instrument) instead of 1 (docs drift): the two need different fixes, and
    conflating them is how a gate becomes a no-op that still prints OK.
    """


# §6 of the aihub#411 audit prints its ruling rows and then, further down, four
# hand-maintained counts over them. The headings bound the row block; §6.3 is the
# first heading after the tier-2 rows, so it is the end marker rather than a
# start marker of anything C4 reads.
#
# The trailing space in each marker is load-bearing: without it `### 6.1` also
# prefix-matches a future `### 6.10`, and the block would silently bound itself
# on the wrong heading. Requiring the space turns a renumbering into a loud
# instrument failure instead of a quietly-wrong recount.
AUDIT_411_SECTION_OPEN = "## 6. "
AUDIT_411_ROWS_START = "### 6.1 "
AUDIT_411_ROWS_END = "### 6.3 "

# THE ROW DISCRIMINANT. Every §6.1/§6.2 ruling row opens with a bold row id
# (`| **T1-1** |`), and between §6.1 and §6.3 nothing else does — the disposition
# vocabulary table opens its rows `| `tag` |` and sits ABOVE §6.1, so the bound
# and the prefix have to fail together before a non-row is counted.
AUDIT_411_ROW_PREFIX = "| **"

# The disposition vocabulary table, which supplies C4's recogniser. Parsed rather
# than hardcoded to buy one thing: a fifth tag defined here and used in a row but
# left out of the tally then goes RED, instead of being invisible to a checker
# that only knows four names. The `N` alternative is literal — the table writes
# `wi-filed:aihub#N` as a template, while `superseded-by:aihub#416` names its one
# real target.
AUDIT_411_VOCAB_ROW = re.compile(r"^\|\s*`([a-z][a-z-]*)(?::aihub#(?:N|\d+))?`\s*\|")

# The tally paragraph, from its own opening sentence to the blank line that ends
# it. It is hard-wrapped, and `superseded-by:aihub#416` is separated from its
# **4** by a newline, so it must be read as one joined blob and not line by line.
AUDIT_411_TALLY = re.compile(
    r"\*\*Counts over the (\d+) rows\.\*\*(.*?)(?:\n\n|\Z)", re.S
)

# A second copy of the row count, in the same paragraph.
AUDIT_411_UNIVERSAL = re.compile(r"\*\*All (\d+) rulings\*\*")

# A THIRD projection of the same rows: DISTINCT wi numbers rather than tag
# occurrences. It drifts differently from the tally — the two disagree by exactly
# the five wi's that two rows each name — so it needs its own assertion, not a
# derivation from the `wi-filed` count.
AUDIT_411_FILED_RANGE = re.compile(
    r"\*\*Work items filed by this adjudication:\s*`aihub#(\d+)`\s*(?:…|\.\.\.)\s*"
    r"`aihub#(\d+)`\*\*\s*\((\d+)\)"
)

# The tag whose `:aihub#N` suffixes the range claim above summarises.
AUDIT_411_FILED_TAG = "wi-filed"


def audit_411_tag_patterns(vocab: list[str]) -> tuple[re.Pattern, re.Pattern]:
    """Return (row-cell token, tally tag-plus-count) patterns for `vocab`.

    Restricted to the vocabulary §6 itself defines, deliberately: a generic
    `[a-z-]+` inside backticks also matches `dump-mcp-schemas` and `tools/list`,
    both of which are live in T1-10's disposition cell, so a wide recogniser
    would inflate the recount with prose and report drift that is not there.
    Longest name first so no tag can shadow another as a prefix.
    """
    names = "|".join(re.escape(tag) for tag in sorted(vocab, key=len, reverse=True))
    token = re.compile(r"`(" + names + r")(?::aihub#(\d+))?`")
    pair = re.compile(r"`(" + names + r")(?::aihub#\d+)?`\s*\*\*(\d+)\*\*")
    return token, pair


def check_c4_411_tally(text: str) -> list[str]:
    """§6's counted claims must equal a recount of §6.1+§6.2's rows.

    Counts OCCURRENCES, not rows. The paragraph states its own rule verbatim —
    "Rows carrying two tags are counted under each" — so the per-tag sum exceeds
    the row count by design: 40 over 35 rows as of aihub#488.

    A tag a row records as RETRACTED still counts. T1-10 keeps its superseded
    `wi-filed:aihub#437` struck through beside the `already-landed:aihub#419`
    that replaced it, and the published 25 includes it — two independent hand
    recounts agreed on 25. Skipping `~~...~~` is the other defensible rule and it
    would make the recount 24, i.e. it would change a published number, which is
    an editorial call and not a lint's to make. §6 states that rule next to its
    counting rule so the document and this function cannot drift apart, and the
    self-test pins it so flipping it is a visible decision rather than a diff
    nobody reads.
    """
    lines = text.splitlines()

    def first_line_starting(prefix: str) -> int | None:
        return next(
            (i for i, line in enumerate(lines) if line.startswith(prefix)), None
        )

    open_idx = first_line_starting(AUDIT_411_SECTION_OPEN)
    start_idx = first_line_starting(AUDIT_411_ROWS_START)
    end_idx = first_line_starting(AUDIT_411_ROWS_END)
    if None in (open_idx, start_idx, end_idx) or not open_idx < start_idx < end_idx:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: could not locate §6's row block. Expected lines "
            f"starting {AUDIT_411_SECTION_OPEN!r}, {AUDIT_411_ROWS_START!r} and "
            f"{AUDIT_411_ROWS_END!r}, in that order. If §6 was renumbered, "
            "retarget C4 — do not let a recount pass by finding nothing."
        )

    vocab = []
    for line in lines[open_idx:start_idx]:
        match = AUDIT_411_VOCAB_ROW.match(line)
        if match:
            vocab.append(match.group(1))
    if not vocab:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: §6's disposition vocabulary table yielded no "
            "tags, so C4 has nothing to recognise and would report every tally "
            "as matching an empty recount. Restore the table, or retarget "
            "AUDIT_411_VOCAB_ROW at wherever the vocabulary now lives."
        )

    rows = [
        line
        for line in lines[start_idx:end_idx]
        if line.startswith(AUDIT_411_ROW_PREFIX)
    ]
    if not rows:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: found no §6.1/§6.2 ruling rows (lines starting "
            f"{AUDIT_411_ROW_PREFIX!r}) between {AUDIT_411_ROWS_START!r} and "
            f"{AUDIT_411_ROWS_END!r}. A recount over zero rows satisfies every "
            "tally, so this is instrument failure, not a clean result."
        )

    token, pair = audit_411_tag_patterns(vocab)
    counted: dict[str, int] = {}
    filed: set[int] = set()
    for row in rows:
        cells = [cell.strip() for cell in row.strip().strip("|").split("|")]
        if len(cells) != 3:
            raise InstrumentFailure(
                f"{AUDIT_411_REL}: §6 row {cells[0]!r} has {len(cells)} cells, "
                "not the 3 of `| row | ruling | disposition |`. C4 reads the "
                "third cell, so it cannot recount a table it can no longer parse."
            )
        for match in token.finditer(cells[2]):
            counted[match.group(1)] = counted.get(match.group(1), 0) + 1
            if match.group(1) == AUDIT_411_FILED_TAG and match.group(2):
                filed.add(int(match.group(2)))

    tally = AUDIT_411_TALLY.search(text)
    if tally is None:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: could not find the tally paragraph — the one "
            'opening "**Counts over the N rows.**". It is the claim C4 grades; '
            "if it was reworded, reword AUDIT_411_TALLY with it rather than "
            "leaving a check that grades nothing."
        )
    declared_rows = int(tally.group(1))
    paragraph = " ".join(tally.group(2).split())

    declared: dict[str, int] = {}
    for match in pair.finditer(paragraph):
        tag, number = match.group(1), int(match.group(2))
        if tag in declared:
            raise InstrumentFailure(
                f"{AUDIT_411_REL}: the tally paragraph gives `{tag}` a count "
                f"twice ({declared[tag]} and {number}). Which one is the claim "
                "is then undecidable; state it once."
            )
        declared[tag] = number
    if not declared:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: the tally paragraph names no `tag` **N** pair, "
            "so there is no claim to grade. Restore the counts, or retire C4 "
            "with them — an absent check reports green."
        )

    errors: list[str] = []

    if declared_rows != len(rows):
        errors.append(
            f'{AUDIT_411_REL}: the tally says "Counts over the {declared_rows} '
            f'rows" but §6.1+§6.2 hold {len(rows)}. They are one fact: fix the '
            "number, or the rows."
        )

    universal = AUDIT_411_UNIVERSAL.search(paragraph)
    if universal is None:
        raise InstrumentFailure(
            f'{AUDIT_411_REL}: the tally paragraph no longer claims "**All N '
            'rulings**" are written back as memory. That is a second copy of '
            "the row count and C4 grades it; restore the sentence, or drop the "
            "assertion here deliberately."
        )
    if int(universal.group(1)) != len(rows):
        errors.append(
            f"{AUDIT_411_REL}: the tally claims **All {universal.group(1)} "
            f"rulings** are written back as memory, but §6.1+§6.2 hold "
            f"{len(rows)} rows. The write-back is universal, so the two numbers "
            "are the same fact."
        )

    for tag in sorted(set(declared) | set(counted)):
        if tag not in declared:
            errors.append(
                f"{AUDIT_411_REL}: §6's rows carry `{tag}` {counted[tag]} "
                "time(s), but the tally paragraph never names it. A tag missing "
                "from the tally is invisible to a total that still adds up."
            )
        elif tag not in counted:
            errors.append(
                f"{AUDIT_411_REL}: the tally claims `{tag}` **{declared[tag]}**, "
                "but no §6 row carries that tag. Drop it from the tally, or "
                "restore the row that used it."
            )
        elif declared[tag] != counted[tag]:
            errors.append(
                f"{AUDIT_411_REL}: the tally claims `{tag}` **{declared[tag]}**, "
                f"but §6's rows carry it {counted[tag]} time(s). This counts "
                "OCCURRENCES, per the paragraph's own rule that a row carrying "
                "two tags is counted under each — so do not reconcile it by "
                "counting rows."
            )

    filed_range = AUDIT_411_FILED_RANGE.search(text)
    if filed_range is None:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: could not find the \"Work items filed by this "
            'adjudication: `aihub#A` … `aihub#B`** (N)" claim. C4 grades it as a '
            "third projection of the same rows; restore it, or drop the "
            "assertion here deliberately."
        )
    low, high, total = (int(group) for group in filed_range.groups())
    if not filed:
        raise InstrumentFailure(
            f"{AUDIT_411_REL}: no `{AUDIT_411_FILED_TAG}:aihub#N` token was "
            "found in any §6 row, so the filed-wi range would be graded against "
            "an empty set. Either the tag was renamed — retarget "
            "AUDIT_411_FILED_TAG — or the row parse is broken."
        )
    if len(filed) != total:
        errors.append(
            f"{AUDIT_411_REL}: the tally claims ({total}) work items filed, but "
            f"§6's rows name {len(filed)} distinct "
            f"`{AUDIT_411_FILED_TAG}:aihub#N` targets. This is DISTINCT wi's, "
            f"not occurrences — several wi's are named by two rows, which is "
            "why it does not equal the tag count above."
        )
    if (min(filed), max(filed)) != (low, high):
        errors.append(
            f"{AUDIT_411_REL}: the tally claims the filed range `aihub#{low}` … "
            f"`aihub#{high}`, but §6's rows span `aihub#{min(filed)}` … "
            f"`aihub#{max(filed)}`."
        )

    return errors


# ─── C5 ───────────────────────────────────────────────────────────────────────

RESOURCE_EVENTS_REL = "internal/domain/resource_events.go"
RESOURCE_EVENTS_GO = os.path.join(REPO_ROOT, RESOURCE_EVENTS_REL)
DESIGN_DOC_REL = "docs/design/polyforge-v1-design.md"
DESIGN_DOC_MD = os.path.join(REPO_ROOT, DESIGN_DOC_REL)

# A const line binding a cause name to its wire string:
#     lockCauseWICancelled = "wi_cancelled"
# The exported alias `LockCauseDerivationRetired = lockCauseDerivationRetired`
# carries no string literal, so this pattern skips it — correct, because it is a
# second SPELLING of a value already collected, not a second value.
GO_LOCK_CAUSE_RE = re.compile(r'^\s*lockCause\w+\s*=\s*"([a-z_]+)"\s*$', re.M)

# The design doc's two payload schemas. Each is introduced by a comment line at
# column 0 inside a ```typescript fence, and each carries a `cause:` union that
# may wrap over several lines and ends at the first `;`.
DOC_LOCK_EVENT_MARKERS = ("lock_acquired", "lock_released")


def _doc_cause_union(design_text: str, event: str) -> set[str]:
    """Pull one `cause:` union out of the design doc's payload schema block."""
    marker = re.search(rf"^// {event}\b", design_text, re.M)
    if marker is None:
        raise InstrumentFailure(
            f"C5 could not find the `// {event}` payload schema in "
            f"{DESIGN_DOC_REL}. If the block moved or was renamed, retarget C5; "
            "do not let it pass by finding nothing."
        )
    # Bounded window rather than the rest of the file: an unterminated union
    # would otherwise swallow the next schema and report its causes as this
    # one's.
    window = design_text[marker.start() : marker.start() + 2000]
    union = re.search(r"^\s*cause:\s*(.*?);", window, re.M | re.S)
    if union is None:
        raise InstrumentFailure(
            f"C5 found `// {event}` in {DESIGN_DOC_REL} but no `cause:` union "
            "within the following 2000 characters."
        )
    values = set(re.findall(r'"([a-z_]+)"', union.group(1)))
    if not values:
        raise InstrumentFailure(
            f"C5 parsed an EMPTY `cause:` union for `{event}` in "
            f"{DESIGN_DOC_REL}. An empty set compares clean against anything."
        )
    return values


def check_c5_lock_cause_union(design_text: str, go_text: str) -> list[str]:
    """Assert the doc's two lock-cause unions cover exactly the Go constants.

    Set equality on the UNION of the two doc unions, not on either one alone.
    That is a deliberate limit and it is stated in the doc beside the tables:
    which of the two events a cause rides on is not derivable from the constant
    block — `derivation_retired` has no Go writer at all (migration 0038 emits
    it) — so a hand-maintained acquired/released split in this file would be a
    third copy of the very fact the check exists to stop copying. What C5 does
    catch is the drift that actually happened twice: a new `lockCause*` constant
    landing with neither union updated (`wi_cancelled`/aihub#355,
    `derivation_retired`/aihub#416, both found by review in aihub#518).
    """
    go_causes = set(GO_LOCK_CAUSE_RE.findall(go_text))
    if not go_causes:
        raise InstrumentFailure(
            f"C5 found no `lockCause* = \"...\"` constants in "
            f"{RESOURCE_EVENTS_REL}. Either the block moved or the pattern "
            "rotted; either way the check did not run."
        )

    doc_causes: set[str] = set()
    for event in DOC_LOCK_EVENT_MARKERS:
        doc_causes |= _doc_cause_union(design_text, event)

    errors = []
    for cause in sorted(go_causes - doc_causes):
        errors.append(
            f"C5: cause {cause!r} is defined in {RESOURCE_EVENTS_REL} but "
            f"appears in neither lock event's `cause:` union in "
            f"{DESIGN_DOC_REL}. Add it to the union it is emitted on."
        )
    for cause in sorted(doc_causes - go_causes):
        errors.append(
            f"C5: cause {cause!r} is listed in a lock event's `cause:` union in "
            f"{DESIGN_DOC_REL} but no `lockCause*` constant in "
            f"{RESOURCE_EVENTS_REL} defines it. The doc names a value the "
            "server cannot emit."
        )
    return errors


# ─── self-test ────────────────────────────────────────────────────────────────


def self_test() -> int:
    """Assert each check fires on a synthetic drift. A gate nobody has seen red
    is an assertion, not a gate."""
    failures = []

    def expect(label, errors, should_fire):
        fired = bool(errors)
        if fired != should_fire:
            failures.append(
                f"{label}: expected {'errors' if should_fire else 'no errors'}, "
                f"got {errors!r}"
            )

    # C3: the drift that matters — a tool registered but not documented.
    schema = {"pf_a", "pf_b", "pf_c"}
    good = (
        "**3 `pf_*` tools**\n"
        "## Group (3) - `tools_x.go`\n"
        "| tool | purpose |\n"
        "|---|---|\n"
        "| `pf_a` | . |\n| `pf_b` | . |\n| `pf_c` | . |\n"
    )
    expect("C3 clean", check_c3_tool_inventory(good, schema), False)
    expect(
        "C3 undocumented tool",
        check_c3_tool_inventory(good, schema | {"pf_new"}),
        True,
    )
    expect(
        "C3 removed tool",
        check_c3_tool_inventory(good, {"pf_a", "pf_b"}),
        True,
    )
    # Count right, names wrong — the case a count-only check passes.
    swapped = good.replace("| `pf_c` | . |", "| `pf_zzz` | . |")
    expect(
        "C3 same count, different names",
        check_c3_tool_inventory(swapped, schema),
        True,
    )
    # Section heading count out of step with its own table.
    bad_count = good.replace("## Group (3)", "## Group (4)")
    expect("C3 section miscount", check_c3_tool_inventory(bad_count, schema), True)
    expect(
        "C3 header total wrong",
        check_c3_tool_inventory(good.replace("**3 `pf_*`", "**4 `pf_*`"), schema),
        True,
    )

    # C4. A synthetic §6 with the same shapes the real one has: a two-tag row, a
    # row whose disposition was retracted and struck through, a hard-wrapped
    # tally with one tag/count pair split across the line break, and a
    # vocabulary entry no row uses.
    c4_doc = "\n".join(
        [
            "## 6. Adjudication",
            "",
            "| tag | meaning |",
            "|---|---|",
            "| `rule-recorded` | a policy statement |",
            "| `wi-filed:aihub#N` | a behaviour change |",
            "| `superseded-by:aihub#416` | dissolved, not answered |",
            "| `already-landed:aihub#N` | executed before adjudication |",
            "",
            "### 6.1 Tier 1",
            "",
            "| row | ruling | disposition |",
            "|---|---|---|",
            "| **T1-1** | r | `wi-filed:aihub#431` |",
            "| **T1-2** | r | `already-landed:aihub#432` · `rule-recorded` |",
            "",
            "### 6.2 Tier 2",
            "",
            "| row | ruling | disposition |",
            "|---|---|---|",
            "| **T2-1** | r | ~~`wi-filed:aihub#433`~~ → "
            "**`already-landed:aihub#434`** |",
            "",
            "**Counts over the 3 rows.** `wi-filed` **2** · `already-landed`",
            "**2** · `rule-recorded` **1**. Rows carrying two tags are counted under",
            "each, and a tag a row records as retracted still counts where the row",
            "keeps it. **All 3 rulings** are written back as memory.",
            "",
            "**Work items filed by this adjudication: `aihub#431` … `aihub#433`** (2).",
            "",
            "### 6.3 Memory write-back index",
            "",
        ]
    )

    def c4(label, doc, should_fire):
        expect(label, check_c4_411_tally(doc), should_fire)

    def c4_instrument(label, doc):
        try:
            check_c4_411_tally(doc)
        except InstrumentFailure:
            return
        failures.append(
            f"{label}: expected InstrumentFailure, got a completed run. A check "
            "that cannot find §6 must fail loudly, not report clean."
        )

    c4("C4 clean", c4_doc, False)
    # An unused vocabulary entry is not drift: `superseded-by` is defined in the
    # fixture's vocabulary, carried by no row, and absent from the tally — and
    # the clean case above passes anyway. Checked with an `if`, not an `assert`:
    # `assert` is stripped under `python3 -O` and would void this silently.
    if "| `superseded-by:aihub#416` |" not in c4_doc:
        failures.append(
            "C4 fixture no longer defines an UNUSED vocabulary tag, so the "
            "clean case above stopped proving that one is not reported."
        )

    # THE aihub#488 DRIFT ITSELF, both directions: the number moves, or the row
    # does. Either one alone must go red.
    c4(
        "C4 tally count edited away from the rows",
        c4_doc.replace("\n**2** ·", "\n**3** ·"),
        True,
    )
    c4(
        "C4 a row's tag changed under a fixed tally",
        c4_doc.replace("| `wi-filed:aihub#431` |", "| `rule-recorded` |"),
        True,
    )
    c4(
        "C4 a row loses its second tag",
        c4_doc.replace("`already-landed:aihub#432` · `rule-recorded`",
                       "`already-landed:aihub#432`"),
        True,
    )
    c4(
        "C4 a row gains a tag the tally never names",
        c4_doc.replace("| `wi-filed:aihub#431` |",
                       "| `wi-filed:aihub#431` · `superseded-by:aihub#416` |"),
        True,
    )
    c4("C4 a row is deleted", c4_doc.replace(
        "| **T1-2** | r | `already-landed:aihub#432` · `rule-recorded` |\n", ""), True)
    # The retracted-tag rule, pinned. Un-striking T2-1's superseded disposition
    # leaves the same two tokens, so this fires only because the struck one is
    # COUNTED — flipping that rule has to be a visible decision, not a drift.
    c4(
        "C4 a struck-through tag still counts",
        c4_doc.replace("~~`wi-filed:aihub#433`~~ → ", ""),
        True,
    )
    c4(
        "C4 the universal write-back count drifts",
        c4_doc.replace("**All 3 rulings**", "**All 4 rulings**"),
        True,
    )
    c4(
        "C4 the filed-wi total drifts",
        c4_doc.replace("`aihub#433`** (2)", "`aihub#433`** (3)"),
        True,
    )
    c4(
        "C4 the filed-wi range endpoint drifts",
        c4_doc.replace("`aihub#431` …", "`aihub#430` …"),
        True,
    )

    # Anti-vacuity. Every one of these would let a recount "pass" against
    # nothing, which is the failure mode the paragraph C1's header warns about.
    c4_instrument(
        "C4 fails loudly when §6's row block cannot be located",
        c4_doc.replace("### 6.1 Tier 1", "### 6.11 Tier 1"),
    )
    c4_instrument(
        "C4 fails loudly when the row block is empty",
        re.sub(r"^\| \*\*.*\n", "", c4_doc, flags=re.M),
    )
    c4_instrument(
        "C4 fails loudly when the vocabulary table is gone",
        c4_doc.replace("| `rule-recorded` | a policy statement |\n", "")
        .replace("| `wi-filed:aihub#N` | a behaviour change |\n", "")
        .replace("| `superseded-by:aihub#416` | dissolved, not answered |\n", "")
        .replace("| `already-landed:aihub#N` | executed before adjudication |\n", ""),
    )
    c4_instrument(
        "C4 fails loudly when the tally paragraph is reworded away",
        c4_doc.replace("**Counts over the 3 rows.**", "**Tally.**"),
    )
    c4_instrument(
        "C4 fails loudly when the tally states no tag/count pair",
        c4_doc.replace("`wi-filed` **2** · `already-landed`\n**2** · "
                       "`rule-recorded` **1**.", "as above."),
    )
    c4_instrument(
        "C4 fails loudly when a row's cell count changes",
        c4_doc.replace("| **T2-1** | r |", "| **T2-1** |"),
    )
    c4_instrument(
        "C4 fails loudly when the tally counts one tag twice",
        c4_doc.replace("`wi-filed` **2** ·", "`wi-filed` **2** · `wi-filed` **9** ·"),
    )
    c4_instrument(
        "C4 fails loudly when the universal write-back claim is deleted",
        c4_doc.replace("**All 3 rulings** are written back as memory.", ""),
    )
    c4_instrument(
        "C4 fails loudly when the filed-wi claim is deleted",
        c4_doc.replace(
            "**Work items filed by this adjudication: `aihub#431` … "
            "`aihub#433`** (2).\n", ""),
    )
    c4_instrument(
        "C4 fails loudly when no row carries the filed tag it summarises",
        c4_doc.replace("`wi-filed:aihub#431`", "`rule-recorded`")
        .replace("~~`wi-filed:aihub#433`~~", "~~`rule-recorded`~~"),
    )

    # C1 and C2 are driven through the real check functions against a temporary
    # docs tree, not through bare `assert`s on their regexes. Two reasons: a
    # regex assertion does not prove the function reports anything, and `assert`
    # is stripped entirely under `python3 -O`, which would void these silently.
    global REPO_ROOT, DOCS
    real_root, real_docs = REPO_ROOT, DOCS
    tmp = tempfile.mkdtemp(prefix="pf-docs-selftest-")
    try:
        REPO_ROOT = tmp
        DOCS = os.path.join(tmp, "docs")
        os.makedirs(os.path.join(DOCS, "design"))
        os.makedirs(os.path.join(tmp, "internal", "domain"))
        with open(os.path.join(tmp, "internal", "domain", "memory.go"), "w") as fh:
            fh.write("package domain\n")

        def write(rel, text):
            path = os.path.join(DOCS, rel)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(text)
            return path

        cases = [
            ("C1 clean file", "clean.md",
             "`UpdateMemory` in `internal/domain/memory.go`\n", False),
            ("C1 go citation fires", "cited.md",
             "see internal/domain/memory.go:1444 for it\n", True),
            ("C1 non-Go citation fires", "sql.md",
             "migrations/0006_events.sql:141 is NOT NULL\n", True),
            # SHAPE 1 covers markdown as of aihub#439: the design doc is 4,620
            # actively-edited lines, so a citation into it rots fastest of all.
            ("C1 md citation fires", "md_cited.md",
             "see docs/design/polyforge-v1-design.md:4620 for the DDL\n", True),
            ("C1b range anchor fires", "range.md",
             "the cursor predicate at `:1195-1202` and\n", True),
            # Ports must NOT fire: docs/deployment.md carries five of these, and
            # a check that reports them buries real findings in false positives.
            ("C1b ports do not fire", "ports.md",
             "HTTP on `:8080`, TEI on `:8085`, `-p 8080:8080`\n", False),
            # SHAPE 3 (aihub#439). Every one of these is a real form taken from
            # docs/ as it stood at 385dcef, fired or spared for a stated reason.
            ("C1c prose single anchor fires", "prose_single.md",
             "the restriction is a condition on `lockType` and is :476\n", True),
            ("C1c prose range anchor fires", "prose_range.md",
             "the code says so itself at :2698-2709 (\"obeys half\")\n", True),
            ("C1c bold-wrapped anchor fires", "prose_bold.md",
             "the prose is stale and **:981** still tells the caller\n", True),
            ("C1c anchor trailing a code span fires", "prose_after_span.md",
             "`queryFloat` :157; `queryFloatInRange` :169) and Rule 2\n", True),
            # The trailing guard deliberately does NOT exclude `.`, so an anchor
            # that ends a sentence still fires. A false negative here is rot that
            # ships; there is no prose in docs/ that pays for the exclusion.
            ("C1c anchor ending a sentence fires", "prose_period.md",
             "the dividing question is stated at `queryInt` :157.\n", True),
            # Fenced blocks are IN SCOPE: 4 of the 15 real anchors aihub#439
            # converted were d2 diagram labels inside a fence, in a file whose
            # banner already claimed every line number had been removed.
            ("C1c anchor inside a fenced block fires", "prose_fence.md",
             '```d2\nsites: "four sites" {\n'
             '  s1: "ORDER BY  :1238\\nwas NULLS LAST"\n}\n```\n', True),
            # The price of covering fences, asserted rather than discovered: a
            # port written in the citation shape fires. One existed in docs/ and
            # was reworded to `port 8080`, not allowlisted.
            ("C1c prose-shaped port fires (documented cost)", "prose_port.md",
             "docker stop aihub   # graceful SIGTERM; releases :8080\n", True),
            # The code-formatted SINGLE anchor stays OPEN — it is the port
            # ambiguity owned by aihub#406, and aihub#439's ruling left it there.
            # This case exists so closing it is a visible decision, not a drift.
            ("C1c code-formatted single stays open (aihub#406)", "span_single.md",
             "a single `` `:563` `` is indistinguishable from a port\n", False),
            # Colons that separate inside a larger token are not anchors. All
            # four classes are live in docs/ today.
            ("C1c host-qualified port does not fire", "prose_host.md",
             "[ok] config: http://<host>:8080 (built-in default)\n", False),
            ("C1c json literal does not fire", "prose_json.md",
             '"stalled_at_step": {"step_id":"code_change","current":2}\n', False),
            ("C1c role ladder does not fire", "prose_ladder.md",
             "a map, viewer:1, writer:2, maintainer:3, ranks them\n", False),
            ("C1c clock time does not fire", "prose_clock.md",
             "the sweep runs at 03:30 every night\n", False),
            # The allowlist must excuse its token ONLY inside its own file.
            ("C1 allowlist suppresses in its own file",
             "design/polyforge-v1-design.md", 'summary: "fixed auth.go:42"\n', False),
            ("C1 allowlist does NOT suppress elsewhere", "elsewhere.md",
             'summary: "fixed auth.go:42"\n', True),
        ]
        for label, rel, text, should_fire in cases:
            expect(label, check_c1_no_line_citations([write(rel, text)]), should_fire)

        c2_cases = [
            ("C2 existing path", "ref_ok.md",
             "see `internal/domain/memory.go` (`Recall`)\n", False),
            ("C2 missing path fires", "ref_bad.md",
             "see `internal/domain/gone.go` (`Recall`)\n", True),
            ("C2 ignores an unqualified filename", "ref_bare.md",
             "see `memory.go` on its own\n", False),
        ]
        for label, rel, text, should_fire in c2_cases:
            expect(
                label,
                check_c2_referenced_go_files_exist([write(rel, text)]),
                should_fire,
            )

        # The pointer every C1 error hands the author must resolve. Same style
        # as above: through the real function, against a temp REPO_ROOT.
        readme = os.path.join(tmp, "README.md")
        expect(
            "form-doc guard fires when README.md is absent",
            check_citation_form_is_documented(),
            True,
        )
        with open(readme, "w", encoding="utf-8") as fh:
            fh.write(f"# x\n\n{CITATION_FORM_HEADING}\n\nemptied out\n")
        expect(
            "form-doc guard fires when the heading survives but the rule does not",
            check_citation_form_is_documented(),
            True,
        )
        with open(readme, "w", encoding="utf-8") as fh:
            fh.write(f"# x\n\n{CITATION_FORM_HEADING}\n\n**{CITATION_FORM_RULE}.**\n")
        expect(
            "form-doc guard passes on a well-formed README.md",
            check_citation_form_is_documented(),
            False,
        )
    finally:
        REPO_ROOT, DOCS = real_root, real_docs
        shutil.rmtree(tmp, ignore_errors=True)

    # And against the REAL repo, not only a synthetic one. --self-test runs
    # before the real check in CI, so a deleted section reports here first.
    expect(
        "form-doc guard passes on this repo's README.md",
        check_citation_form_is_documented(),
        False,
    )
    if not os.path.exists(AUDIT_411_MD):
        failures.append(
            f"{AUDIT_411_REL} is missing, so C4 has nothing to grade. Retarget "
            "C4 or retire it; do not leave it pointing at a deleted file."
        )
    else:
        with open(AUDIT_411_MD, encoding="utf-8") as fh:
            audit_411_text = fh.read()
        try:
            expect(
                "C4 passes on this repo's aihub#411 table",
                check_c4_411_tally(audit_411_text),
                False,
            )
        except InstrumentFailure as exc:
            # Reported, not propagated: an uncaught raise here would exit with a
            # traceback and Python's own code, hiding a real finding behind a
            # crash. The main run reports the same condition as exit 2.
            failures.append(f"C4 cannot read this repo's aihub#411 table: {exc}")

    # C5. Synthetic doc + Go blocks in the same shape as the real ones: a union
    # that wraps across lines, an exported alias with no string literal, and
    # prose naming a cause OUTSIDE the union (which must not count as coverage).
    c5_go = (
        "const (\n"
        '\tlockCauseClaim      = "claim"\n'
        '\tlockCauseWICancelled = "wi_cancelled"\n'
        '\tlockCauseOrphanSweep = "orphan_sweep"\n'
        ")\n"
        "const LockCauseOrphanSweep = lockCauseOrphanSweep\n"
    )
    c5_doc = (
        "// lock_acquired  (aihub#343)\n"
        "{ resource_type: string;\n"
        '  cause: "claim";\n'
        "  op_id: string }\n"
        "\n"
        "// lock_released  (aihub#343)\n"
        "{ resource_type: string;\n"
        '  cause: "wi_cancelled"\n'
        '       |"orphan_sweep";\n'
        "  op_id: string }\n"
    )
    expect("C5 clean", check_c5_lock_cause_union(c5_doc, c5_go), False)
    expect(
        "C5 new Go constant not in either union",
        check_c5_lock_cause_union(
            c5_doc, c5_go.replace(")\n", '\tlockCauseNew = "brand_new"\n)\n', 1)
        ),
        True,
    )
    expect(
        "C5 doc names a cause the code cannot emit",
        check_c5_lock_cause_union(
            c5_doc.replace('"orphan_sweep";', '"orphan_sweep"|"ghost";'), c5_go
        ),
        True,
    )
    # The exact failure aihub#518 fixed: the constant exists, the changelog
    # mentions it, only the union is missing it.
    expect(
        "C5 union dropped one cause",
        check_c5_lock_cause_union(
            c5_doc.replace('       |"orphan_sweep";', "       ;"), c5_go
        ),
        True,
    )
    # Naming a cause in PROSE beside the schema must not satisfy the union —
    # that is precisely how the real doc looked while being wrong.
    expect(
        "C5 prose mention is not coverage",
        check_c5_lock_cause_union(
            c5_doc.replace('       |"orphan_sweep";', "       ;")
            + '// "orphan_sweep" is the gc sweep\n',
            c5_go,
        ),
        True,
    )
    for label, doc, exc_expected in (
        ("C5 missing schema block", "nothing here", "could not find"),
        (
            "C5 union unparseable",
            "// lock_acquired  (x)\n{ no union here }\n// lock_released (x)\n",
            "no `cause:` union",
        ),
    ):
        try:
            check_c5_lock_cause_union(doc, c5_go)
            failures.append(f"{label}: expected InstrumentFailure, got none")
        except InstrumentFailure as exc:
            if exc_expected not in str(exc):
                failures.append(f"{label}: wrong InstrumentFailure: {exc}")
    try:
        check_c5_lock_cause_union(c5_doc, "package domain\n")
        failures.append("C5 no Go constants: expected InstrumentFailure, got none")
    except InstrumentFailure:
        pass
    # And against the real repo, for the same reason C4 is: a block that moved
    # reports here before it reports in CI.
    for label, path in (
        ("C5 design doc", DESIGN_DOC_MD),
        ("C5 resource_events.go", RESOURCE_EVENTS_GO),
    ):
        if not os.path.exists(path):
            failures.append(
                f"{label} is missing at {path}, so C5 has nothing to compare. "
                "Retarget C5 or retire it; do not leave it pointing at a "
                "deleted file."
            )
    if os.path.exists(DESIGN_DOC_MD) and os.path.exists(RESOURCE_EVENTS_GO):
        with open(DESIGN_DOC_MD, encoding="utf-8") as fh:
            real_design = fh.read()
        with open(RESOURCE_EVENTS_GO, encoding="utf-8") as fh:
            real_go = fh.read()
        try:
            expect(
                "C5 passes on this repo's lock-cause unions",
                check_c5_lock_cause_union(real_design, real_go),
                False,
            )
        except InstrumentFailure as exc:
            failures.append(f"C5 cannot read this repo's lock-cause unions: {exc}")

    if failures:
        for failure in failures:
            print(f"SELF-TEST FAIL: {failure}", file=sys.stderr)
        return 1
    print("pf_docs_contract_check self-test: OK")
    return 0


# ─── main ─────────────────────────────────────────────────────────────────────


def markdown_files(root: str) -> list[str]:
    out = []
    for dirpath, _dirnames, filenames in os.walk(root):
        for filename in filenames:
            if filename.endswith(".md"):
                out.append(os.path.join(dirpath, filename))
    return sorted(out)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--schemas",
        help="Path to `polyforge dump-mcp-schemas` output (required unless --self-test)",
    )
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()

    if args.self_test:
        return self_test()

    if not args.schemas:
        print(
            "error: --schemas is required. Build the binary and dump the "
            "schema; do not skip C3 silently — an absent check is not a "
            "passing one.",
            file=sys.stderr,
        )
        return 2

    # An empty file list satisfies every check vacuously, so the instrument has
    # to prove it found something before its silence can mean anything. This
    # matters concretely: docs/superpowers/ now holds exactly two archived
    # files, so one reorganization would turn C2 into a no-op that still
    # prints OK. Treat an empty scan as instrument failure, not a pass.
    # Guard the globs that can legitimately go empty. Note this checks the
    # SUPERPOWERS glob on its own rather than C2's full input list: c2_files
    # always contains MCP_TOOLS_MD, so a guard on the combined list could never
    # fire and would only look like protection.
    c1_files = markdown_files(DOCS)
    superpowers_files = markdown_files(SUPERPOWERS)
    for label, files, root in (
        ("C1", c1_files, DOCS),
        ("C2", superpowers_files, SUPERPOWERS),
    ):
        if not files:
            print(
                f"error: {label} matched no markdown files under {root}. That is "
                "instrument failure, not a clean result — the check cannot pass "
                "by having nothing to look at.",
                file=sys.stderr,
            )
            return 2
    if not os.path.exists(MCP_TOOLS_MD):
        print(
            f"error: {MCP_TOOLS_MD} is missing, so C3 has no inventory to check. "
            "If the page was intentionally removed, remove check C3 with it "
            "rather than letting it disappear silently.",
            file=sys.stderr,
        )
        return 2
    if not os.path.exists(AUDIT_411_MD):
        print(
            f"error: {AUDIT_411_MD} is missing, so C4 has no tally to recount. "
            "If §6 moved, retarget C4; if the document was retired, remove C4 "
            "with it rather than letting the check disappear silently.",
            file=sys.stderr,
        )
        return 2
    for label, path in (
        ("design doc", DESIGN_DOC_MD),
        ("Go source", RESOURCE_EVENTS_GO),
    ):
        if not os.path.exists(path):
            print(
                f"error: C5's {label} is missing at {path}, so it has nothing "
                "to compare. Retarget C5 or remove it rather than letting the "
                "check disappear silently.",
                file=sys.stderr,
            )
            return 2
    # Instrument integrity, not a docs error: C1's messages route authors to a
    # README section, so a dangling pointer stops the run rather than shipping
    # advice nobody can follow.
    form_doc_errors = check_citation_form_is_documented()
    if form_doc_errors:
        for error in form_doc_errors:
            print(f"error: {error}", file=sys.stderr)
        return 2

    # Same distinction one step further: C4's PARSE is instrument-level, so a
    # §6 it cannot read stops the run here rather than reporting a recount it
    # never performed. Done before any error accumulates so the exit code is
    # unambiguous — 2 means "the gate did not run", never "the gate ran clean".
    with open(AUDIT_411_MD, encoding="utf-8") as fh:
        audit_411_text = fh.read()
    try:
        c4_errors = check_c4_411_tally(audit_411_text)
    except InstrumentFailure as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    # C5's parse is instrument-level for the same reason.
    with open(DESIGN_DOC_MD, encoding="utf-8") as fh:
        design_text = fh.read()
    with open(RESOURCE_EVENTS_GO, encoding="utf-8") as fh:
        resource_events_text = fh.read()
    try:
        c5_errors = check_c5_lock_cause_union(design_text, resource_events_text)
    except InstrumentFailure as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    c2_files = superpowers_files + [MCP_TOOLS_MD]

    errors: list[str] = []
    errors += check_c1_no_line_citations(c1_files)
    # C2 covers docs/mcp-tools.md as well as the archive: that file is where
    # this gate's own headline fix put path-qualified anchors, and an anchor
    # nothing checks rots exactly as quietly as the line number it replaced.
    # It deliberately does NOT cover docs/design/polyforge-v1-design.md, which
    # is a design document and legitimately names files that do not exist yet.
    errors += check_c2_referenced_go_files_exist(c2_files)
    errors += c4_errors
    errors += c5_errors

    with open(args.schemas, encoding="utf-8") as fh:
        schema_tools = set(json.load(fh)["tools"])
    with open(MCP_TOOLS_MD, encoding="utf-8") as fh:
        errors += check_c3_tool_inventory(fh.read(), schema_tools)

    if errors:
        for error in errors:
            print(f"::error::{error}", file=sys.stderr)
        print(f"\n{len(errors)} docs-contract error(s).", file=sys.stderr)
        return 1

    print(
        f"OK: docs/ line-number citations closed; superpowers Go references "
        f"resolve; mcp-tools.md matches all {len(schema_tools)} registered "
        f"tools; the aihub#411 §6 tally matches a recount of its rows; the "
        f"design doc's lock-cause unions cover exactly the "
        f"{len(set(GO_LOCK_CAUSE_RE.findall(resource_events_text)))} "
        f"lockCause* constants."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
