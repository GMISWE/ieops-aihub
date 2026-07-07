#!/usr/bin/env python3
# pf_contract_lint.py - Polyforge MCP tool-contract lint.
#
# Vendored from GMISWE/GMI-marketplace scripts/pf_contract_lint.py @ c97a04d.
# To refresh: re-copy from that path at a newer SHA, bump the SHA above, and
# re-apply the aihub#211 modification below.
# aihub#211 modification: the ENUM_VIOLATION rule also skips @@ROUTER_TOKEN@@
# substitution placeholders (the skill router injects them into _common/*.md),
# so an enum param whose value is a template token is not a false positive.
#
# Rules
# -----
# A. Contract parity (against JSON schema from --schemas):
#    a) Unknown tool name used in a pf_xxx(...) call fragment -> UNKNOWN_TOOL
#    b) Unknown kwarg for a known tool -> UNKNOWN_PARAM
#    c) Param has enum in schema AND literal string value not in enum -> ENUM_VIOLATION
#    Note: using *fewer* params than the schema requires is never a violation.
#    Special: declared_resources / requested_locks -> only name presence checked.
#
# B. Silent failure (in ```bash blocks):
#    Line matching || (true|echo) without trailing "# lint-allow: silent <reason>"
#    -> SILENT_FAILURE
#
# Baseline
# --------
# JSON array of {"file": ..., "rule": ..., "match": ..., "wi": ...}.
# Every baseline entry MUST have a "wi" field (enforced at load time).
# Violations matching a baseline entry are demoted to NOTICE (still printed).
#
# Output
# ------
#   <file>:<line> [RULE] description
# Exit code 1 if any un-baselined violations; 0 otherwise.
#
# Usage
# -----
#   pf_contract_lint.py --schemas <path|url> --target <dir> [--target <dir>...]
#                        [--baseline <json>] [--self-test]


import argparse
import json
import os
import re
import sys
import urllib.request
from dataclasses import dataclass
from typing import Optional


# ---------------------------------------------------------------------------
# Schema loading
# ---------------------------------------------------------------------------

def load_schema(source: str) -> dict:
    """Load schema from a file path or http(s) URL."""
    if source.startswith("http://") or source.startswith("https://"):
        req = urllib.request.Request(source, headers={"User-Agent": "pf-contract-lint/1"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = resp.read()
    else:
        with open(source, "rb") as f:
            data = f.read()
    return json.loads(data)


def load_baseline(path: Optional[str]) -> list:
    """Load and validate the baseline JSON array."""
    if not path:
        return []
    with open(path) as f:
        entries = json.load(f)
    if not isinstance(entries, list):
        raise ValueError(f"Baseline must be a JSON array, got {type(entries)}")
    for i, e in enumerate(entries):
        if not isinstance(e, dict):
            raise ValueError(f"Baseline entry {i} is not an object")
        if "wi" not in e or not e["wi"]:
            raise ValueError(
                f"Baseline entry {i} is missing required 'wi' field: {e!r}\n"
                "Every baseline entry must reference a work item (e.g. \"wi\": \"aihub#123\")"
            )
        # A baseline entry without file+rule+match would silently swallow whole
        # categories of future violations — require all three to keep each entry
        # scoped to exactly one known drift.
        for required in ("file", "rule", "match"):
            if not e.get(required):
                raise ValueError(
                    f"Baseline entry {i} is missing required '{required}' field: {e!r}\n"
                    "Every entry must pin file + rule + match (no wildcards)."
                )
    return entries


# ---------------------------------------------------------------------------
# Violation dataclass
# ---------------------------------------------------------------------------

@dataclass
class Violation:
    file: str
    line: int
    rule: str
    message: str
    match: str = ""

    def key(self):
        return (self.file, self.rule, self.match)


# ---------------------------------------------------------------------------
# Rule A: Contract parity
# ---------------------------------------------------------------------------

# Patterns to find pf_xxx( call fragments:
#   - inline code: `pf_xxx(...)`
#   - fenced code block: pf_xxx(
#   - plain prose: pf_xxx(
_TOOL_CALL_RE = re.compile(r'\bpf_(\w+)\s*\(([^)]*)\)?', re.DOTALL)
_KWARG_RE = re.compile(r'\b(\w+)\s*=')
_STRING_VALUE_RE = re.compile(r'\b(\w+)\s*=\s*["\']([^"\']*)["\']')

# Params that only have their name checked (not internal shape).
_SKIP_SHAPE_PARAMS = {"declared_resources", "requested_locks"}


def lint_rule_a(file_path: str, lines: list, schema: dict) -> list[Violation]:
    """Apply Rule A: contract parity against schema."""
    violations = []
    tools = schema.get("tools", {})

    # We scan line-by-line for tool call fragments, collecting multi-line blocks.
    # For simplicity we join all lines but track line offsets.
    full_text = "\n".join(lines)
    # Build a map: char_offset → line number (1-indexed).
    offsets = []
    pos = 0
    for i, ln in enumerate(lines, 1):
        offsets.append((pos, i))
        pos += len(ln) + 1  # +1 for \n

    def char_to_line(char_pos: int) -> int:
        lo, hi = 0, len(offsets) - 1
        while lo < hi:
            mid = (lo + hi + 1) // 2
            if offsets[mid][0] <= char_pos:
                lo = mid
            else:
                hi = mid - 1
        return offsets[lo][1]

    for m in _TOOL_CALL_RE.finditer(full_text):
        name = "pf_" + m.group(1)
        args_text = m.group(2)
        fragment = m.group(0)[:80]  # truncate for match key
        lineno = char_to_line(m.start())

        # (a) Unknown tool name.
        if name not in tools:
            violations.append(Violation(
                file=file_path, line=lineno,
                rule="UNKNOWN_TOOL",
                message=f"tool '{name}' not in schema",
                match=fragment,
            ))
            continue

        tool_params = tools[name].get("params", {})
        kwargs = [k.group(1) for k in _KWARG_RE.finditer(args_text)]
        string_vals = {
            kv.group(1): kv.group(2)
            for kv in _STRING_VALUE_RE.finditer(args_text)
        }

        for kw in kwargs:
            # (b) Unknown kwarg.
            if kw not in tool_params:
                # Skip shape-only params.
                if kw in _SKIP_SHAPE_PARAMS:
                    continue
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="UNKNOWN_PARAM",
                    message=f"tool '{name}': param '{kw}' not in schema",
                    match=fragment,
                ))
                continue

            # (c) Enum violation: literal string not in allowed enum.
            param_def = tool_params.get(kw, {})
            enum_vals = param_def.get("enum")
            if not enum_vals:
                continue
            if kw not in string_vals:
                continue  # not a literal string value -> no check
            val = string_vals[kw]
            # Skip placeholder patterns: enum wildcards (* or |) and @@ROUTER_TOKEN@@
            # substitution placeholders injected by the skill router (aihub#211).
            if "*" in val or "|" in val or "@@" in val:
                continue
            if val not in enum_vals:
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="ENUM_VIOLATION",
                    message=(
                        f"tool '{name}': param '{kw}' value '{val}' "
                        f"not in enum {enum_vals}"
                    ),
                    match=fragment,
                ))

    return violations


# ---------------------------------------------------------------------------
# Rule B: Silent failure in bash fenced blocks
# ---------------------------------------------------------------------------

_SILENT_RE = re.compile(r'\|\|\s*(true|echo)\b')
_LINT_ALLOW_RE = re.compile(r'#\s*lint-allow:\s*silent\b')

def lint_rule_b(file_path: str, lines: list) -> list[Violation]:
    """Apply Rule B: || true / || echo in bash fenced blocks."""
    violations = []
    in_bash_block = False

    for lineno, ln in enumerate(lines, 1):
        stripped = ln.strip()
        # Detect fenced code block delimiters.
        if stripped.startswith("```"):
            lang = stripped[3:].strip().lower()
            if not in_bash_block:
                # Opening: check if it's a bash/shell block.
                if lang in ("bash", "sh", "shell", "zsh"):
                    in_bash_block = True
            else:
                # Closing fence (any ``` closes).
                in_bash_block = False
            continue

        if in_bash_block:
            m = _SILENT_RE.search(ln)
            if m and not _LINT_ALLOW_RE.search(ln):
                # Window the fragment around the match so the `|| true/echo`
                # itself stays visible (and baseline-matchable) on long lines.
                s = ln.strip()
                idx = max(0, s.find(m.group(0)))
                start = max(0, idx - 60)
                match_text = s[start:idx + len(m.group(0)) + 20][:100]
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="SILENT_FAILURE",
                    message=f"|| true/echo without lint-allow: silent annotation",
                    match=match_text,
                ))

    return violations


# ---------------------------------------------------------------------------
# Baseline matching
# ---------------------------------------------------------------------------

def match_baseline(v: Violation, baseline: list) -> Optional[dict]:
    """Return the first baseline entry that matches this violation, or None.

    The 'match' field is compared with word-boundary anchoring against v.match
    (the truncated call fragment) and v.message — so an entry like 'ids' matches
    the param token `ids` but NOT `work_item_ids`/`guids`/`rapids`. This keeps
    each baseline entry scoped to exactly the violation it was written for.
    """
    for entry in baseline:
        if entry.get("rule") and entry["rule"] != v.rule:
            continue
        if entry.get("file") and not v.file.endswith(entry["file"]):
            continue
        if entry.get("match"):
            needle_re = re.compile(
                r"(?<![0-9A-Za-z_])" + re.escape(entry["match"]) + r"(?![0-9A-Za-z_])"
            )
            if not needle_re.search(v.match) and not needle_re.search(v.message):
                continue
        return entry
    return None


# ---------------------------------------------------------------------------
# File scanning
# ---------------------------------------------------------------------------

def collect_md_files(targets: list) -> list:
    """Collect all *.md files from target directories (recursive)."""
    files = []
    for target in targets:
        if os.path.isfile(target):
            if target.endswith(".md"):
                files.append(target)
        else:
            for root, dirs, fnames in os.walk(target):
                # Skip hidden dirs.
                dirs[:] = [d for d in dirs if not d.startswith(".")]
                for fn in sorted(fnames):
                    if fn.endswith(".md"):
                        files.append(os.path.join(root, fn))
    return sorted(files)


def lint_file(file_path: str, schema: dict) -> list[Violation]:
    """Run both rule sets on a single file."""
    try:
        with open(file_path, encoding="utf-8") as f:
            content = f.read()
    except OSError as e:
        return [Violation(file=file_path, line=0, rule="IO_ERROR", message=str(e))]

    lines = content.splitlines()
    violations = []
    violations.extend(lint_rule_a(file_path, lines, schema))
    violations.extend(lint_rule_b(file_path, lines))
    return violations


# ---------------------------------------------------------------------------
# Self-test
# ---------------------------------------------------------------------------

_FIXTURES = {
    # fmt: off
    "unknown_tool.md": (
        "# Test\n"
        "`pf_nonexistent(work_item_id=\"foo\")`\n",
        [("UNKNOWN_TOOL", "pf_nonexistent")],
    ),
    "unknown_param.md": (
        "# Test\n"
        "`pf_recall(project=\"foo\", bad_param=\"bar\")`\n",
        [("UNKNOWN_PARAM", "bad_param")],
    ),
    "enum_violation.md": (
        "# Test\n"
        '`pf_remember(project="foo", type="not_a_real_type", content="x", visibility="project")`\n',
        [("ENUM_VIOLATION", "not_a_real_type")],
    ),
    "valid_subset.md": (
        "# Test\n"
        # Using fewer params than required is always valid.
        "`pf_recall(project=\"foo\")`\n",
        [],  # no violations
    ),
    "lint_allow.md": (
        "# Test\n"
        "```bash\n"
        "some_cmd || true  # lint-allow: silent pre-existing pattern from publish_dev\n"
        "```\n",
        [],  # exempted by annotation
    ),
    "silent_failure.md": (
        "# Test\n"
        "```bash\n"
        "some_cmd || true\n"
        "```\n",
        [("SILENT_FAILURE", "|| true")],
    ),
    "baseline_demotion.md": (
        "# Test\n"
        "`pf_nonexistent2(work_item_id=\"foo\")`\n",
        [],  # baselined → NOTICE only, not a failure
    ),
    "save_artifact_bare_type.md": (
        "# Test\n"
        '`pf_save_artifact(type="spec", work_item_id="x", content="y")`\n',
        [("ENUM_VIOLATION", "spec")],  # server 400s on bare "spec"; must be methodology.spec (aihub#211)
    ),
    "save_artifact_router_placeholder.md": (
        "# Test\n"
        '`pf_save_artifact(type="@@ARTIFACT_TYPE@@", work_item_id="x", content="y")`\n',
        [],  # @@...@@ router substitution token is skipped, not an enum violation (aihub#211)
    ),
    # fmt: on
}

_MINIMAL_SCHEMA = {
    "generated_from": "test",
    "tools": {
        "pf_recall": {
            "description": "Recall memories",
            "params": {
                "project": {"type": "string", "required": True},
                "query": {"type": "string", "required": False},
            },
        },
        "pf_remember": {
            "description": "Store memory",
            "params": {
                "project": {"type": "string", "required": True},
                "type": {
                    "type": "string",
                    "required": True,
                    "enum": [
                        "experience.debug", "experience.approach", "experience.pitfall",
                        "experience.code", "fact.architecture", "fact.constraint",
                        "fact.reference", "fact.note", "rule.scheduling",
                        "rule.convention", "rule.process", "rule.coding", "rule.work",
                        "methodology.spec", "methodology.plan", "methodology.review",
                        "methodology.execute", "methodology.retro", "methodology.wrap_summary",
                    ],
                },
                "content": {"type": "string", "required": True},
                "visibility": {"type": "string", "required": True},
            },
        },
        "pf_save_artifact": {
            "description": "Save a methodology artifact",
            "params": {
                "type": {
                    "type": "string",
                    "required": True,
                    "enum": [
                        "methodology.spec", "methodology.plan", "methodology.review",
                        "methodology.execute", "methodology.retro", "methodology.wrap_summary",
                    ],
                },
                "work_item_id": {"type": "string", "required": True},
                "content": {"type": "string", "required": False},
            },
        },
    },
}

_SELF_TEST_BASELINE = [
    {"file": "baseline_demotion.md", "rule": "UNKNOWN_TOOL", "match": "pf_nonexistent2", "wi": "aihub#999"},
]


def run_self_test():
    """Run built-in fixture tests; exit(1) on failure."""
    import tempfile

    failures = []
    passed = 0

    with tempfile.TemporaryDirectory() as tmpdir:
        for fixture_name, (content, expected) in _FIXTURES.items():
            fpath = os.path.join(tmpdir, fixture_name)
            with open(fpath, "w") as f:
                f.write(content)

            viols = lint_file(fpath, _MINIMAL_SCHEMA)

            # Apply baseline for the baseline_demotion fixture.
            if fixture_name == "baseline_demotion.md":
                remaining = [v for v in viols if not match_baseline(v, _SELF_TEST_BASELINE)]
            else:
                remaining = viols

            if not expected:
                # Expect no violations.
                if remaining:
                    failures.append(
                        f"FAIL {fixture_name}: expected no violations, got {[v.rule for v in remaining]}"
                    )
                else:
                    passed += 1
            else:
                # Check each expected (rule, fragment) is present.
                for exp_rule, exp_fragment in expected:
                    found = any(
                        v.rule == exp_rule and exp_fragment in (v.match + v.message)
                        for v in viols
                    )
                    if not found:
                        failures.append(
                            f"FAIL {fixture_name}: expected {exp_rule!r} containing {exp_fragment!r}, "
                            f"got violations: {[(v.rule, v.match) for v in viols]}"
                        )
                    else:
                        passed += 1

    if failures:
        print(f"self-test: {len(failures)} failure(s):")
        for f in failures:
            print(f"  {f}")
        sys.exit(1)
    else:
        print(f"self-test: {passed} fixture(s) passed")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Lint polyforge MCP tool calls against schema contract."
    )
    parser.add_argument("--schemas", help="Path or URL to pf-tool-schemas.json")
    parser.add_argument(
        "--target", action="append", dest="targets",
        help="Directory or file to lint (repeatable)",
    )
    parser.add_argument("--baseline", help="Path to baseline JSON file")
    parser.add_argument(
        "--self-test", action="store_true",
        help="Run built-in fixture tests and exit",
    )
    args = parser.parse_args()

    if args.self_test:
        run_self_test()
        sys.exit(0)

    if not args.schemas:
        parser.error("--schemas is required (unless --self-test)")
    if not args.targets:
        parser.error("--target is required (unless --self-test)")

    # Load schema.
    try:
        schema = load_schema(args.schemas)
    except Exception as e:
        print(f"ERROR: failed to load schemas from {args.schemas!r}: {e}", file=sys.stderr)
        sys.exit(1)

    # Load baseline.
    try:
        baseline = load_baseline(args.baseline)
    except Exception as e:
        print(f"ERROR: failed to load baseline: {e}", file=sys.stderr)
        sys.exit(1)

    # Collect files.
    files = collect_md_files(args.targets)
    if not files:
        print("No .md files found in specified targets.", file=sys.stderr)
        sys.exit(0)

    all_violations = []
    for fpath in files:
        viols = lint_file(fpath, schema)
        all_violations.extend(viols)

    # Categorize and print.
    has_error = False
    notice_count = 0
    error_count = 0

    for v in sorted(all_violations, key=lambda x: (x.file, x.line)):
        b = match_baseline(v, baseline)
        if b:
            level = "NOTICE"
            notice_count += 1
        else:
            level = "ERROR"
            error_count += 1
            has_error = True

        wi_ref = f" (baseline: {b['wi']})" if b else ""
        print(f"{v.file}:{v.line} [{v.rule}] {v.message}{wi_ref}")

    # Summary.
    total = len(all_violations)
    print(
        f"\n{total} violation(s): {error_count} error(s), {notice_count} notice(s) "
        f"in {len(files)} file(s)"
    )

    if has_error:
        sys.exit(1)


if __name__ == "__main__":
    main()
