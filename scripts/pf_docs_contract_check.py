#!/usr/bin/env python3
"""Gate docs/ against the executable authorities it describes (aihub#352).

Three checks. Each one goes RED on a real drift, and each exists because the
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
    # Instrument integrity, not a docs error: C1's messages route authors to a
    # README section, so a dangling pointer stops the run rather than shipping
    # advice nobody can follow.
    form_doc_errors = check_citation_form_is_documented()
    if form_doc_errors:
        for error in form_doc_errors:
            print(f"error: {error}", file=sys.stderr)
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
        f"resolve; mcp-tools.md matches all {len(schema_tools)} registered tools."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
