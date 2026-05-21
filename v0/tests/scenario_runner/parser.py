"""Parse a `*.scenario.md` file into a structured Scenario object.

The format spec lives in `tests/scenarios/README.md`. This parser is
intentionally strict — unknown YAML keys or malformed actions raise
loud errors. Better to fail at parse time than mid-execution.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


@dataclass
class Action:
    """A single imperative step in a timeline block.

    Exactly one of {skill, bash, sleep} is set.
    """
    kind: str                                 # "skill" | "bash" | "sleep" | "repeat"
    payload: str | float                      # skill cmd string, bash cmd, sleep seconds, or repeat count
    # capture / on_error_capture: {result_field_name: var_name}
    # e.g. {"work_item_id": "$WI"} means "store result['work_item_id'] into vars[WI]"
    capture: dict[str, str] = field(default_factory=dict)
    on_error_capture: dict[str, str] = field(default_factory=dict)
    # For "repeat": payload = N, child action lives in `inner`
    inner: "Action | None" = None


@dataclass
class TimelineBlock:
    """A `### T+N: title` block in the timeline."""
    title: str                                # "T+0: Wang on mbp starts work"
    cast_ids: list[str]                       # ["wang-mbp"] or ["dev-zhang", "dev-wang"]
    actions: list[Action]
    parallel: bool                            # True if multiple cast_ids


@dataclass
class CastMember:
    id: str
    user: str
    machine_id: str
    session_secret: str | None = None
    bearer_env: str | None = None


@dataclass
class Assertion:
    """One assertion in the ## Assertions section."""
    kind: str          # "compare" | "sql" | "file" | "one_succeeds" | "loser" | "no_payload_collision"
    spec: dict[str, Any]


@dataclass
class Scenario:
    name: str
    description: str
    env: dict[str, str]
    cast: list[CastMember]
    expected_runtime_s: int
    background: str
    timeline: list[TimelineBlock | Action]   # Action used for top-level sleeps
    assertions: list[Assertion]
    source_path: Path


def parse_scenario(path: Path) -> Scenario:
    text = path.read_text(encoding="utf-8")

    # Split frontmatter from body
    if not text.startswith("---\n"):
        raise ValueError(f"{path}: missing YAML frontmatter (must start with '---')")
    parts = text.split("\n---\n", 1)
    if len(parts) != 2:
        raise ValueError(f"{path}: malformed frontmatter delimiters")
    fm_yaml = parts[0][len("---\n"):]
    body = parts[1]

    meta = yaml.safe_load(fm_yaml)
    for required in ("name", "description", "cast"):
        if required not in meta:
            raise ValueError(f"{path}: frontmatter missing required key '{required}'")

    cast = [
        CastMember(
            id=c["id"], user=c.get("user", ""), machine_id=c.get("machine_id", ""),
            session_secret=c.get("session_secret"),
            bearer_env=c.get("bearer_env"),
        )
        for c in meta["cast"]
    ]
    cast_ids = {c.id for c in cast}

    # Parse body sections
    sections = _split_sections(body)
    if "Timeline" not in sections:
        raise ValueError(f"{path}: missing ## Timeline section")
    if "Assertions" not in sections:
        raise ValueError(f"{path}: missing ## Assertions section")

    timeline = _parse_timeline(sections["Timeline"], cast_ids, path)
    assertions = _parse_assertions(sections["Assertions"], path)

    return Scenario(
        name=meta["name"],
        description=meta["description"],
        env=meta.get("env") or {},
        cast=cast,
        expected_runtime_s=meta.get("expected_runtime_s", 60),
        background=sections.get("Background", "").strip(),
        timeline=timeline,
        assertions=assertions,
        source_path=path,
    )


def _split_sections(body: str) -> dict[str, str]:
    """Split body by '## H2' headers."""
    sections: dict[str, str] = {}
    current_name: str | None = None
    current_lines: list[str] = []
    for line in body.splitlines():
        m = re.match(r"^## (.+?)\s*$", line)
        if m:
            if current_name is not None:
                sections[current_name] = "\n".join(current_lines).strip()
            current_name = m.group(1).strip()
            current_lines = []
        else:
            current_lines.append(line)
    if current_name is not None:
        sections[current_name] = "\n".join(current_lines).strip()
    return sections


def _parse_timeline(text: str, cast_ids: set[str], path: Path) -> list:
    """Parse '### T+N: title' blocks + top-level - sleep: items."""
    out: list = []
    # Split on '### T' boundaries (preserve top-level - items between them)
    blocks = re.split(r"\n(?=### T)", "\n" + text)
    for block in blocks:
        block = block.strip()
        if not block:
            continue
        if block.startswith("### T"):
            out.append(_parse_timeline_block(block, cast_ids, path))
        else:
            # Top-level actions (e.g., '- sleep: 3' between blocks)
            for line in block.splitlines():
                if line.strip().startswith("- sleep:"):
                    secs = float(line.split(":", 1)[1].strip())
                    out.append(Action(kind="sleep", payload=secs))
    return out


def _parse_timeline_block(block_text: str, cast_ids: set[str], path: Path) -> TimelineBlock | Action:
    lines = block_text.splitlines()
    title_line = lines[0]
    title_m = re.match(r"^### (T\+\S+:?\s*.*?)\s*$", title_line)
    title = title_m.group(1) if title_m else title_line.lstrip("# ")

    body = "\n".join(lines[1:]).strip()

    # Top-level sleep within a titled block
    if body.startswith("- sleep:"):
        secs = float(body.split(":", 1)[1].strip())
        return Action(kind="sleep", payload=secs)

    # @cast-id[, @cast-id]: header, optionally followed by `# comment`
    m = re.match(r"^@([\w\-, @]+):[ \t]*(?:#[^\n]*)?\n(.*)", body, re.DOTALL)
    if not m:
        raise ValueError(
            f"{path}: block '{title}' has no '@cast-id:' header. Body was:\n{body[:200]}"
        )
    cast_header = m.group(1)
    actions_text = m.group(2)
    actor_ids = [a.strip().lstrip("@") for a in cast_header.split(",")]
    for aid in actor_ids:
        if aid not in cast_ids:
            raise ValueError(
                f"{path}: block '{title}' references unknown cast id '{aid}'; "
                f"known: {sorted(cast_ids)}"
            )

    actions = _parse_actions(actions_text, path, title)
    return TimelineBlock(
        title=title, cast_ids=actor_ids, actions=actions,
        parallel=len(actor_ids) > 1,
    )


def _parse_actions(text: str, path: Path, ctx: str) -> list[Action]:
    """Parse YAML-ish action list. We use yaml.safe_load on the trimmed text."""
    # The body is a list of mappings starting with '-'. Parse via PyYAML.
    try:
        items = yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise ValueError(f"{path}: block '{ctx}' action YAML invalid: {e}") from e
    if items is None:
        return []
    if not isinstance(items, list):
        raise ValueError(f"{path}: block '{ctx}' actions must be a YAML list; got {type(items).__name__}")
    return [_action_from_dict(d, path, ctx) for d in items]


def _action_from_dict(d: dict, path: Path, ctx: str) -> Action:
    if "skill" in d:
        on_error = d.get("on_error") or {}
        # Honor the user-supplied var name. Two accepted shapes:
        #   on_error:
        #     code: $ERR_VAR           ← capture err.code into vars[ERR_VAR]
        #     message: $MSG            ← capture err.message into vars[MSG]
        # Legacy shorthand (deprecated, still parsed):
        #   on_error:
        #     capture_code: $ERR_VAR   ← same as `code: $ERR_VAR`
        on_error_capture: dict[str, str] = {}
        if "capture_code" in on_error:
            on_error_capture["code"] = on_error["capture_code"]
        for attr in ("code", "message", "http_status"):
            if attr in on_error:
                on_error_capture[attr] = on_error[attr]
        return Action(
            kind="skill",
            payload=d["skill"].strip(),
            capture=d.get("capture") or {},
            on_error_capture=on_error_capture,
        )
    if "bash" in d:
        return Action(kind="bash", payload=d["bash"].strip(), capture=d.get("capture") or {})
    if "sleep" in d:
        return Action(kind="sleep", payload=float(d["sleep"]))
    if "repeat" in d:
        # `repeat: N` + `skill: ...` in same dict
        inner = _action_from_dict({k: v for k, v in d.items() if k != "repeat"}, path, ctx)
        return Action(kind="repeat", payload=int(d["repeat"]), inner=inner)
    if "mcp" in d:
        raise NotImplementedError(
            "mcp: actions are not supported in Mode-1 executor; "
            "use Mode-2 /execute-scenario for MCP tool dispatch"
        )
    raise ValueError(
        f"{path}: block '{ctx}' has action with no recognized kind "
        f"(expected one of skill/bash/sleep/repeat); got keys {list(d.keys())}"
    )


def _parse_assertions(text: str, path: Path) -> list[Assertion]:
    try:
        items = yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise ValueError(f"{path}: assertions YAML invalid: {e}") from e
    if not isinstance(items, list):
        raise ValueError(f"{path}: assertions must be a YAML list")
    out = []
    for d in items:
        if not isinstance(d, dict) or len(d) == 0:
            raise ValueError(f"{path}: each assertion must be a mapping with one kind key")
        # The first key (other than sub-keys) is the assertion kind
        kind = next(iter(d.keys()))
        spec = d[kind] if isinstance(d[kind], dict) else {"value": d[kind]}
        out.append(Assertion(kind=kind, spec=spec))
    return out
