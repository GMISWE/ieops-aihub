#!/usr/bin/env python3
"""Gate docs/ against the executable authorities it describes (aihub#352).

Three checks. Each one goes RED on a real drift, and each exists because the
drift it catches is otherwise SILENT — nothing in this repo turns red today when
a doc's copy of a code fact stops matching the code.

  C1  No `<file>.go:<line>` citations anywhere in docs/.
      A line number is an anchor that rots on the next refactor with no
      compiler, test, or linter noticing. Measured on 2026-09-03 against
      e8fbfcb: of the 23 real line-number citations in docs/superpowers/, 18
      pointed at the wrong line and 3 more were off by one or two. The
      replacement is a semantic anchor — file plus symbol name — which C2
      verifies. This check exists so the class stays closed.

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
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(REPO_ROOT, "docs")
MCP_TOOLS_MD = os.path.join(DOCS, "mcp-tools.md")
SUPERPOWERS = os.path.join(DOCS, "superpowers")

# ─── C1 ───────────────────────────────────────────────────────────────────────

# A citation is a source filename followed by a line number. The extension set
# is deliberately source-only: a filename plus extension plus `:digits` in prose
# is a line citation, whereas a bare `:8080` is a port. Verified against all of
# docs/ on 2026-09-03 — zero false positives.
GO_LINE_CITATION = re.compile(
    r"(?<![\w/.-])([\w/.-]+\.(?:go|sql|py|ts|tsx|js|sh|yml|yaml)):(\d+)"
)

# The filename-less form, e.g. `:1195-1202`. Same rot, less information: it does
# not even say which file, so it is uncheckable by construction.
#
# ONLY the RANGE form is matched. A single `:563` is indistinguishable from a
# port (`:8080`, `:8085` — docs/deployment.md has five such, all ports), and a
# regex wide enough to catch it reports those five as citations, burying the real
# signal in false positives. A range is unambiguous: no port is written `:N-M`.
# Reintroducing a filename-less SINGLE line anchor is therefore NOT gated — see
# the C-tagged note in the aihub#352 report. Do not widen this to single values
# without a discriminator that clears docs/deployment.md.
BARE_LINE_ANCHOR = re.compile(r"`:(\d+)-(\d+)`")

# Matches that are NOT citations, keyed (docs-relative path, exact token).
#
# THIS IS THE ONLY ESCAPE HATCH IN C1, AND IT LIVES IN THE GATE'S OWN FILE ON
# PURPOSE: widening it is a reviewable edit to this script, not a doc edit that
# slips through under a docs-only diff. Each entry needs a reason. Do not add an
# entry to make a real citation pass — replace the citation with a semantic
# anchor instead, which is the whole point of C1.
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


def check_c1_no_line_citations(paths: list[str]) -> list[str]:
    """No `<file>.go:<line>` citations in docs/, except the allowlisted samples."""
    errors = []
    for path in paths:
        rel = os.path.relpath(path, DOCS)
        with open(path, encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, 1):
                for match in GO_LINE_CITATION.finditer(line):
                    token = f"{match.group(1)}:{match.group(2)}"
                    if (rel, token) in C1_ALLOWED:
                        continue
                    errors.append(
                        f"docs/{rel}:{lineno}: line-number citation `{token}`. "
                        "Line numbers rot silently — nothing goes red when the "
                        "code moves. Cite the file plus the symbol name "
                        f"instead, e.g. `{match.group(1)}` (`SomeFunc`), which "
                        "C2 verifies."
                    )
                for match in BARE_LINE_ANCHOR.finditer(line):
                    errors.append(
                        f"docs/{rel}:{lineno}: filename-less line anchor "
                        f"`:{match.group(1)}-{match.group(2)}`. It does not say "
                        "which file, so it can never be checked or repaired. "
                        "Name the file and the symbol instead."
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

    # C1 regex: fires on a citation, ignores a bare filename.
    assert GO_LINE_CITATION.search("see internal/domain/memory.go:1444 for it")
    assert GO_LINE_CITATION.search("migrations/0006_events.sql:141 is NOT NULL")
    assert not GO_LINE_CITATION.search("see internal/domain/memory.go for it")
    assert not GO_LINE_CITATION.search("`UpdateMemory` in internal/domain/memory.go")

    # C1b: range anchors fire; ports must NOT. docs/deployment.md carries five
    # `:8080`/`:8085` ports and a regex that flags them is useless.
    assert BARE_LINE_ANCHOR.search("the cursor predicate at `:1195-1202` and")
    assert not BARE_LINE_ANCHOR.search("a single Go HTTP server (listens on `:8080`)")
    assert not BARE_LINE_ANCHOR.search("published on `:8085`. A deploy never touches it")
    assert not BARE_LINE_ANCHOR.search("`-p 8080:8080`")

    # C2 regex: path-qualified only.
    assert GO_PATH_REF.search("internal/domain/memory.go")
    assert not GO_PATH_REF.search("just memory.go alone")

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

    errors: list[str] = []
    errors += check_c1_no_line_citations(markdown_files(DOCS))
    errors += check_c2_referenced_go_files_exist(markdown_files(SUPERPOWERS))

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
